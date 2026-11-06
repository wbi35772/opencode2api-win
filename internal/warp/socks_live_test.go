package warp

import (
	"context"
	"encoding/binary"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"
)

// TestLiveSocksRelaySTUN exercises parseSocks5Proxy + SOCKS5 UDP ASSOCIATE
// against a real local proxy. It is skipped unless WARP_SOCKS_LIVE names one.
func TestLiveSocksRelaySTUN(t *testing.T) {
	raw := os.Getenv("WARP_SOCKS_LIVE")
	if raw == "" {
		t.Skip("set WARP_SOCKS_LIVE=socks5://127.0.0.1:1080 to run")
	}
	proxy, err := parseSocks5Proxy(raw)
	if err != nil {
		t.Fatal(err)
	}
	control, relay, err := proxy.dial(context.Background(), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()

	target := "stun.cloudflare.com:3478"
	if value := os.Getenv("WARP_SOCKS_LIVE_DST"); value != "" {
		target = value
	}
	host, portValue, err := net.SplitHostPort(target)
	if err != nil {
		t.Fatal(err)
	}
	addresses, err := net.DefaultResolver.LookupNetIP(context.Background(), "ip4", host)
	if err != nil || len(addresses) == 0 {
		t.Fatalf("resolve %s: %v", host, err)
	}
	port, err := net.LookupPort("udp", portValue)
	if err != nil {
		t.Fatal(err)
	}
	destination := netip.AddrPortFrom(addresses[0].Unmap(), uint16(port))
	transaction := [12]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	request := []byte{0x00, 0x01, 0x00, 0x00, 0x21, 0x12, 0xA4, 0x42}
	request = append(request, transaction[:]...)
	packet := encodeSocksUDP(destination, request)

	network := "udp4"
	if relay.Addr().Is6() {
		network = "udp6"
	}
	udp, err := net.ListenUDP(network, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	if _, err := udp.WriteToUDPAddrPort(packet, relay); err != nil {
		t.Fatal(err)
	}
	udp.SetReadDeadline(time.Now().Add(5 * time.Second))
	buffer := make([]byte, 2048)
	size, from, err := udp.ReadFromUDPAddrPort(buffer)
	if err != nil {
		t.Fatalf("no UDP reply via %s from %s: %v", raw, relay, err)
	}
	source, payload, err := decodeSocksUDP(buffer[:size])
	if err != nil {
		t.Fatalf("bad relay reply from %s: %v", from, err)
	}
	if len(payload) < 20 || payload[0] != 0x01 || payload[1] != 0x01 {
		t.Fatalf("unexpected reply from %s (source=%s): %x", from, source, payload)
	}
	if binary.BigEndian.Uint32(payload[4:8]) != 0x2112A442 {
		t.Fatalf("STUN cookie missing: %x", payload)
	}
	t.Logf("STUN reply from %s via %s", source, relay)
}
