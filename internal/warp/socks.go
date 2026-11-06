package warp

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.zx2c4.com/wireguard/conn"
)

// SOCKS5 protocol constants (RFC 1928 and RFC 1929).
const (
	socks5Version         = 0x05
	socks5AuthVersion     = 0x01
	socks5MethodNone      = 0x00
	socks5MethodUserPass  = 0x02
	socks5MethodNoAccept  = 0xff
	socks5CmdUDPAssociate = 0x03
	socks5AtypIPv4        = 0x01
	socks5AtypDomain      = 0x03
	socks5AtypIPv6        = 0x04
	socks5ReplySuccess    = 0x00
)

// defaultRelayFailureThreshold is how many consecutive relay failures open the
// shared circuit. Every WARP exit is created by a registration followed by a
// handshake probe; without a shared brake a dead proxy would retire all of them
// one after another and spend a device registration each time.
const defaultRelayFailureThreshold = 3

// defaultRelayCooldown is how long the open circuit fails fast before one
// half-open probe is allowed through.
const defaultRelayCooldown = 60 * time.Second

// socks5Proxy is a parsed SOCKS5 UDP relay endpoint.
type socks5Proxy struct {
	host string
	port uint16
	user string
	pass string
}

func (p socks5Proxy) address() string {
	return net.JoinHostPort(p.host, strconv.Itoa(int(p.port)))
}

// NormalizeSocksUDP validates a SOCKS5 UDP relay endpoint and returns its
// canonical form. It accepts socks5://[user:pass@]host:port, socks5h://… and a
// bare host:port; the canonical form always carries the scheme so the stored
// configuration is unambiguous. Port 1080 is the default.
func NormalizeSocksUDP(raw string) (string, error) {
	proxy, err := parseSocks5Proxy(raw)
	if err != nil {
		return "", err
	}
	normalized := &url.URL{Scheme: "socks5", Host: proxy.address()}
	if proxy.user != "" || proxy.pass != "" {
		normalized.User = url.UserPassword(proxy.user, proxy.pass)
	}
	return normalized.String(), nil
}

func parseSocks5Proxy(raw string) (socks5Proxy, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return socks5Proxy{}, errors.New("empty SOCKS5 address")
	}
	if !strings.Contains(value, "://") {
		value = "socks5://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return socks5Proxy{}, fmt.Errorf("invalid SOCKS5 address %q: %w", raw, err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "socks5", "socks5h":
	default:
		return socks5Proxy{}, fmt.Errorf("SOCKS5 address must use socks5:// or socks5h://, got %q", parsed.Scheme)
	}
	host := parsed.Hostname()
	if host == "" {
		return socks5Proxy{}, fmt.Errorf("SOCKS5 address %q has no host", raw)
	}
	portValue := parsed.Port()
	if portValue == "" {
		portValue = "1080"
	}
	port, err := strconv.ParseUint(portValue, 10, 16)
	if err != nil || port == 0 {
		return socks5Proxy{}, fmt.Errorf("SOCKS5 address %q has an invalid port", raw)
	}
	var user, pass string
	if parsed.User != nil {
		user = parsed.User.Username()
		pass, _ = parsed.User.Password()
	}
	return socks5Proxy{host: host, port: uint16(port), user: user, pass: pass}, nil
}

// dial opens the TCP control connection and performs UDP ASSOCIATE. The
// returned relay address is where encapsulated datagrams are sent (RFC 1928
// section 7). The control connection must stay open for the association to
// remain valid.
func (p socks5Proxy) dial(ctx context.Context, tun bool, log logger) (net.Conn, netip.AddrPort, error) {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   socksRelayControl(p.host, "tcp", tun, log),
	}
	control, err := dialer.DialContext(ctx, "tcp", p.address())
	if err != nil {
		return nil, netip.AddrPort{}, fmt.Errorf("dial SOCKS5 proxy %s: %w", p.address(), err)
	}
	deadline := time.Now().Add(10 * time.Second)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := control.SetDeadline(deadline); err != nil {
		control.Close()
		return nil, netip.AddrPort{}, err
	}
	if err := p.negotiate(control); err != nil {
		control.Close()
		return nil, netip.AddrPort{}, fmt.Errorf("SOCKS5 handshake: %w", err)
	}
	bound, err := p.associate(control)
	if err != nil {
		control.Close()
		return nil, netip.AddrPort{}, err
	}
	if err := control.SetDeadline(time.Time{}); err != nil {
		control.Close()
		return nil, netip.AddrPort{}, err
	}
	relay, err := p.resolveRelay(bound)
	if err != nil {
		control.Close()
		return nil, netip.AddrPort{}, err
	}
	return control, relay, nil
}

func (p socks5Proxy) negotiate(connection net.Conn) error {
	methods := []byte{socks5MethodNone}
	if p.user != "" || p.pass != "" {
		// Offer both: the credentials are used when the server asks for them,
		// and a server that accepts unauthenticated clients still works.
		methods = []byte{socks5MethodUserPass, socks5MethodNone}
	}
	request := append([]byte{socks5Version, byte(len(methods))}, methods...)
	if _, err := connection.Write(request); err != nil {
		return err
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(connection, reply); err != nil {
		return err
	}
	if reply[0] != socks5Version {
		return fmt.Errorf("unexpected SOCKS version %d", reply[0])
	}
	switch reply[1] {
	case socks5MethodNone:
		return nil
	case socks5MethodUserPass:
		if p.user == "" && p.pass == "" {
			return errors.New("proxy requires username/password but none is configured")
		}
		return p.authenticate(connection)
	case socks5MethodNoAccept:
		return errors.New("proxy rejected every offered authentication method")
	default:
		return fmt.Errorf("proxy selected unsupported authentication method 0x%02x", reply[1])
	}
}

func (p socks5Proxy) authenticate(connection net.Conn) error {
	if len(p.user) > 255 || len(p.pass) > 255 {
		return errors.New("username and password must be at most 255 bytes")
	}
	request := make([]byte, 0, 3+len(p.user)+len(p.pass))
	request = append(request, socks5AuthVersion, byte(len(p.user)))
	request = append(request, p.user...)
	request = append(request, byte(len(p.pass)))
	request = append(request, p.pass...)
	if _, err := connection.Write(request); err != nil {
		return err
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(connection, reply); err != nil {
		return err
	}
	if reply[0] != socks5AuthVersion {
		return fmt.Errorf("unexpected authentication version %d", reply[0])
	}
	if reply[1] != 0 {
		return fmt.Errorf("authentication rejected (status 0x%02x)", reply[1])
	}
	return nil
}

func (p socks5Proxy) associate(connection net.Conn) (netip.AddrPort, error) {
	// The client address is informational; every implementation accepts the
	// wildcard and derives the association from the TCP control connection.
	request := []byte{
		socks5Version, socks5CmdUDPAssociate, 0x00,
		socks5AtypIPv4, 0, 0, 0, 0, 0, 0,
	}
	if _, err := connection.Write(request); err != nil {
		return netip.AddrPort{}, err
	}
	bound, err := readSocks5Reply(connection)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("SOCKS5 UDP ASSOCIATE: %w", err)
	}
	return bound, nil
}

func readSocks5Reply(connection net.Conn) (netip.AddrPort, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(connection, header); err != nil {
		return netip.AddrPort{}, err
	}
	if header[0] != socks5Version {
		return netip.AddrPort{}, fmt.Errorf("unexpected SOCKS version %d", header[0])
	}
	if header[1] != socks5ReplySuccess {
		return netip.AddrPort{}, fmt.Errorf("reply 0x%02x (%s)", header[1], socks5ReplyText(header[1]))
	}
	var address netip.Addr
	switch header[3] {
	case socks5AtypIPv4:
		raw := make([]byte, 4)
		if _, err := io.ReadFull(connection, raw); err != nil {
			return netip.AddrPort{}, err
		}
		address = netip.AddrFrom4([4]byte(raw))
	case socks5AtypIPv6:
		raw := make([]byte, 16)
		if _, err := io.ReadFull(connection, raw); err != nil {
			return netip.AddrPort{}, err
		}
		address = netip.AddrFrom16([16]byte(raw))
	case socks5AtypDomain:
		length := make([]byte, 1)
		if _, err := io.ReadFull(connection, length); err != nil {
			return netip.AddrPort{}, err
		}
		name := make([]byte, int(length[0]))
		if _, err := io.ReadFull(connection, name); err != nil {
			return netip.AddrPort{}, err
		}
		addresses, err := net.DefaultResolver.LookupNetIP(context.Background(), "ip", string(name))
		if err != nil || len(addresses) == 0 {
			return netip.AddrPort{}, fmt.Errorf("resolve relay host %q: %w", string(name), err)
		}
		address = addresses[0].Unmap()
	default:
		return netip.AddrPort{}, fmt.Errorf("unsupported address type 0x%02x", header[3])
	}
	rawPort := make([]byte, 2)
	if _, err := io.ReadFull(connection, rawPort); err != nil {
		return netip.AddrPort{}, err
	}
	return netip.AddrPortFrom(address, binary.BigEndian.Uint16(rawPort)), nil
}

func socks5ReplyText(code byte) string {
	switch code {
	case 1:
		return "general SOCKS server failure"
	case 2:
		return "connection not allowed by ruleset"
	case 3:
		return "network unreachable"
	case 4:
		return "host unreachable"
	case 5:
		return "connection refused"
	case 6:
		return "TTL expired"
	case 7:
		return "command not supported"
	case 8:
		return "address type not supported"
	default:
		return "unknown reply"
	}
}

// resolveRelay turns the BND.ADDR from UDP ASSOCIATE into an address this host
// can send to. A wildcard answer is common; it means the proxy host itself on
// the returned port.
func (p socks5Proxy) resolveRelay(bound netip.AddrPort) (netip.AddrPort, error) {
	if bound.Addr().IsValid() && !bound.Addr().IsUnspecified() {
		return netip.AddrPortFrom(bound.Addr().Unmap(), bound.Port()), nil
	}
	if address, err := netip.ParseAddr(p.host); err == nil {
		return netip.AddrPortFrom(address.Unmap(), bound.Port()), nil
	}
	addresses, err := net.DefaultResolver.LookupNetIP(context.Background(), "ip", p.host)
	if err != nil || len(addresses) == 0 {
		return netip.AddrPort{}, fmt.Errorf("resolve SOCKS5 proxy host %q: %w", p.host, err)
	}
	return netip.AddrPortFrom(addresses[0].Unmap(), bound.Port()), nil
}

// preflight validates the proxy and its UDP ASSOCIATE without keeping the
// association. It runs before a device registration so a dead relay costs a
// handshake rather than a real WARP device.
func (p socks5Proxy) preflight(ctx context.Context, tun bool, log logger) error {
	control, _, err := p.dial(ctx, tun, log)
	if err != nil {
		return err
	}
	return control.Close()
}

// socksRelayControl pins the relay traffic to the physical adapter for the same
// reason the direct WireGuard socket is pinned, but never for a loopback proxy:
// a local proxy needs no route, and the interface option can make Windows
// reject a loopback send outright.
func socksRelayControl(host, network string, tun bool, log logger) func(string, string, syscall.RawConn) error {
	if tun || isLoopbackHost(host) {
		return nil
	}
	return interfaceControl(network, log)
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address, err := netip.ParseAddr(host)
	return err == nil && address.IsLoopback()
}

// encodeSocksUDP wraps one WireGuard datagram in the SOCKS5 UDP request header
// (RFC 1928 section 7): RSV(2) FRAG(1) ATYP ADDR PORT DATA.
func encodeSocksUDP(destination netip.AddrPort, payload []byte) []byte {
	address := destination.Addr().Unmap()
	size := 4 + 2 + len(payload)
	if address.Is4() {
		size = 4 + 4 + 2 + len(payload)
	} else {
		size += 16
	}
	packet := make([]byte, 0, size)
	packet = append(packet, 0x00, 0x00, 0x00)
	if address.Is4() {
		packet = append(packet, socks5AtypIPv4)
		raw := address.As4()
		packet = append(packet, raw[:]...)
	} else {
		packet = append(packet, socks5AtypIPv6)
		raw := address.As16()
		packet = append(packet, raw[:]...)
	}
	packet = binary.BigEndian.AppendUint16(packet, destination.Port())
	packet = append(packet, payload...)
	return packet
}

// decodeSocksUDP strips the SOCKS5 UDP header and returns the source address
// and the inner datagram. Fragmented datagrams are rejected: WireGuard never
// needs them and supporting reassembly would only add state to the relay.
func decodeSocksUDP(packet []byte) (netip.AddrPort, []byte, error) {
	if len(packet) < 4 {
		return netip.AddrPort{}, nil, errors.New("short SOCKS5 UDP datagram")
	}
	if packet[0] != 0x00 || packet[1] != 0x00 {
		return netip.AddrPort{}, nil, errors.New("invalid SOCKS5 UDP reserved bytes")
	}
	if packet[2] != 0x00 {
		return netip.AddrPort{}, nil, errors.New("fragmented SOCKS5 UDP datagrams are not supported")
	}
	offset := 4
	var address netip.Addr
	switch packet[3] {
	case socks5AtypIPv4:
		if len(packet) < offset+4+2 {
			return netip.AddrPort{}, nil, errors.New("short SOCKS5 IPv4 datagram")
		}
		address = netip.AddrFrom4([4]byte(packet[offset : offset+4]))
		offset += 4
	case socks5AtypIPv6:
		if len(packet) < offset+16+2 {
			return netip.AddrPort{}, nil, errors.New("short SOCKS5 IPv6 datagram")
		}
		address = netip.AddrFrom16([16]byte(packet[offset : offset+16]))
		offset += 16
	default:
		return netip.AddrPort{}, nil, fmt.Errorf("unsupported SOCKS5 UDP address type 0x%02x", packet[3])
	}
	if len(packet) < offset+2 {
		return netip.AddrPort{}, nil, errors.New("short SOCKS5 UDP port")
	}
	port := binary.BigEndian.Uint16(packet[offset : offset+2])
	offset += 2
	return netip.AddrPortFrom(address.Unmap(), port), packet[offset:], nil
}

// socksUDPBind is a WireGuard conn.Bind that carries the outer datagrams over
// one SOCKS5 UDP association. The UDP socket in this process only talks to the
// proxy's relay endpoint; every datagram is wrapped with its real Cloudflare
// destination, so endpoint switching inside WireGuard keeps working.
type socksUDPBind struct {
	proxy socks5Proxy
	guard *relayGuard
	log   logger
	tun   bool

	assocMu sync.Mutex
	mu      sync.Mutex
	control net.Conn
	udp     *net.UDPConn
	relay   netip.AddrPort
	dead    bool
	closed  bool
}

func newSocksUDPBind(proxy socks5Proxy, guard *relayGuard, log logger, tun bool) *socksUDPBind {
	return &socksUDPBind{proxy: proxy, guard: guard, log: log, tun: tun}
}

func (b *socksUDPBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	b.mu.Lock()
	if b.control != nil || b.udp != nil {
		b.mu.Unlock()
		return nil, 0, conn.ErrBindAlreadyOpen
	}
	b.closed = false
	b.mu.Unlock()

	control, relay, err := b.proxy.dial(context.Background(), b.tun, b.log)
	if err != nil {
		if b.guard != nil {
			b.guard.failure(err)
		}
		return nil, 0, fmt.Errorf("open SOCKS5 UDP relay: %w", err)
	}
	network := "udp4"
	if relay.Addr().Is6() {
		network = "udp6"
	}
	// The requested port is WireGuard's remembered port from a previous bind;
	// the relay does not need it, so bind an ephemeral port and report it back.
	udp, err := listenUDP(network, &net.UDPAddr{}, socksRelayControl(b.proxy.host, network, b.tun, b.log))
	if err != nil {
		control.Close()
		if b.guard != nil {
			b.guard.failure(err)
		}
		return nil, 0, fmt.Errorf("open SOCKS5 relay socket: %w", err)
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		control.Close()
		udp.Close()
		return nil, 0, net.ErrClosed
	}
	b.control, b.udp, b.relay, b.dead = control, udp, relay, false
	b.mu.Unlock()
	go b.watch(control)
	return []conn.ReceiveFunc{b.receive}, uint16(udp.LocalAddr().(*net.UDPAddr).Port), nil
}

// watch marks the association dead when the proxy closes the TCP control
// connection. The next Send re-establishes it; there is no background reconnect
// so an idle exit does not keep a dead proxy busy.
func (b *socksUDPBind) watch(control net.Conn) {
	buffer := make([]byte, 1)
	for {
		if _, err := control.Read(buffer); err != nil {
			b.mu.Lock()
			if b.control == control {
				b.dead = true
			}
			b.mu.Unlock()
			return
		}
	}
}

func (b *socksUDPBind) receive(packets [][]byte, sizes []int, endpoints []conn.Endpoint) (int, error) {
	for {
		b.mu.Lock()
		udp := b.udp
		closed := b.closed
		relay := b.relay
		b.mu.Unlock()
		if closed || udp == nil {
			return 0, net.ErrClosed
		}
		size, from, err := udp.ReadFromUDPAddrPort(packets[0])
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return 0, net.ErrClosed
			}
			return 0, err
		}
		if relay.IsValid() {
			sameAddress := from.Addr().Unmap() == relay.Addr().Unmap()
			if !sameAddress || from.Port() != relay.Port() {
				continue
			}
		}
		address, payload, err := decodeSocksUDP(packets[0][:size])
		if err != nil || len(payload) == 0 {
			continue
		}
		copy(packets[0], payload)
		sizes[0] = len(payload)
		endpoints[0] = &simpleEndpoint{addr: address}
		if b.guard != nil {
			b.guard.success()
		}
		return 1, nil
	}
}

func (b *socksUDPBind) Send(bufs [][]byte, endpoint conn.Endpoint) error {
	ep, ok := endpoint.(*simpleEndpoint)
	if !ok {
		return conn.ErrWrongEndpointType
	}
	for _, buf := range bufs {
		packet := encodeSocksUDP(ep.addr, buf)
		if err := b.sendPacket(packet); err != nil {
			return err
		}
	}
	return nil
}

func (b *socksUDPBind) sendPacket(packet []byte) error {
	for attempt := 0; attempt < 2; attempt++ {
		b.mu.Lock()
		udp, relay, dead, closed := b.udp, b.relay, b.dead, b.closed
		b.mu.Unlock()
		if closed || udp == nil {
			return net.ErrClosed
		}
		if dead || !relay.IsValid() {
			if err := b.reconnect(); err != nil {
				return err
			}
			continue
		}
		if _, err := udp.WriteToUDPAddrPort(packet, relay); err != nil {
			if b.guard != nil {
				b.guard.failure(err)
			}
			b.mu.Lock()
			b.dead = true
			b.mu.Unlock()
			if attempt == 0 {
				continue
			}
			return err
		}
		return nil
	}
	return errors.New("SOCKS5 UDP relay send failed")
}

func (b *socksUDPBind) reconnect() error {
	b.assocMu.Lock()
	defer b.assocMu.Unlock()
	b.mu.Lock()
	closed, alive := b.closed, b.control != nil && !b.dead
	b.mu.Unlock()
	if closed {
		return net.ErrClosed
	}
	if alive {
		return nil
	}
	control, relay, err := b.proxy.dial(context.Background(), b.tun, b.log)
	if err != nil {
		if b.guard != nil {
			b.guard.failure(err)
		}
		return fmt.Errorf("reconnect SOCKS5 UDP relay: %w", err)
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		control.Close()
		return net.ErrClosed
	}
	old := b.control
	b.control, b.relay, b.dead = control, relay, false
	b.mu.Unlock()
	if old != nil {
		old.Close()
	}
	go b.watch(control)
	return nil
}

func (b *socksUDPBind) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	control, udp := b.control, b.udp
	b.control, b.udp, b.relay = nil, nil, netip.AddrPort{}
	b.mu.Unlock()
	var errs []error
	if control != nil {
		errs = append(errs, control.Close())
	}
	if udp != nil {
		errs = append(errs, udp.Close())
	}
	return errors.Join(errs...)
}

func (*socksUDPBind) SetMark(uint32) error { return nil }

func (*socksUDPBind) ParseEndpoint(value string) (conn.Endpoint, error) {
	address, err := netip.ParseAddrPort(value)
	if err != nil {
		return nil, err
	}
	return &simpleEndpoint{addr: address}, nil
}

func (*socksUDPBind) BatchSize() int { return 1 }

// relayGuard is the circuit breaker shared by every WARP exit of one pool. A
// proxy outage then trips one brake instead of making each exit pay for its own
// registration and cooldown. Open means fast failure with no registration;
// after the cooldown one half-open probe is allowed to decide the state.
type relayGuard struct {
	threshold int
	cooldown  time.Duration
	log       logger

	mu        sync.Mutex
	failures  int
	openUntil time.Time
	probing   bool
}

// relayUnavailableError reports that the shared circuit is open. The pool uses
// its delay to pace the next registration instead of retrying into a dead
// proxy.
type relayUnavailableError struct{ retryAfter time.Duration }

func (e *relayUnavailableError) Error() string {
	return fmt.Sprintf("SOCKS5 UDP relay unavailable for %s", e.retryAfter.Round(time.Second))
}

func relayUnavailablePause(err error) time.Duration {
	var unavailable *relayUnavailableError
	if errors.As(err, &unavailable) {
		return unavailable.retryAfter
	}
	return 0
}

func newRelayGuard(log logger) *relayGuard {
	return &relayGuard{
		threshold: defaultRelayFailureThreshold,
		cooldown:  defaultRelayCooldown,
		log:       log,
	}
}

// acquire admits a new exit registration. While open it fails immediately; in
// the half-open window exactly one caller gets through.
func (g *relayGuard) acquire() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	if !g.openUntil.IsZero() && now.Before(g.openUntil) {
		return &relayUnavailableError{retryAfter: g.openUntil.Sub(now)}
	}
	if !g.openUntil.IsZero() {
		if g.probing {
			return &relayUnavailableError{retryAfter: time.Second}
		}
		g.probing = true
	}
	return nil
}

// abandon releases a half-open probe that ended without a verdict, such as a
// Cloudflare registration pause. The relay itself was not tested.
func (g *relayGuard) abandon() {
	g.mu.Lock()
	g.probing = false
	g.mu.Unlock()
}

func (g *relayGuard) success() {
	g.mu.Lock()
	recovered := !g.openUntil.IsZero()
	g.failures = 0
	g.openUntil = time.Time{}
	g.probing = false
	g.mu.Unlock()
	if recovered && g.log != nil {
		g.log.Infof("SOCKS5 UDP relay recovered")
	}
}

func (g *relayGuard) failure(err error) {
	g.mu.Lock()
	g.probing = false
	g.failures++
	tripped := false
	if g.failures >= g.threshold {
		now := time.Now()
		if g.openUntil.IsZero() || !now.Before(g.openUntil) {
			tripped = true
		}
		g.openUntil = now.Add(g.cooldown)
	}
	failures, cooldown := g.failures, g.cooldown
	g.mu.Unlock()
	if tripped && g.log != nil {
		g.log.Warnf("SOCKS5 UDP relay circuit opened for %s after %d consecutive failures: %v", cooldown.Round(time.Second), failures, err)
	}
}

// open reports whether the circuit is currently failing fast.
func (g *relayGuard) open() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return !g.openUntil.IsZero() && time.Now().Before(g.openUntil)
}
