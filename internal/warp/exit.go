package warp

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

const (
	// probeTimeout bounds one verification attempt inside the fresh tunnel. It
	// has to cover the WireGuard handshake plus TCP and TLS: five seconds lands
	// exactly on the handshake retransmit, so a healthy tunnel that lost the
	// first initiation was retired as if it had never come up.
	probeTimeout = 7 * time.Second
	// probeCandidates caps how many Cloudflare endpoints are tried when the
	// first ones never answer. Targets race inside one endpoint, so a wider list
	// costs no extra wall time per endpoint.
	probeCandidates = 6
	// upstreamTraceURL is Cloudflare's diagnostic endpoint on the host the free
	// lane actually calls. It reports the egress address AND proves the tunnel
	// carries traffic to the upstream.
	upstreamTraceURL = "https://opencode.ai/cdn-cgi/trace"
)

type Stack string

const (
	StackIPv4 Stack = "ipv4"
	StackIPv6 Stack = "ipv6"
	StackDual Stack = "dual"

	// DefaultStack is IPv6: a WARP IPv4 egress is a public address shared by
	// every user behind Cloudflare's NAT, so the upstream's per-address free
	// quota is usually already spent, while the IPv6 egress belongs to the
	// tunnel. The handshake still falls back to IPv4 endpoints when IPv6 does
	// not answer, so an IPv4-only host keeps working without extra settings.
	DefaultStack = StackIPv6
)

var (
	dnsV4 = netip.MustParseAddr("1.1.1.1")
	dnsV6 = netip.MustParseAddr("2606:4700:4700::1111")
)

func ParseStack(value string) (Stack, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return DefaultStack, nil
	case "ipv6", "v6":
		return StackIPv6, nil
	case "ipv4", "v4":
		return StackIPv4, nil
	case "dual", "both":
		return StackDual, nil
	default:
		return "", fmt.Errorf("unsupported WARP stack %q; use ipv6, ipv4 or dual", value)
	}
}

type exit struct {
	id        string
	egress    netip.Addr
	stack     Stack
	device    *device.Device
	network   *netstack.Net
	log       logger
	mu        sync.Mutex
	refs      int
	retired   bool
	closed    bool
	penalized bool
	closeOnce sync.Once
}

func newExit(ctx context.Context, reg registration) (*exit, error) {
	return newExitWithStack(ctx, reg, DefaultStack, netip.Addr{}, nil)
}

// newExitWithStack builds one userspace tunnel for reg. A valid source pins
// the outer WireGuard socket to that address and restricts the peer endpoints
// to IPv6, so the public egress is derived from the rotating source rather than
// from whatever address the host happens to share.
func newExitWithStack(ctx context.Context, reg registration, stack Stack, source netip.Addr, log logger) (*exit, error) {
	normalized, err := ParseStack(string(stack))
	if err != nil {
		return nil, err
	}
	stack = normalized
	localV6, v6Err := parseAddress(reg.AddressV6)
	localV4, v4Err := parseAddress(reg.AddressV4)
	var localAddresses []netip.Addr
	var dns []netip.Addr
	switch stack {
	case StackIPv6:
		if v6Err != nil || !localV6.Is6() {
			return nil, fmt.Errorf("invalid WARP IPv6 address %q", reg.AddressV6)
		}
		localAddresses = []netip.Addr{localV6}
		dns = []netip.Addr{dnsV6}
	case StackIPv4:
		if v4Err != nil || !localV4.Is4() {
			return nil, fmt.Errorf("invalid WARP IPv4 address %q", reg.AddressV4)
		}
		localAddresses = []netip.Addr{localV4}
		dns = []netip.Addr{dnsV4}
	case StackDual:
		if v6Err == nil && localV6.Is6() {
			localAddresses = append(localAddresses, localV6)
		}
		if v4Err == nil && localV4.Is4() {
			localAddresses = append(localAddresses, localV4)
		}
		if len(localAddresses) == 0 {
			return nil, fmt.Errorf("WARP registration has no usable address: v4=%q v6=%q", reg.AddressV4, reg.AddressV6)
		}
		dns = []netip.Addr{dnsV4, dnsV6}
	}
	tunDevice, network, err := netstack.CreateNetTUN(localAddresses, dns, 1280)
	if err != nil {
		return nil, fmt.Errorf("create WARP network stack: %w", err)
	}
	privateKey, err := uapiKey(reg.PrivateKey)
	if err != nil {
		tunDevice.Close()
		return nil, fmt.Errorf("decode WARP private key: %w", err)
	}
	peerKey, err := uapiKey(reg.PeerPublicKey)
	if err != nil {
		tunDevice.Close()
		return nil, fmt.Errorf("decode WARP peer key: %w", err)
	}
	endpoints, err := resolveEndpoints(ctx, reg.Endpoints, reg.Endpoint)
	if err != nil {
		tunDevice.Close()
		return nil, err
	}
	if source.IsValid() {
		// Try IPv6 endpoints first so the rotating source is used, but keep
		// the IPv4 ones behind them: a host (or network) that cannot reach
		// Cloudflare over IPv6 still gets a working tunnel, just with the
		// shared address the prefix was supposed to avoid.
		endpoints = preferIPv6Endpoints(endpoints)
	} else if outerInterfacePinned() {
		// The socket leaves through the physical adapter, and that adapter's
		// IPv6 route is the one that tends to be missing on a home network. A
		// dead IPv6 endpoint costs a full probe timeout before the IPv4 one is
		// tried, so put IPv4 first; the tunnel egress is chosen by the stack,
		// not by the outer address family.
		endpoints = preferIPv4Endpoints(endpoints)
	}
	endpoints = diversifyEndpoints(endpoints, probeCandidates)
	wg := device.NewDevice(tunDevice, &simpleBind{source: source, log: log}, device.NewLogger(device.LogLevelError, "warp: "))
	uapi := fmt.Sprintf(
		"private_key=%s\npublic_key=%s\nallowed_ip=0.0.0.0/0\nallowed_ip=::/0\nendpoint=%s\npersistent_keepalive_interval=25\n",
		privateKey, peerKey, endpoints[0],
	)
	if err := wg.IpcSet(uapi); err != nil {
		wg.Close()
		<-wg.Wait()
		return nil, fmt.Errorf("configure WARP device: %w", err)
	}
	if err := wg.Up(); err != nil {
		wg.Close()
		<-wg.Wait()
		return nil, fmt.Errorf("start WARP device: %w", err)
	}
	e := &exit{id: reg.ID, stack: stack, device: wg, network: network, log: log}
	egress, err := e.probe(ctx, wg, peerKey, endpoints)
	if err != nil {
		e.close()
		return nil, err
	}
	e.egress = egress
	return e, nil
}

// preferIPv6Endpoints returns endpoints with the IPv6 ones first and the IPv4
// ones after them, both in their original order. Nothing is dropped: an IPv4
// fallback keeps WARP usable on a host that cannot reach Cloudflare over IPv6.
func preferIPv6Endpoints(endpoints []string) []string {
	ordered := make([]string, 0, len(endpoints))
	ipv4 := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		host, _, err := net.SplitHostPort(endpoint)
		if err != nil {
			continue
		}
		addr, err := netip.ParseAddr(host)
		if err != nil || addr.Is4In6() {
			continue
		}
		if addr.Is6() {
			ordered = append(ordered, endpoint)
		} else {
			ipv4 = append(ipv4, endpoint)
		}
	}
	return append(ordered, ipv4...)
}

// preferIPv4Endpoints returns endpoints with the IPv4 ones first, keeping the
// IPv6 ones behind them as a fallback. A socket pinned to a physical adapter
// uses this: on a host whose adapter has no working IPv6 route the IPv6
// endpoints are dead ends that cost a full probe timeout each.
func preferIPv4Endpoints(endpoints []string) []string {
	ordered := make([]string, 0, len(endpoints))
	ipv6 := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		host, _, err := net.SplitHostPort(endpoint)
		if err != nil {
			continue
		}
		addr, err := netip.ParseAddr(host)
		if err != nil || addr.Is4In6() {
			continue
		}
		if addr.Is4() {
			ordered = append(ordered, endpoint)
		} else {
			ipv6 = append(ipv6, endpoint)
		}
	}
	return append(ordered, ipv6...)
}

func resolveEndpoints(ctx context.Context, candidates []string, fallback string) ([]string, error) {
	if len(candidates) == 0 && fallback != "" {
		candidates = []string{fallback}
	}
	seen := make(map[string]bool)
	var endpoints []string
	for _, candidate := range candidates {
		normalized, err := normalizeEndpoint(candidate)
		if err != nil {
			continue
		}
		host, port, _ := net.SplitHostPort(normalized)
		if net.ParseIP(host) != nil {
			if !seen[normalized] {
				seen[normalized] = true
				endpoints = append(endpoints, normalized)
			}
		} else {
			addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				continue
			}
			for _, ip := range addrs {
				value := net.JoinHostPort(ip.IP.String(), port)
				if !seen[value] {
					seen[value] = true
					endpoints = append(endpoints, value)
				}
			}
		}
		if len(endpoints) >= probeCandidates*2 {
			break
		}
	}
	if len(endpoints) == 0 {
		return nil, fmt.Errorf("resolve WARP endpoint candidates %v", candidates)
	}
	return endpoints, nil
}

// diversifyEndpoints picks the first limit candidates so that the ports differ
// before the addresses do. Cloudflare publishes the same anycast host on 2408,
// 500, 1701 and 4500, and a network that filters one UDP port usually leaves
// the others alone; walking every address of 2408 first would never reach them.
func diversifyEndpoints(endpoints []string, limit int) []string {
	byGroup := make(map[string][]string)
	var groups []string
	for _, endpoint := range endpoints {
		host, port, err := net.SplitHostPort(endpoint)
		if err != nil {
			continue
		}
		family := "v4"
		if addr, err := netip.ParseAddr(host); err == nil && addr.Is6() {
			family = "v6"
		}
		key := port + "/" + family
		if _, seen := byGroup[key]; !seen {
			groups = append(groups, key)
		}
		byGroup[key] = append(byGroup[key], endpoint)
	}
	var ordered []string
	for depth := 0; len(ordered) < limit; depth++ {
		added := false
		for _, group := range groups {
			bucket := byGroup[group]
			if depth >= len(bucket) {
				continue
			}
			ordered = append(ordered, bucket[depth])
			added = true
			if len(ordered) == limit {
				break
			}
		}
		if !added {
			break
		}
	}
	return ordered
}

func normalizeEndpoint(endpoint string) (string, error) {
	endpoint = strings.TrimSpace(endpoint)
	if !strings.Contains(endpoint, ":") {
		endpoint = net.JoinHostPort(endpoint, "2408")
	} else if addr, err := netip.ParseAddr(endpoint); err == nil {
		endpoint = net.JoinHostPort(addr.String(), "2408")
	}
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return "", fmt.Errorf("invalid WARP endpoint %q", endpoint)
	}
	if port == "" || port == "0" {
		port = "2408"
	}
	return net.JoinHostPort(host, port), nil
}

func parseAddress(value string) (netip.Addr, error) {
	value = strings.TrimSpace(value)
	if addr, err := netip.ParseAddr(value); err == nil {
		return addr.Unmap(), nil
	}
	prefix, err := netip.ParsePrefix(value)
	if err != nil {
		return netip.Addr{}, err
	}
	return prefix.Addr().Unmap(), nil
}

func uapiKey(encoded string) (string, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil || len(key) != 32 {
		return "", errors.New("invalid WireGuard key")
	}
	return hex.EncodeToString(key), nil
}

type probeTarget struct {
	network string
	url     string
}

func probeTargets(stack Stack) []probeTarget {
	// The upstream WARP has to carry is the first thing worth proving. Its
	// Cloudflare trace doubles as the egress readout, and unlike 1.1.1.1 it is
	// the host the free lane actually talks to: a tunnel that reaches
	// opencode.ai is usable even when 1.1.1.1 is filtered inside WARP.
	upstream := probeTarget{network: "tcp4", url: upstreamTraceURL}
	switch stack {
	case StackIPv4:
		return []probeTarget{upstream, {network: "tcp4", url: "https://1.1.1.1/cdn-cgi/trace"}}
	case StackDual:
		return []probeTarget{
			{network: "tcp6", url: upstreamTraceURL},
			upstream,
			{network: "tcp4", url: "https://1.1.1.1/cdn-cgi/trace"},
			{network: "tcp6", url: "https://[2606:4700:4700::1111]/cdn-cgi/trace"},
		}
	default:
		return []probeTarget{
			{network: "tcp6", url: upstreamTraceURL},
			{network: "tcp6", url: "https://[2606:4700:4700::1111]/cdn-cgi/trace"},
		}
	}
}

func (e *exit) probe(ctx context.Context, wg *device.Device, peerKey string, endpoints []string) (netip.Addr, error) {
	if len(endpoints) > probeCandidates {
		endpoints = endpoints[:probeCandidates]
	}
	// Keep every endpoint's failure: a bare "deadline exceeded" hides whether the
	// tunnel never came up or came up and stalled on the request, which are
	// different faults to chase.
	failures := make([]string, 0, len(endpoints))
	for index, endpoint := range endpoints {
		if index > 0 {
			if err := wg.IpcSet(fmt.Sprintf("public_key=%s\nendpoint=%s\n", peerKey, endpoint)); err != nil {
				failures = append(failures, fmt.Sprintf("%s: switch endpoint: %v", endpoint, err))
				continue
			}
		}
		egress, err := e.probeEndpoint(ctx, endpoint)
		if err == nil {
			return egress, nil
		}
		failures = append(failures, fmt.Sprintf("%s: %v", endpoint, err))
		if ctx.Err() != nil {
			break
		}
		// No handshake at all after both address families on the first two
		// ports means Cloudflare never answered this device's key: every
		// remaining endpoint carries the same key and will be dropped the same
		// way, so the caller should register a fresh device instead of grinding
		// through the fallback list.
		if index >= 3 && !handshakeSeen(wg) {
			return netip.Addr{}, fmt.Errorf("WARP %s handshake never answered (%s): %s", e.stack, peerStats(wg), strings.Join(failures, "; "))
		}
	}
	return netip.Addr{}, fmt.Errorf("verify WARP %s egress over %d endpoints (%s): %s", e.stack, len(endpoints), peerStats(wg), strings.Join(failures, "; "))
}

// probeEndpoint tries every trace target of one endpoint at once. Both targets
// ride the same tunnel, so racing them costs one probeTimeout instead of two and
// still surfaces as soon as either proves the tunnel carries traffic.
func (e *exit) probeEndpoint(ctx context.Context, endpoint string) (netip.Addr, error) {
	targets := probeTargets(e.stack)
	type outcome struct {
		egress netip.Addr
		err    error
	}
	results := make(chan outcome, len(targets))
	for _, target := range targets {
		go func(t probeTarget) {
			egress, err := e.probeTarget(ctx, t)
			results <- outcome{egress: egress, err: err}
		}(target)
	}
	var failures []string
	var found netip.Addr
	for range targets {
		result := <-results
		if result.err == nil && !found.IsValid() {
			found = result.egress
		}
		if result.err != nil {
			failures = append(failures, result.err.Error())
		}
	}
	if found.IsValid() {
		return found, nil
	}
	return netip.Addr{}, fmt.Errorf("%s", strings.Join(failures, "; "))
}

// peerStats summarizes the WireGuard peer counters so a failed probe says
// whether Cloudflare answered at all. rx_bytes stays 0 when the outer UDP path
// is dead; a completed handshake with a stalled request points at the tunnel's
// inner route instead.
func peerStats(wg *device.Device) string {
	raw, err := wg.IpcGet()
	if err != nil {
		return "peer stats unavailable"
	}
	var rx, tx, handshake string
	for _, line := range strings.Split(raw, "\n") {
		switch {
		case strings.HasPrefix(line, "rx_bytes="):
			rx = strings.TrimPrefix(line, "rx_bytes=")
		case strings.HasPrefix(line, "tx_bytes="):
			tx = strings.TrimPrefix(line, "tx_bytes=")
		case strings.HasPrefix(line, "last_handshake_time_sec="):
			handshake = strings.TrimPrefix(line, "last_handshake_time_sec=")
		}
	}
	if handshake == "" || handshake == "0" {
		handshake = "never"
	}
	return fmt.Sprintf("handshake=%s rx=%s tx=%s", handshake, rx, tx)
}

// handshakeSeen reports whether Cloudflare has answered this device at all. A
// device whose key was never acknowledged cannot be rescued by a different
// endpoint, because every endpoint validates the same key.
func handshakeSeen(wg *device.Device) bool {
	raw, err := wg.IpcGet()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "last_handshake_time_sec=") {
			return strings.TrimPrefix(line, "last_handshake_time_sec=") != "0"
		}
	}
	return false
}

// probeTarget asks the fresh tunnel for its public address. The handshake
// retransmits on its own schedule, so a single slow attempt is retried by the
// next endpoint or target rather than by hammering the same one.
func (e *exit) probeTarget(ctx context.Context, target probeTarget) (netip.Addr, error) {
	egress, err := e.probeTargetOnce(ctx, target)
	if err == nil {
		e.logf("WARP %s probe %s: egress %s", e.stack, target.url, egress)
		return egress, nil
	}
	e.logf("WARP %s probe %s failed: %v", e.stack, target.url, err)
	return netip.Addr{}, err
}

// logf reports a probe result when the caller supplied a logger. Probes are the
// boundary where a live tunnel is separated from a dead one, so their failures
// are worth keeping at debug level.
func (e *exit) logf(format string, args ...interface{}) {
	if e.log != nil {
		e.log.Debugf(format, args...)
	}
}

func (e *exit) probeTargetOnce(ctx context.Context, target probeTarget) (netip.Addr, error) {
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	transport := &http.Transport{
		Proxy:               nil,
		ForceAttemptHTTP2:   false,
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: 10 * time.Second,
		DialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
			return e.network.DialContext(ctx, target.network, address)
		},
	}
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, target.url, nil)
	if err != nil {
		return netip.Addr{}, err
	}
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		return netip.Addr{}, err
	}
	defer resp.Body.Close()
	egress, err := parseTrace(resp.Body)
	if err != nil {
		return netip.Addr{}, err
	}
	if target.network == "tcp4" && !egress.Is4() {
		return netip.Addr{}, fmt.Errorf("WARP trace over IPv4 reported %s", egress)
	}
	if target.network == "tcp6" && !egress.Is6() {
		return netip.Addr{}, fmt.Errorf("WARP trace over IPv6 reported %s", egress)
	}
	return egress, nil
}

func parseTrace(body io.Reader) (netip.Addr, error) {
	scanner := bufio.NewScanner(io.LimitReader(body, 64<<10))
	var rawIP string
	warpEnabled := false
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "ip":
			rawIP = strings.TrimSpace(value)
		case "warp":
			warpEnabled = strings.TrimSpace(value) == "on"
		}
	}
	if err := scanner.Err(); err != nil {
		return netip.Addr{}, err
	}
	if !warpEnabled {
		return netip.Addr{}, errors.New("Cloudflare trace reports warp=off")
	}
	addr, err := netip.ParseAddr(rawIP)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("invalid trace IP %q", rawIP)
	}
	addr = addr.Unmap()
	if (!addr.Is4() && !addr.Is6()) || !addr.IsGlobalUnicast() || addr.IsPrivate() {
		return netip.Addr{}, fmt.Errorf("trace returned non-public address %s", addr)
	}
	return addr, nil
}

func (e *exit) addRef() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.retired || e.closed {
		return false
	}
	e.refs++
	return true
}

func (e *exit) release() {
	e.mu.Lock()
	if e.refs > 0 {
		e.refs--
	}
	closeNow := e.refs == 0 && e.retired && !e.closed
	e.mu.Unlock()
	if closeNow {
		e.close()
	}
}

func (e *exit) retire() {
	e.mu.Lock()
	e.retired = true
	closeNow := e.refs == 0 && !e.closed
	e.mu.Unlock()
	if closeNow {
		e.close()
	}
}

func (e *exit) close() {
	e.closeOnce.Do(func() {
		e.mu.Lock()
		e.closed = true
		e.mu.Unlock()
		if e.device != nil {
			e.device.Close()
			<-e.device.Wait()
		}
	})
}

func (e *exit) observe(status int, body []byte) (bool, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.penalized {
		return false, ""
	}
	if status == http.StatusForbidden {
		reason := "HTTP 403"
		if networkAnomaly(body) {
			reason += " network anomaly"
		}
		return true, reason
	}
	// A free-tier 429 is scoped to the egress address, not to the request, the
	// key or the session: retrying the same exit repeats it. Replace the exit
	// on the first one, as the instant-rotate deployment does, so the retry
	// draws a fresh outer source and a new public egress.
	if status == http.StatusTooManyRequests {
		return true, "HTTP 429"
	}
	return false, ""
}

func (e *exit) markPenalized() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.penalized {
		return false
	}
	e.penalized = true
	return true
}

func (e *exit) clearPenalty() {
	e.mu.Lock()
	e.penalized = false
	e.mu.Unlock()
}

func networkAnomaly(body []byte) bool {
	return strings.Contains(strings.ToLower(string(body)), "network")
}

func (e *exit) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if e.network == nil {
		return nil, errors.New("WARP network is closed")
	}
	network = strings.ToLower(strings.TrimSpace(network))
	switch network {
	case "tcp":
		network = e.dialNetwork()
	case "tcp4", "tcp6":
	default:
		return nil, fmt.Errorf("WARP supports TCP only, got %q", network)
	}
	switch e.stack {
	case StackIPv4:
		if network != "tcp4" {
			return nil, fmt.Errorf("WARP IPv4 stack cannot dial %q", network)
		}
	case StackIPv6:
		if network != "tcp6" {
			return nil, fmt.Errorf("WARP IPv6 stack cannot dial %q", network)
		}
	case StackDual:
		// Both address families are present in the netstack.
	default:
		return nil, fmt.Errorf("unsupported WARP stack %q", e.stack)
	}
	return e.network.DialContext(ctx, network, address)
}

func (e *exit) dialNetwork() string {
	switch e.stack {
	case StackIPv4:
		return "tcp4"
	case StackDual:
		if e.egress.Is4() {
			return "tcp4"
		}
		return "tcp6"
	default:
		return "tcp6"
	}
}

func (e *exit) egressKey() string {
	if e.egress.Is4() {
		return netip.PrefixFrom(e.egress, 32).Masked().String()
	}
	if !e.egress.Is6() {
		return ""
	}
	// The free tier treats a Cloudflare /64 as one quota scope: rotating inside
	// the same /64 yields a new /128 but the same exhausted quota. Keying on
	// the /64 keeps every active and cooling exit on a distinct scope, so a
	// replacement is a real rotation rather than a cosmetic address change.
	return netip.PrefixFrom(e.egress, 64).Masked().String()
}

// idle reports whether no lease currently holds this exit.
func (e *exit) idle() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.refs == 0 && !e.retired && !e.closed
}
