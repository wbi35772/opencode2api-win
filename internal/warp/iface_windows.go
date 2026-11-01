//go:build windows

package warp

import (
	"encoding/binary"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Windows takes the outgoing interface for a unicast packet as an interface
// index socket option. Both address families use option 31 and the level picks
// the family; IP_UNICAST_IF wants the index in network byte order,
// IPV6_UNICAST_IF wants it in host byte order.
const (
	optUnicastIf     = 31
	optUnicastIfIPv6 = 31
)

const (
	// pinTTL bounds how long a chosen interface is reused before the host is
	// re-examined, so moving between Ethernet, Wi-Fi and USB tethering is
	// picked up without a restart.
	pinTTL = 5 * time.Minute
	// interfaceProbeTimeout bounds one candidate's reachability probe.
	interfaceProbeTimeout = 1500 * time.Millisecond
	// interfaceProbeAttempts covers one lost DNS query: the probe only decides
	// whether an adapter is usable, so failing it on a single drop would pin
	// nothing and silently give the TUN the traffic again.
	interfaceProbeAttempts = 2
)

// tunnelLike names the adapters a WARP socket must not be pinned to. WARP has
// to keep working while a local proxy or a WSL/virtual machine network owns the
// default route, which is the situation this pin exists for.
var tunnelLike = []string{
	"tun", "tap", "wintun", "wireguard", "vpn", "singbox", "sing-box",
	"clash", "v2ray", "xray", "tailscale", "zerotier", "openvpn",
	"vmware", "virtualbox", "hyper-v", "vethernet", "loopback",
	"npcap", "bluetooth", "teredo", "isatap", "wsl",
}

// pinnedInterface caches the interface index the WARP sockets are pinned to.
var pinnedInterface struct {
	mu      sync.Mutex
	index   uint32
	name    string
	expires time.Time
}

// interfaceControl returns a Control hook that pins the socket to the host's
// outbound physical interface, so the WireGuard traffic bypasses a TUN adapter
// that owns the default route. It returns nil when no usable interface is
// found: the socket then routes normally, which is the behaviour without this
// pin and is always preferable to failing the handshake.
func interfaceControl(network string, log logger) func(string, string, syscall.RawConn) error {
	if pinDisabled() {
		return nil
	}
	index, name, fresh, ok := outboundInterface()
	if !ok {
		return nil
	}
	if fresh && log != nil {
		log.Infof("OpenCode WARP outer UDP pinned to %s (ifIndex %d), bypassing any TUN default route", name, index)
	}
	return pinInterfaceControl(network, index)
}

// outerInterfacePinned reports whether the WARP sockets will be pinned to a
// physical adapter. Callers use it to prefer the outer address family the
// adapter is likely to reach.
func outerInterfacePinned() bool {
	_, _, _, ok := outboundInterface()
	return ok
}

// pinInterfaceControl is the Control hook for one fixed interface index. A
// rejected option is ignored so the socket falls back to the system route
// instead of losing the tunnel.
func pinInterfaceControl(network string, index uint32) func(string, string, syscall.RawConn) error {
	level, option, value := syscall.IPPROTO_IP, optUnicastIf, networkOrderIndex(index)
	if network == "udp6" {
		level, option, value = syscall.IPPROTO_IPV6, optUnicastIfIPv6, int(index)
	}
	return func(_, _ string, raw syscall.RawConn) error {
		_ = raw.Control(func(fd uintptr) {
			_ = syscall.SetsockoptInt(syscall.Handle(fd), level, option, value)
		})
		return nil
	}
}

// networkOrderIndex converts an interface index to the byte order
// IP_UNICAST_IF expects.
func networkOrderIndex(index uint32) int {
	var raw [4]byte
	binary.BigEndian.PutUint32(raw[:], index)
	return int(binary.LittleEndian.Uint32(raw[:]))
}

// pinDisabled reports whether the operator turned the pin off. A TUN that
// already carries a working proxy would otherwise be bypassed, which is the
// one case where following the system route is preferable.
func pinDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("OPENCODE2API_WARP_NO_PIN"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// outboundInterface returns the index and name of the physical adapter WARP
// should leave through. fresh reports that the choice was made just now, so the
// caller logs it once instead of on every exit.
func outboundInterface() (index uint32, name string, fresh bool, ok bool) {
	pinnedInterface.mu.Lock()
	defer pinnedInterface.mu.Unlock()
	if pinnedInterface.index != 0 && time.Now().Before(pinnedInterface.expires) {
		return pinnedInterface.index, pinnedInterface.name, false, true
	}
	index, name, ok = probeOutboundInterface()
	if !ok {
		// A failed probe must not cost a pin that already works: a single lost
		// DNS query would hand the traffic back to the TUN. Keep the cached
		// adapter while it still exists and has an address.
		if pinnedInterface.index != 0 && interfaceAlive(pinnedInterface.index) {
			pinnedInterface.expires = time.Now().Add(pinTTL)
			return pinnedInterface.index, pinnedInterface.name, false, true
		}
		// Do not cache a failure for long: the adapter may still be coming up.
		pinnedInterface.index = 0
		pinnedInterface.expires = time.Now().Add(10 * time.Second)
		return 0, "", false, false
	}
	pinnedInterface.index, pinnedInterface.name = index, name
	pinnedInterface.expires = time.Now().Add(pinTTL)
	return index, name, true, true
}

// interfaceAlive reports whether the adapter with this index is still up and
// still carries a routable IPv4 address.
func interfaceAlive(index uint32) bool {
	interfaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, candidate := range interfaces {
		if uint32(candidate.Index) != index {
			continue
		}
		if candidate.Flags&net.FlagUp == 0 || candidate.Flags&net.FlagLoopback != 0 {
			return false
		}
		addresses, err := candidate.Addrs()
		return err == nil && hasGlobalIPv4(addresses)
	}
	return false
}

// probeOutboundInterface walks the host's adapters and returns the first one
// that both looks physical and demonstrably reaches the internet, so an
// adapter without a working route (a VM network, an unplugged Wi-Fi) is never
// chosen.
func probeOutboundInterface() (uint32, string, bool) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return 0, "", false
	}
	for _, candidate := range interfaces {
		if candidate.Flags&net.FlagUp == 0 || candidate.Flags&net.FlagLoopback != 0 {
			continue
		}
		name := strings.ToLower(candidate.Name)
		skipped := false
		for _, marker := range tunnelLike {
			if strings.Contains(name, marker) {
				skipped = true
				break
			}
		}
		if skipped {
			continue
		}
		addresses, err := candidate.Addrs()
		if err != nil || !hasGlobalIPv4(addresses) {
			continue
		}
		if probeInterface(uint32(candidate.Index)) {
			return uint32(candidate.Index), candidate.Name, true
		}
	}
	return 0, "", false
}

// hasGlobalIPv4 reports whether the adapter carries a routable IPv4 address. A
// link-local 169.254/16 address means the adapter never got a lease.
func hasGlobalIPv4(addresses []net.Addr) bool {
	for _, address := range addresses {
		prefix, err := netip.ParsePrefix(address.String())
		if err != nil {
			continue
		}
		if prefix.Addr().Is4() && prefix.Addr().IsGlobalUnicast() && !prefix.Addr().IsLinkLocalUnicast() {
			return true
		}
	}
	return false
}

// probeInterface sends one DNS query from the given interface and waits for the
// answer. A reply proves the adapter actually reaches the internet, which a
// plain route lookup cannot tell once a TUN owns the default route.
func probeInterface(index uint32) bool {
	level, option, value := syscall.IPPROTO_IP, optUnicastIf, networkOrderIndex(index)
	for attempt := 0; attempt < interfaceProbeAttempts; attempt++ {
		if probeInterfaceOnce(level, option, value) {
			return true
		}
	}
	return false
}

func probeInterfaceOnce(level, option, value int) bool {
	dialer := net.Dialer{
		Timeout: interfaceProbeTimeout,
		Control: func(_, _ string, raw syscall.RawConn) error {
			var setErr error
			if err := raw.Control(func(fd uintptr) {
				setErr = syscall.SetsockoptInt(syscall.Handle(fd), level, option, value)
			}); err != nil {
				return err
			}
			return setErr
		},
	}
	conn, err := dialer.Dial("udp4", "1.1.1.1:53")
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(interfaceProbeTimeout))
	if _, err := conn.Write(dnsProbeQuery); err != nil {
		return false
	}
	buffer := make([]byte, 512)
	n, err := conn.Read(buffer)
	if err != nil || n < 12 {
		return false
	}
	return buffer[0] == 0x1a && buffer[1] == 0x2b
}

// dnsProbeQuery is a minimal recursive A query for example.com, used only to
// ask whether the probed interface has a working default route.
var dnsProbeQuery = []byte{
	0x1a, 0x2b, 0x01, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x07, 'e', 'x', 'a', 'm', 'p', 'l', 'e',
	0x03, 'c', 'o', 'm', 0x00,
	0x00, 0x01, 0x00, 0x01,
}
