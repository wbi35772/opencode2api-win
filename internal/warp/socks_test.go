package warp

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/conn"
)

func TestNormalizeSocksUDP(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"127.0.0.1:1080", "socks5://127.0.0.1:1080"},
		{"socks5://127.0.0.1", "socks5://127.0.0.1:1080"},
		{"socks5h://user:pass@127.0.0.1:1081", "socks5://user:pass@127.0.0.1:1081"},
		{"  socks5://[::1]:9050  ", "socks5://[::1]:9050"},
	}
	for _, testCase := range cases {
		got, err := NormalizeSocksUDP(testCase.input)
		if err != nil {
			t.Fatalf("NormalizeSocksUDP(%q): %v", testCase.input, err)
		}
		if got != testCase.want {
			t.Fatalf("NormalizeSocksUDP(%q) = %q want %q", testCase.input, got, testCase.want)
		}
	}
	for _, input := range []string{"", "http://127.0.0.1:1080", "socks5://127.0.0.1:0", "socks5://:1080"} {
		if _, err := NormalizeSocksUDP(input); err == nil {
			t.Fatalf("NormalizeSocksUDP(%q) accepted an invalid value", input)
		}
	}
}

func TestSocksUDPBindRoundTrip(t *testing.T) {
	server := startFakeSocks5(t, false)
	bind, err := openTestBind(t, server.addr())
	if err != nil {
		t.Fatalf("open relayed bind: %v", err)
	}
	defer bind.Close()

	destination := netip.MustParseAddrPort("1.2.3.4:2408")
	payload := []byte("wireguard handshake initiation")
	if err := bind.Send([][]byte{payload}, &simpleEndpoint{addr: destination}); err != nil {
		t.Fatalf("send through relay: %v", err)
	}
	n, address, echoed := receiveOne(t, bind, 3*time.Second)
	if n != 1 {
		t.Fatalf("receive count = %d", n)
	}
	if address != destination {
		t.Fatalf("received from %s want %s", address, destination)
	}
	if string(echoed) != string(payload) {
		t.Fatalf("received payload %q want %q", echoed, payload)
	}
	if server.datagrams.Load() != 1 {
		t.Fatalf("relay saw %d datagrams want 1", server.datagrams.Load())
	}
}

func TestSocksUDPBindAuthenticates(t *testing.T) {
	server := startFakeSocks5(t, true)
	bind, err := openTestBind(t, "socks5://alice:secret@"+server.addr())
	if err != nil {
		t.Fatalf("open authenticated bind: %v", err)
	}
	defer bind.Close()
	if err := bind.Send([][]byte{[]byte("ping")}, &simpleEndpoint{addr: netip.MustParseAddrPort("1.1.1.1:2408")}); err != nil {
		t.Fatalf("send with credentials: %v", err)
	}
	if _, _, echoed := receiveOne(t, bind, 3*time.Second); string(echoed) != "ping" {
		t.Fatalf("echo = %q", echoed)
	}

	wrong, err := openTestBind(t, "socks5://alice:wrong@"+server.addr())
	if err == nil {
		wrong.Close()
		t.Fatal("wrong password was accepted")
	}
	if !strings.Contains(err.Error(), "authentication rejected") {
		t.Fatalf("wrong-password error = %v", err)
	}
}

func TestSocksUDPBindReconnectsAfterControlClose(t *testing.T) {
	server := startFakeSocks5(t, false)
	bind, err := openTestBind(t, server.addr())
	if err != nil {
		t.Fatalf("open relayed bind: %v", err)
	}
	defer bind.Close()

	destination := netip.MustParseAddrPort("162.159.192.1:2408")
	if err := bind.Send([][]byte{[]byte("first")}, &simpleEndpoint{addr: destination}); err != nil {
		t.Fatalf("first send: %v", err)
	}
	if _, _, echoed := receiveOne(t, bind, 3*time.Second); string(echoed) != "first" {
		t.Fatalf("first echo = %q", echoed)
	}

	bind.mu.Lock()
	control := bind.control
	bind.mu.Unlock()
	if control == nil {
		t.Fatal("bind has no control connection")
	}
	if err := control.Close(); err != nil {
		t.Fatalf("close control: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		bind.mu.Lock()
		dead := bind.dead
		bind.mu.Unlock()
		if dead {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("control watcher did not mark the association dead")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := bind.Send([][]byte{[]byte("second")}, &simpleEndpoint{addr: destination}); err != nil {
		t.Fatalf("send after reconnect: %v", err)
	}
	if _, _, echoed := receiveOne(t, bind, 3*time.Second); string(echoed) != "second" {
		t.Fatalf("second echo = %q", echoed)
	}
}

func TestRelayGuardTripsAndHalfOpens(t *testing.T) {
	guard := newRelayGuard(nil)
	guard.threshold = 2
	guard.cooldown = 40 * time.Millisecond

	if err := guard.acquire(); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	guard.failure(errors.New("relay write failed"))
	if err := guard.acquire(); err != nil {
		t.Fatalf("acquire after one failure: %v", err)
	}
	guard.failure(errors.New("relay write failed"))
	err := guard.acquire()
	if pause := relayUnavailablePause(err); pause <= 0 {
		t.Fatalf("open circuit admitted a registration: %v", err)
	}
	if !guard.open() {
		t.Fatal("guard reports closed while cooling down")
	}

	time.Sleep(60 * time.Millisecond)
	if err := guard.acquire(); err != nil {
		t.Fatalf("half-open probe was rejected: %v", err)
	}
	if err := guard.acquire(); err == nil {
		t.Fatal("second half-open probe was admitted")
	}
	guard.success()
	if guard.open() {
		t.Fatal("success did not close the circuit")
	}
	if err := guard.acquire(); err != nil {
		t.Fatalf("acquire after recovery: %v", err)
	}
}

func TestPenalizeKeepsExitWhileRelayCircuitOpen(t *testing.T) {
	broken := testExit()
	pool := testPool("", broken)
	pool.relay = newRelayGuard(nil)
	pool.relay.threshold = 1

	pool.penalize(broken, "timeout waiting for first event", 0, penaltyTransport)

	pool.mu.Lock()
	defer pool.mu.Unlock()
	if len(pool.active) != 1 || pool.active[0] != broken {
		t.Fatalf("exit was removed while the relay circuit was open: active=%d", len(pool.active))
	}
	if kind := broken.penaltyKind(); kind != penaltyNone {
		t.Fatalf("exit was marked with penalty %d", kind)
	}
	if len(pool.cooling) != 0 {
		t.Fatalf("exit was cooled down: %v", pool.cooling)
	}
}

func openTestBind(t *testing.T, proxy string) (*socksUDPBind, error) {
	t.Helper()
	parsed, err := parseSocks5Proxy(proxy)
	if err != nil {
		t.Fatalf("parse proxy %q: %v", proxy, err)
	}
	bind := newSocksUDPBind(parsed, nil, nil, false)
	_, _, err = bind.Open(0)
	return bind, err
}

func receiveOne(t *testing.T, bind *socksUDPBind, timeout time.Duration) (int, netip.AddrPort, []byte) {
	t.Helper()
	bind.mu.Lock()
	receive := bind.receive
	bind.mu.Unlock()
	packet := make([]byte, 1500)
	packets := [][]byte{packet}
	sizes := []int{0}
	endpoints := []conn.Endpoint{nil}
	type result struct {
		count    int
		size     int
		endpoint conn.Endpoint
		err      error
	}
	done := make(chan result, 1)
	go func() {
		count, err := receive(packets, sizes, endpoints)
		done <- result{count: count, size: sizes[0], endpoint: endpoints[0], err: err}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("receive: %v", got.err)
		}
		ep, ok := got.endpoint.(*simpleEndpoint)
		if !ok {
			t.Fatalf("endpoint type %T", got.endpoint)
		}
		return got.count, ep.addr, append([]byte(nil), packet[:got.size]...)
	case <-time.After(timeout):
		t.Fatal("timed out waiting for a relayed datagram")
		return 0, netip.AddrPort{}, nil
	}
}

// fakeSocks5 is a minimal RFC 1928 server: it completes TCP negotiation, opens
// one UDP socket per UDP ASSOCIATE, and echoes every encapsulated datagram back
// to the sender with the same destination header.
type fakeSocks5 struct {
	listener    net.Listener
	requireAuth bool

	mu       sync.Mutex
	controls []net.Conn
	relays   []*net.UDPConn
	closed   bool

	datagrams atomic.Int64
}

func startFakeSocks5(t *testing.T, requireAuth bool) *fakeSocks5 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen fake SOCKS5: %v", err)
	}
	server := &fakeSocks5{listener: listener, requireAuth: requireAuth}
	go server.serve()
	t.Cleanup(server.close)
	return server
}

func (s *fakeSocks5) addr() string { return s.listener.Addr().String() }

func (s *fakeSocks5) close() {
	s.mu.Lock()
	s.closed = true
	controls, relays := s.controls, s.relays
	s.controls, s.relays = nil, nil
	s.mu.Unlock()
	s.listener.Close()
	for _, control := range controls {
		control.Close()
	}
	for _, relay := range relays {
		relay.Close()
	}
}

func (s *fakeSocks5) serve() {
	for {
		control, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.controls = append(s.controls, control)
		s.mu.Unlock()
		go s.handle(control)
	}
}

func (s *fakeSocks5) handle(control net.Conn) {
	defer control.Close()
	header := make([]byte, 2)
	if _, err := io.ReadFull(control, header); err != nil || header[0] != socks5Version {
		return
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(control, methods); err != nil {
		return
	}
	method := byte(socks5MethodNone)
	if s.requireAuth {
		method = socks5MethodNoAccept
		for _, offered := range methods {
			if offered == socks5MethodUserPass {
				method = socks5MethodUserPass
				break
			}
		}
	}
	if _, err := control.Write([]byte{socks5Version, method}); err != nil || method == socks5MethodNoAccept {
		return
	}
	if method == socks5MethodUserPass {
		version := make([]byte, 2)
		if _, err := io.ReadFull(control, version); err != nil {
			return
		}
		user := make([]byte, int(version[1]))
		if _, err := io.ReadFull(control, user); err != nil {
			return
		}
		length := make([]byte, 1)
		if _, err := io.ReadFull(control, length); err != nil {
			return
		}
		password := make([]byte, int(length[0]))
		if _, err := io.ReadFull(control, password); err != nil {
			return
		}
		status := byte(1)
		if string(user) == "alice" && string(password) == "secret" {
			status = 0
		}
		if _, err := control.Write([]byte{socks5AuthVersion, status}); err != nil || status != 0 {
			return
		}
	}
	request := make([]byte, 4)
	if _, err := io.ReadFull(control, request); err != nil {
		return
	}
	if err := discardSocksAddress(control, request[3]); err != nil {
		return
	}
	port := make([]byte, 2)
	if _, err := io.ReadFull(control, port); err != nil {
		return
	}
	relay, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		relay.Close()
		return
	}
	s.relays = append(s.relays, relay)
	s.mu.Unlock()
	local := relay.LocalAddr().(*net.UDPAddr)
	reply := []byte{socks5Version, socks5ReplySuccess, 0x00, socks5AtypIPv4}
	reply = append(reply, local.IP.To4()...)
	reply = binary.BigEndian.AppendUint16(reply, uint16(local.Port))
	if _, err := control.Write(reply); err != nil {
		return
	}
	go func() {
		io.Copy(io.Discard, control)
		relay.Close()
	}()
	s.echo(relay)
}

func discardSocksAddress(reader io.Reader, atyp byte) error {
	size := 0
	switch atyp {
	case socks5AtypIPv4:
		size = 4
	case socks5AtypIPv6:
		size = 16
	case socks5AtypDomain:
		length := make([]byte, 1)
		if _, err := io.ReadFull(reader, length); err != nil {
			return err
		}
		size = int(length[0])
	default:
		return errors.New("unsupported address type")
	}
	address := make([]byte, size)
	_, err := io.ReadFull(reader, address)
	return err
}

func (s *fakeSocks5) echo(relay *net.UDPConn) {
	buffer := make([]byte, 64*1024)
	for {
		size, from, err := relay.ReadFromUDPAddrPort(buffer)
		if err != nil {
			return
		}
		destination, payload, err := decodeSocksUDP(buffer[:size])
		if err != nil {
			continue
		}
		s.datagrams.Add(1)
		if _, err := relay.WriteToUDPAddrPort(encodeSocksUDP(destination, payload), from); err != nil {
			return
		}
	}
}
