package warp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"syscall"

	"golang.zx2c4.com/wireguard/conn"
)

type simpleBind struct {
	mu     sync.Mutex
	ipv4   *net.UDPConn
	ipv6   *net.UDPConn
	source netip.Addr
	log    logger
	closed bool
}

type simpleEndpoint struct {
	addr netip.AddrPort
}

func (b *simpleBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ipv4 != nil || b.ipv6 != nil {
		return nil, 0, conn.ErrBindAlreadyOpen
	}
	b.closed = false
	if b.source.IsValid() {
		v6, err := b.listenIPv6(port)
		if err != nil {
			return nil, 0, err
		}
		port = uint16(v6.LocalAddr().(*net.UDPAddr).Port)
		// Keep an IPv4 socket as well: if no IPv6 endpoint answers, the
		// handshake falls back to IPv4 and the tunnel still comes up.
		v4, _ := listenUDP("udp4", &net.UDPAddr{Port: int(port)}, interfaceControl("udp4", b.log))
		var receives []conn.ReceiveFunc
		if v4 != nil {
			receives = append(receives, receiveUDP(v4))
		}
		receives = append(receives, receiveUDP(v6))
		b.ipv4, b.ipv6 = v4, v6
		return receives, port, nil
	}
	var v4, v6 *net.UDPConn
	var err error
	if port == 0 {
		v4, err = listenUDP("udp4", &net.UDPAddr{}, interfaceControl("udp4", b.log))
		if err == nil {
			port = uint16(v4.LocalAddr().(*net.UDPAddr).Port)
		}
	} else {
		v4, err = listenUDP("udp4", &net.UDPAddr{Port: int(port)}, interfaceControl("udp4", b.log))
	}
	if err == nil {
		v6, _ = listenUDP("udp6", &net.UDPAddr{Port: int(port)}, interfaceControl("udp6", b.log))
	}
	if v4 == nil && v6 == nil {
		v6, err = listenUDP("udp6", &net.UDPAddr{Port: int(port)}, interfaceControl("udp6", b.log))
		if err != nil {
			return nil, 0, err
		}
		port = uint16(v6.LocalAddr().(*net.UDPAddr).Port)
	}
	var receives []conn.ReceiveFunc
	if v4 != nil {
		receives = append(receives, receiveUDP(v4))
	}
	if v6 != nil {
		receives = append(receives, receiveUDP(v6))
	}
	b.ipv4, b.ipv6 = v4, v6
	return receives, port, nil
}

func receiveUDP(udp *net.UDPConn) conn.ReceiveFunc {
	return func(packets [][]byte, sizes []int, endpoints []conn.Endpoint) (int, error) {
		n, addr, err := udp.ReadFromUDPAddrPort(packets[0])
		if err != nil {
			return 0, err
		}
		sizes[0] = n
		endpoints[0] = &simpleEndpoint{addr: addr}
		return 1, nil
	}
}

// listenUDP opens a UDP socket with control applied before it is bound. The
// control is what pins the socket to the host's physical adapter, so a TUN
// device that owns the default route cannot swallow the WireGuard traffic.
func listenUDP(network string, address *net.UDPAddr, control func(string, string, syscall.RawConn) error) (*net.UDPConn, error) {
	config := net.ListenConfig{Control: control}
	packet, err := config.ListenPacket(context.Background(), network, address.String())
	if err != nil {
		return nil, err
	}
	conn, ok := packet.(*net.UDPConn)
	if !ok {
		packet.Close()
		return nil, fmt.Errorf("listen %s: unexpected connection %T", network, packet)
	}
	return conn, nil
}

// listenIPv6 opens the WireGuard socket bound to the exit's outer source
// address. Every packet — handshake and data — leaves from that address, so a
// replacement exit presents Cloudflare with a new source and receives a new
// public egress. The configured source already selects the adapter, so this
// socket deliberately skips interfaceControl: pinning it to a different
// interface would contradict the address it is bound to.
func (b *simpleBind) listenIPv6(port uint16) (*net.UDPConn, error) {
	address := net.JoinHostPort(b.source.String(), strconv.Itoa(int(port)))
	config := net.ListenConfig{Control: freebindControl}
	packet, err := config.ListenPacket(context.Background(), "udp6", address)
	if err != nil {
		return nil, fmt.Errorf("bind WARP outer source %s: %w", b.source, err)
	}
	conn, ok := packet.(*net.UDPConn)
	if !ok {
		packet.Close()
		return nil, fmt.Errorf("bind WARP outer source %s: unexpected connection %T", b.source, packet)
	}
	return conn, nil
}

func (b *simpleBind) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	var errs []error
	if b.ipv4 != nil {
		errs = append(errs, b.ipv4.Close())
		b.ipv4 = nil
	}
	if b.ipv6 != nil {
		errs = append(errs, b.ipv6.Close())
		b.ipv6 = nil
	}
	return errors.Join(errs...)
}

func (b *simpleBind) SetMark(uint32) error { return nil }

func (b *simpleBind) Send(bufs [][]byte, endpoint conn.Endpoint) error {
	ep, ok := endpoint.(*simpleEndpoint)
	if !ok {
		return conn.ErrWrongEndpointType
	}
	b.mu.Lock()
	var udp *net.UDPConn
	if ep.addr.Addr().Is4() {
		udp = b.ipv4
	} else {
		udp = b.ipv6
	}
	closed := b.closed
	source := b.source
	b.mu.Unlock()
	if closed || udp == nil {
		if !closed && source.IsValid() && ep.addr.Addr().Is4() {
			return fmt.Errorf("WARP exit bound to outer source %s cannot reach IPv4 endpoint %s", source, ep.addr)
		}
		return net.ErrClosed
	}
	for _, buf := range bufs {
		if _, err := udp.WriteToUDPAddrPort(buf, ep.addr); err != nil {
			return err
		}
	}
	return nil
}

func (*simpleBind) ParseEndpoint(value string) (conn.Endpoint, error) {
	addr, err := netip.ParseAddrPort(value)
	if err != nil {
		return nil, err
	}
	return &simpleEndpoint{addr: addr}, nil
}

func (*simpleBind) BatchSize() int { return 1 }

func (e *simpleEndpoint) ClearSrc()           {}
func (e *simpleEndpoint) SrcToString() string { return "" }
func (e *simpleEndpoint) DstToString() string { return e.addr.String() }
func (e *simpleEndpoint) DstIP() netip.Addr   { return e.addr.Addr() }
func (e *simpleEndpoint) SrcIP() netip.Addr   { return netip.Addr{} }
func (e *simpleEndpoint) DstToBytes() []byte {
	value, _ := e.addr.MarshalBinary()
	return value
}
