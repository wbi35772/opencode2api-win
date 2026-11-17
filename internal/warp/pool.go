package warp

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const DefaultPoolSize = 4

// maxPreload bounds how many exits are registered eagerly at startup. The rest
// of a larger pool still grows on demand, so a bigger ceiling does not hammer
// the registration API just to sit idle. Warm exits are the only capacity a
// burst can draw on, and a spent /64 forces a replacement mid-burst, so the
// floor is kept above one replacement's worth of headroom.
const maxPreload = 12

// egressCooldown keeps a public /64 (or IPv4 /32) that the upstream has
// rejected out of the pool for this long. The free tier scopes its quota to
// that block, so a new tunnel inside it would only repeat the same answer.
const egressCooldown = 24 * time.Hour

// maxEgressCooldown caps an upstream Retry-After. A free-tier window is hours
// long, and honoring an absurd header (or a clock skew) must not park a block
// forever.
const maxEgressCooldown = 24 * time.Hour

// transportCooldown parks an exit whose tunnel died for a local reason. The
// address may well be fine — Cloudflare often reissues the same block — so this
// only lasts long enough to stop a replacement from colliding with the exit
// that was just retired.
const transportCooldown = 90 * time.Second

// rateLimitCooldown parks an exit that answered with an ordinary rate limit
// rather than a spent quota. Rotating is still worthwhile, but the block should
// not be written off for hours.
const rateLimitCooldown = time.Minute

// maxRateLimitCooldown caps Retry-After for an ordinary rate limit, which is
// usually a short burst window rather than a daily free-tier quota.
const maxRateLimitCooldown = 5 * time.Minute

// fixed 24h blacklist for quota 429; retry-after can extend, capped at 24h.

// refillRetryDelay spaces out a failed registration attempt. Successful
// registrations run back to back in the background.
const refillRetryDelay = 5 * time.Second

// A pool can only hold as many exits as Cloudflare hands out distinct egress
// blocks. When preload is larger than that supply, every extra registration is
// a duplicate; after a few in a row the pool backs off instead of hammering the
// registration API forever. The backoff stays short: a duplicate draw says
// nothing about the next draw (WARP picks a random block every time), and a
// long pause here is exactly what turns a burst of spent /64s into minutes of
// "no ready egress" for live conversations.
const (
	refillFailureThreshold = 3
	refillBackoffBase      = 15 * time.Second
	refillBackoffMax       = time.Minute
)

// firstDuplicateRetryDelay spaces out first-exit attempts when Cloudflare keeps
// handing back /64 blocks that are already active or cooling. The spent-block
// set can dwarf the fresh supply, so a fixed handful of registrations is not
// enough to tell "no fresh egress" from "unlucky draw"; the first exit keeps
// trying until the caller's deadline instead of failing startup.
const firstDuplicateRetryDelay = 2 * time.Second

// verifyAttempts is how many registrations one replacement may try when the
// free-tier precheck is active. A spent block no longer costs a live request's
// first attempt, but it does cost a fresh registration, and most new /64s are
// spent; the plain budget would give up before finding a usable block.
const verifyAttempts = 8

// errDuplicateEgress reports a registration whose public egress block is
// already active or cooling down.
var errDuplicateEgress = errors.New("WARP egress is already active or cooling down")

// scaleDownDelay keeps exits created for a burst hot for a while after the
// burst, so a following burst reuses them instead of paying for new
// registrations.
const scaleDownDelay = 60 * time.Second

// ErrClosed is returned when a pool is used after Close. Callers use it to
// tell a retiring pool apart from a temporary capacity wait.
var ErrClosed = errors.New("WARP pool is closed")

// ErrNoExit is returned when the pool has no exit at all and none came up
// within emptyPoolWait. A caller that keeps waiting only turns a broken WARP
// into a request that hangs until its own deadline, so it fails fast instead.
var ErrNoExit = errors.New("WARP pool has no ready egress")

// emptyPoolWait bounds how long Acquire blocks on an empty pool. A registration
// normally finishes well inside this; anything longer means WARP is down.
const emptyPoolWait = 25 * time.Second

// ErrEgressSpent reports that a freshly registered exit's public block has
// already exhausted the upstream's free-tier quota. Most new /64s are someone
// else's spent block, so the pool quarantines the block on registration instead
// of spending a live request's first attempt on it.
var ErrEgressSpent = errors.New("WARP egress free-tier quota is spent")

// DialFunc opens one TCP connection through a candidate WARP exit. It mirrors
// exit.dialContext so a verifier uses exactly the tunnel the pool is judging.
type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// Verifier checks a newly registered exit against the upstream's free-tier
// quota before the pool admits it. A nil error admits the exit and records it
// as proven; ErrEgressSpent quarantines the public block for the quota
// cooldown; any other error admits the exit unproven, because a broken probe
// path must not keep replacements out of the pool.
type Verifier func(ctx context.Context, dial DialFunc) error

// maxAffinity bounds the conversation→exit map. A gateway that runs for weeks
// sees far more conversations than exits, so the map is capped and one entry is
// dropped at random when it fills; an evicted conversation simply lands on the
// ring again.
const maxAffinity = 4096

type logger interface {
	Debugf(string, ...interface{})
	Infof(string, ...interface{})
	Warnf(string, ...interface{})
	Errorf(string, ...interface{})
}

// slogLogger adapts the structured logger used by the gateway to the short
// diagnostic messages this package emits at its own lifecycle boundaries.
type slogLogger struct {
	log *slog.Logger
}

func (l slogLogger) logf(level slog.Level, format string, args ...interface{}) {
	if l.log == nil {
		return
	}
	l.log.Log(context.Background(), level, fmt.Sprintf(format, args...), "component", "warp")
}

func (l slogLogger) Debugf(format string, args ...interface{}) {
	l.logf(slog.LevelDebug, format, args...)
}

func (l slogLogger) Infof(format string, args ...interface{}) {
	l.logf(slog.LevelInfo, format, args...)
}

func (l slogLogger) Warnf(format string, args ...interface{}) {
	l.logf(slog.LevelWarn, format, args...)
}

func (l slogLogger) Errorf(format string, args ...interface{}) {
	l.logf(slog.LevelError, format, args...)
}

type Pool struct {
	ctx       context.Context
	cancel    context.CancelFunc
	logger    logger
	size      int
	preload   int
	stack     Stack
	statePath string
	newExit   func(context.Context) (*exit, error)
	// verify, when set, screens every new registration and the exits admitted
	// before the gateway existed. It is installed after construction because
	// the check runs through the gateway's free-tier request shape.
	verify Verifier
	// relay is the shared SOCKS5 UDP circuit breaker; nil when the outer UDP
	// goes direct.
	relay *relayGuard

	mu            sync.Mutex
	active        []*exit
	affinity      map[string]*exit
	inflight      int
	waiting       int
	next          int
	reserved      map[string]bool
	cooling       map[string]time.Time
	manual        map[string]bool
	falsePositive map[string]bool
	changed       chan struct{}
	closed        bool
	refilling     bool
	scaling       bool
	// refillFailures counts consecutive failed preload registrations;
	// refillPaused keeps every refill trigger quiet until it expires.
	refillFailures int
	refillPaused   time.Time
	wait           sync.WaitGroup
	createMu       sync.Mutex
	stateMu        sync.Mutex
}

type Lease struct {
	pool *Pool
	exit *exit
	once sync.Once
}

// Snapshot is a point-in-time view of a pool for health and admin reporting.
type Snapshot struct {
	Stack string
	Size  int
	// Preload is the number of exits kept hot even when the pool is idle: the
	// whole Size up to maxPreload, registered in the background after the first
	// exit so a burst does not wait for a registration.
	Preload int
	// Active is the number of exits currently in the ring. Standby counts the
	// idle part of it: exits not held by a lease and ready to replace a
	// penalized one immediately.
	Active  int
	Standby int
	Cooling int
	Egress  []string
}

// Options configure a WARP pool.
type Options struct {
	// Size is the upper bound on exits. The whole pool is kept hot up to
	// maxPreload even while it is idle; a larger ceiling grows on demand as
	// concurrency needs it and never exceeds Size.
	Size int
	// Stack selects the address family inside the tunnel. An IPv6 stack makes
	// the public egress an IPv6 address.
	Stack Stack
	// SourceIPv6, when valid, is the outer IPv6 prefix each exit binds its
	// WireGuard socket to. Every exit draws its own address from it, so a
	// replacement reaches Cloudflare from a new source and receives a new
	// public egress — the only way past an egress-scoped free usage limit.
	SourceIPv6 netip.Prefix
	// TUN makes every WARP socket follow the host's system route instead of
	// pinning it to the physical adapter and binding an outer source. Use it
	// when the WireGuard handshake must travel through a local TUN device.
	TUN bool
	// RegistryProxy carries only the HTTPS registration traffic. The WireGuard
	// data plane still leaves from this host: a proxy that does not relay UDP
	// cannot carry the tunnel, but it can keep device creation working while
	// the direct route to api.cloudflareclient.com is flaky.
	RegistryProxy *url.URL
	// SocksUDP routes the WireGuard outer UDP through a SOCKS5 proxy's UDP
	// ASSOCIATE relay (socks5://[user:pass@]host:port). Use it when this host
	// cannot send UDP directly but the proxy client can forward it. Empty keeps
	// the direct socket. When set, the relay owns the outer source, so
	// SourceIPv6 and TUN no longer apply to the data plane; TUN still selects
	// the registration route.
	SocksUDP string
	// StatePath persists the egress blocks the upstream has rejected, so a
	// restart does not register the same spent /64 again. Empty disables
	// persistence.
	StatePath string
}

// NewWithOptions creates a WARP pool whose exits use the selected IP stack and,
// optionally, rotate their outer IPv6 source within a prefix.
func NewWithOptions(ctx context.Context, log *slog.Logger, opts Options) (*Pool, error) {
	normalized, err := ParseStack(string(opts.Stack))
	if err != nil {
		return nil, err
	}
	if opts.SourceIPv6.IsValid() && (!opts.SourceIPv6.Addr().Is6() || opts.SourceIPv6.Bits() > 64) {
		return nil, fmt.Errorf("WARP outer source must be an IPv6 prefix of /64 or shorter, got %s", opts.SourceIPv6)
	}
	size := opts.Size
	if size <= 0 {
		size = DefaultPoolSize
	}
	formatter := slogLogger{log: log}
	r := newRegistrar(formatter, opts.RegistryProxy, opts.TUN)
	var relay *socks5Proxy
	var guard *relayGuard
	if opts.SocksUDP != "" {
		parsed, err := parseSocks5Proxy(opts.SocksUDP)
		if err != nil {
			return nil, fmt.Errorf("WARP socks_udp: %w", err)
		}
		relay = &parsed
		guard = newRelayGuard(formatter)
	}
	pool, err := newPool(ctx, formatter, size, opts.StatePath, func(ctx context.Context) (*exit, error) {
		if guard != nil {
			if err := guard.acquire(); err != nil {
				return nil, err
			}
			// Validate the relay before spending a device registration: a dead
			// proxy then costs one failed handshake, not a new WARP device on
			// every refill attempt.
			if err := relay.preflight(ctx, opts.TUN, formatter); err != nil {
				guard.failure(err)
				return nil, fmt.Errorf("WARP socks_udp: %w", err)
			}
		}
		reg, err := r.register(ctx)
		if err != nil {
			if guard != nil {
				guard.abandon()
			}
			return nil, err
		}
		var source netip.Addr
		if relay == nil && opts.SourceIPv6.IsValid() && !opts.TUN {
			source, err = randomSource(opts.SourceIPv6)
			if err != nil {
				return nil, err
			}
		}
		candidate, err := newExitWithRelay(ctx, reg, normalized, source, formatter, opts.TUN, relay, guard)
		if err != nil {
			if guard != nil {
				guard.failure(err)
			}
			return nil, err
		}
		if guard != nil {
			guard.success()
		}
		return candidate, nil
	})
	if err != nil {
		return nil, err
	}
	pool.stack = normalized
	pool.relay = guard
	return pool, nil
}

// randomSource draws one host address from prefix. The bits above the prefix
// length are kept, every bit below is random, so two exits practically never
// share an outer source.
func randomSource(prefix netip.Prefix) (netip.Addr, error) {
	address := prefix.Masked().Addr().As16()
	var noise [16]byte
	if _, err := rand.Read(noise[:]); err != nil {
		return netip.Addr{}, fmt.Errorf("draw WARP outer source: %w", err)
	}
	keep := prefix.Bits()
	for index := range address {
		switch bits := keep - index*8; {
		case bits >= 8:
		case bits <= 0:
			address[index] = noise[index]
		default:
			mask := byte(0xFF) << (8 - bits)
			address[index] = address[index]&mask | noise[index]&^mask
		}
	}
	return netip.AddrFrom16(address), nil
}

func newPool(ctx context.Context, log logger, size int, statePath string, create func(context.Context) (*exit, error)) (*Pool, error) {
	if size <= 0 {
		size = DefaultPoolSize
	}
	// Keep the whole pool warm, capped at maxPreload, so a burst finds ready
	// exits instead of paying for registrations while requests queue. The first
	// exit is synchronous and gates readiness; the rest register in the
	// background.
	preload := preloadFor(size)
	// The caller's context bounds the synchronous first registration, not the
	// pool: it is typically an initialization deadline that expires moments
	// after NewWithOptions returns. Deriving the pool lifetime from it would
	// cancel every later refill and replacement with "context canceled",
	// leaving the pool stuck on its first egress forever.
	poolCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	cooling, manual := loadPenalties(statePath)
	p := &Pool{
		ctx:       poolCtx,
		cancel:    cancel,
		logger:    log,
		size:      size,
		preload:   preload,
		statePath: statePath,
		newExit:   create,
		reserved:  make(map[string]bool),
		affinity:  make(map[string]*exit),
		cooling:   cooling,
		manual:    manual,
		changed:   make(chan struct{}),
	}
	if log != nil && len(p.cooling) > 0 {
		log.Infof("OpenCode WARP inherited %d spent egress block(s) from %s", len(p.cooling), statePath)
	}
	// The first registration gates readiness. Cloudflare can answer it with a
	// rate limit even when this process has just started (the limit follows the
	// source address), so wait out that window inside the caller's deadline
	// instead of failing startup over a pause that is about to end. A duplicate
	// /64 is the other transient answer: with a large spent-block set the next
	// registration often lands on an already cooling block, and a fixed attempt
	// budget would fail startup even though a fresh block is still reachable.
	// Keep retrying both until the caller's deadline; any other failure still
	// fails closed.
	first, err := p.createExit(ctx)
	for err != nil {
		pause := registrationPause(err)
		var delay time.Duration
		switch {
		case pause > 0:
			delay = pause
			if log != nil {
				log.Infof("OpenCode WARP first registration paused for %s", delay.Round(time.Second))
			}
		case errors.Is(err, errDuplicateEgress), errors.Is(err, ErrEgressSpent):
			delay = firstDuplicateRetryDelay
			if log != nil {
				log.Debugf("OpenCode WARP first egress is a spent block; retrying in %s", delay)
			}
		default:
			cancel()
			return nil, err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			cancel()
			return nil, ctx.Err()
		case <-timer.C:
		}
		first, err = p.createExit(ctx)
	}
	p.active = []*exit{first}
	if log != nil {
		log.Infof("OpenCode WARP/%s pool ready (max %d exits, preloading %d)", first.stack, size, preload)
	}
	p.startRefill()
	return p, nil
}

// preloadFor returns how many exits to register eagerly: the whole pool, capped
// at maxPreload. A larger ceiling still grows on demand.
func preloadFor(size int) int {
	if size <= 0 {
		size = DefaultPoolSize
	}
	if size > maxPreload {
		return maxPreload
	}
	return size
}

// desiredLocked is the number of exits the pool should keep hot: one per
// in-flight lease, one for every request waiting for an exit, plus a spare, so
// a 429 can switch to a ready exit instead of waiting for a registration. The
// preload floor keeps the whole pool registered while it is idle, up to
// maxPreload. Size is the upper bound.
func (p *Pool) desiredLocked() int {
	want := p.inflight + p.waiting + 1
	if want < p.preload {
		want = p.preload
	}
	if want < 2 {
		want = 2
	}
	if want > p.size {
		want = p.size
	}
	return want
}

func (p *Pool) Acquire(ctx context.Context) (*Lease, error) {
	return p.AcquireFor(ctx, "")
}

// AcquireFor leases an exit, preferring the one already serving affinity (the
// conversation key). A conversation that hops exits can look spent to a fresh
// block even when its previous exit was healthy, so the pin keeps a working
// pair together. With an empty affinity (probes, keyed traffic) the lease is
// proven-first: an exit that already answered a 2xx before an untested one,
// because a freshly registered public /64 is often already spent and answers
// 429 on its first request.
func (p *Pool) AcquireFor(ctx context.Context, affinity string) (*Lease, error) {
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, ErrClosed
		}
		if affinity != "" {
			if bound := p.affinity[affinity]; bound != nil {
				if p.activeIndexLocked(bound) >= 0 && bound.addRef() {
					p.inflight++
					p.mu.Unlock()
					// Demand just rose: grow the ring toward it in the background.
					p.startRefill()
					return &Lease{pool: p, exit: bound}, nil
				}
				// The exit was retired while this conversation was idle.
				delete(p.affinity, affinity)
			}
		}
		lease := p.leaseLocked(affinity, true)
		if lease == nil {
			lease = p.leaseLocked(affinity, false)
		}
		if lease != nil {
			p.mu.Unlock()
			// Demand just rose: grow the ring toward it in the background.
			p.startRefill()
			return lease, nil
		}
		p.waiting++
		changed := p.changed
		empty := len(p.active) == 0
		needRefill := len(p.active) < p.desiredLocked()
		p.mu.Unlock()
		if needRefill {
			// Queued demand is demand too: start growing before we block.
			p.startRefill()
		}
		var deadline <-chan time.Time
		if empty {
			// Nothing to wait for but a registration: fail before the caller's
			// own attempt budget turns into a minute-long hang.
			timer := time.NewTimer(emptyPoolWait)
			defer timer.Stop()
			deadline = timer.C
		}
		var err error
		select {
		case <-ctx.Done():
			err = ctx.Err()
		case <-p.ctx.Done():
			err = ErrClosed
		case <-changed:
		case <-deadline:
			err = ErrNoExit
		}
		p.mu.Lock()
		p.waiting--
		p.mu.Unlock()
		if err != nil {
			return nil, err
		}
	}
}

// leaseLocked walks the ring from p.next and leases the first exit that can
// take another request. provenOnly restricts the walk to exits with a recorded
// 2xx so an untested block is only tried when no proven one is free. The caller
// holds p.mu.
func (p *Pool) leaseLocked(affinity string, provenOnly bool) *Lease {
	for attempts := 0; attempts < len(p.active); attempts++ {
		index := p.next % len(p.active)
		p.next = (p.next + 1) % len(p.active)
		active := p.active[index]
		if provenOnly && !active.served() {
			continue
		}
		if active.addRef() {
			p.inflight++
			if affinity != "" {
				p.bindAffinityLocked(affinity, active)
			}
			return &Lease{pool: p, exit: active}
		}
	}
	return nil
}

// Unbind drops the exit pinned to affinity. A conversation that was answered
// 429 must not be handed the same exit again: the free tier scopes the
// rejection to the public block, so the retry walks the ring instead. The next
// AcquireFor invalidates a retired pin on its own; dropping it here keeps the
// retry independent of that lookup and of a concurrent replacement.
func (p *Pool) Unbind(affinity string) {
	if affinity == "" {
		return
	}
	p.mu.Lock()
	delete(p.affinity, affinity)
	p.mu.Unlock()
}

// SetVerifier installs the registration-time free-tier quota check. It is
// called after construction because the check itself needs the gateway that
// owns the pool. The exits admitted before this call (the synchronous first
// registration, and any preload that won the race) are screened in the
// background so the cold-start request does not sample a spent block either.
func (p *Pool) SetVerifier(verify Verifier) {
	p.mu.Lock()
	p.verify = verify
	admitted := append([]*exit(nil), p.active...)
	p.mu.Unlock()
	if verify == nil || len(admitted) == 0 {
		return
	}
	p.wait.Add(1)
	go func() {
		defer p.wait.Done()
		for _, candidate := range admitted {
			// A busy exit is already serving traffic: its outcome, not a
			// second probe, decides whether the block is spent.
			if candidate.served() || !candidate.idle() {
				continue
			}
			if err := p.checkQuota(p.ctx, candidate); errors.Is(err, ErrEgressSpent) {
				p.penalize(candidate, "free-tier precheck", 0, penaltyQuota)
			}
		}
	}()
}

// checkQuota runs the configured verifier against e. It returns nil when the
// exit is admitted (proven on a 2xx, merely unproven on any other probe
// failure) and ErrEgressSpent when the public block is already spent.
func (p *Pool) checkQuota(ctx context.Context, e *exit) error {
	p.mu.Lock()
	verify := p.verify
	p.mu.Unlock()
	if verify == nil {
		return nil
	}
	err := verify(ctx, e.dialContext)
	switch {
	case err == nil:
		e.markSuccess(time.Now())
		return nil
	case errors.Is(err, ErrEgressSpent):
		return ErrEgressSpent
	default:
		if p.logger != nil {
			p.logger.Debugf("OpenCode WARP precheck for %s was inconclusive: %v", e.egress, err)
		}
		return nil
	}
}

// quarantine writes a spent public block off for the fixed quota cooldown and
// closes the rejected candidate. The exit never joins the ring, so no live
// request pays an attempt on it.
func (p *Pool) quarantine(e *exit, until time.Time) {
	key := e.egressKey()
	p.mu.Lock()
	if key != "" {
		if parked, ok := p.cooling[key]; !ok || until.After(parked) {
			p.cooling[key] = until
		}
		delete(p.reserved, key)
	}
	p.mu.Unlock()
	p.saveCooling()
	if p.logger != nil {
		p.logger.Warnf("OpenCode WARP egress %s is already spent; cooling %s before use", e.egress, time.Until(until).Round(time.Second))
	}
	e.close()
}

// Report feeds one upstream response back into the pool. retryAfter, when the
// upstream sent one, lengthens the egress cooldown: the free-tier window is
// scoped to the public block and lasts hours, so parking the block for the
// header's duration keeps the pool from rotating back into a spent /64.
func (p *Pool) Report(lease *Lease, status int, body []byte, retryAfter time.Duration) {
	if lease == nil || lease.exit == nil {
		return
	}
	rotate, reason := lease.exit.observe(status, body)
	if !rotate {
		if status >= 200 && status < 300 {
			p.noteSuccess(lease.exit)
		}
		return
	}
	p.penalize(lease.exit, reason, retryAfter, classifyPenalty(status, body))
}

// PenalizeEgress parks the active exit whose public egress key is key. A 429
// can be held back while the caller validates whether it follows the session
// or the exit; when the rotated session is rejected too, this retires the held
// exit that was never reported. It reports whether an active exit matched.
func (p *Pool) PenalizeEgress(key string, status int, body []byte, retryAfter time.Duration) bool {
	if key == "" {
		return false
	}
	p.mu.Lock()
	var bad *exit
	for _, candidate := range p.active {
		if candidate.egressKey() == key {
			bad = candidate
			break
		}
	}
	p.mu.Unlock()
	if bad == nil {
		return false
	}
	p.penalize(bad, "429 survived session rotation", retryAfter, classifyPenalty(status, body))
	return true
}

// classifyPenalty separates a spent free-tier quota from an ordinary burst rate
// limit. Both retire the exit, but a quota answer writes the whole /64 off for
// hours while a rate limit only needs a short pause. Unknown bodies default to
// quota: treating a spent /64 as a temporary limit is what recreates the loop.
func classifyPenalty(status int, body []byte) penaltyKind {
	if status != 429 && status != 403 {
		return penaltyQuota
	}
	text := strings.ToLower(string(body))
	for _, marker := range []string{"quota", "usage limit", "free usage", "insufficient_quota", "billing", "credit", "额度"} {
		if strings.Contains(text, marker) {
			return penaltyQuota
		}
	}
	for _, marker := range []string{"rate_limit", "rate limit", "rate-limit", "too many requests", "try again"} {
		if strings.Contains(text, marker) {
			return penaltyRateLimit
		}
	}
	return penaltyQuota
}

// quotaCooldownFor returns a fixed 24h cooldown for quota 429. Retry-After
// can extend it, but never past maxEgressCooldown (24h). No escalation.
func quotaCooldownFor(_ int, retryAfter time.Duration) time.Duration {
	cooldown := egressCooldown
	if retryAfter > cooldown {
		cooldown = retryAfter
	}
	if cooldown > maxEgressCooldown {
		cooldown = maxEgressCooldown
	}
	return cooldown
}

// rateLimitCooldownFor keeps an ordinary rate limit short. A Retry-After may
// stretch it, but not into the quota range.
func rateLimitCooldownFor(retryAfter time.Duration) time.Duration {
	cooldown := rateLimitCooldown
	if retryAfter > cooldown {
		cooldown = retryAfter
	}
	if cooldown > maxRateLimitCooldown {
		cooldown = maxRateLimitCooldown
	}
	return cooldown
}

// noteSuccess stamps that the exit answered a real request with a 2xx. There is
// no strike escalation to clear (the blacklist is a fixed 24h), but the
// timestamp feeds the prefer-proven rule in AcquireFor: a block that has
// already served traffic is a far better bet than a freshly registered /64
// that most likely belongs to someone else's spent quota.
func (p *Pool) noteSuccess(e *exit) {
	if e != nil {
		e.markSuccess(time.Now())
	}
}

// Fail drops an exit that cannot carry traffic even though the upstream never
// produced an HTTP response, such as a broken WARP handshake or a stalled first
// token. Retrying the same user-space tunnel would only repeat the stall.
func (p *Pool) Fail(lease *Lease, reason string) {
	if lease == nil || lease.exit == nil || reason == "" {
		return
	}
	// A dead tunnel says nothing about the egress address: Cloudflare usually
	// hands the same /64 to the replacement, so parking it for the full
	// cooldown would lock the pool's only usable subnet out of rotation.
	p.penalize(lease.exit, reason, 0, penaltyTransport)
}

func (p *Pool) Size() int { return p.size }

// Snapshot reports the pool's current capacity and the public egress address
// of every active exit. It never exposes keys or device tokens.
func (p *Pool) Snapshot() Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	snapshot := Snapshot{Stack: string(p.stack), Size: p.size, Preload: p.preload, Active: len(p.active)}
	// Cooled subnets are swept lazily; counting the stored map would keep
	// reporting blocks whose cooldown has already run out.
	now := time.Now()
	for _, until := range p.cooling {
		if until.After(now) {
			snapshot.Cooling++
		}
	}
	for _, active := range p.active {
		if active.idle() {
			snapshot.Standby++
		}
		if active.egress.IsValid() {
			snapshot.Egress = append(snapshot.Egress, active.egress.String())
		}
	}
	return snapshot
}

func (p *Pool) penalize(bad *exit, reason string, retryAfter time.Duration, kind penaltyKind) {
	if p.relay != nil && kind == penaltyTransport {
		// A stalled request is not proof that this exit is bad: the shared
		// relay may be the failing hop. Feed the circuit breaker and, while it
		// is open, keep the exit rather than retiring it and asking the pool to
		// register a replacement through the same dead relay.
		p.relay.failure(errors.New(reason))
		if p.relay.open() {
			if p.logger != nil {
				p.logger.Warnf("OpenCode WARP relay circuit is open; keeping the egress active (%s)", reason)
			}
			return
		}
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	index := p.activeIndexLocked(bad)
	if index < 0 {
		p.mu.Unlock()
		return
	}
	if !bad.markPenalized(kind) {
		p.mu.Unlock()
		return
	}
	now := time.Now()
	key := bad.egressKey()
	cooldown := transportCooldown
	switch kind {
	case penaltyQuota:
		cooldown = quotaCooldownFor(1, retryAfter)
	case penaltyRateLimit:
		cooldown = rateLimitCooldownFor(retryAfter)
	}
	if key != "" {
		// Never shorten a block the upstream asked for.
		until := now.Add(cooldown)
		if parked, ok := p.cooling[key]; !ok || until.After(parked) {
			p.cooling[key] = until
		}
		delete(p.reserved, key)
	}
	p.active = append(p.active[:index], p.active[index+1:]...)
	if p.next >= len(p.active) {
		p.next = 0
	}
	p.notifyLocked()
	p.wait.Add(1)
	p.mu.Unlock()
	p.saveCooling()

	if p.logger != nil {
		p.logger.Warnf("Rotating OpenCode WARP egress %s (%s), cooling %s", bad.id, reason, cooldown.Round(time.Second))
	}
	// Re-register immediately: the retired exit is replaced in the background
	// and the retry draws whichever active exit is ready first.
	go func() {
		defer p.wait.Done()
		p.replace(bad, reason)
	}()
}

// coolingState is the on-disk form of the pool's penalty memory. Only rejected
// public blocks and their expiry are stored; fixed 24h blacklist, no strikes.
// and tokens stay out of it. Manual marks a block an operator banned by hand so
// the WebUI can tell it apart from an upstream quota penalty.
type coolingState struct {
	Cooling       map[string]time.Time `json:"cooling"`
	Manual        map[string]bool      `json:"manual,omitempty"`
	FalsePositive map[string]bool      `json:"false_positive"`
}

// loadPenalties reads the persisted egress cooldowns (fixed 24h blacklist). Cooling
// entries whose window has already ended are dropped; the cooldown is a fixed 24h blacklist. A
// missing or unreadable file yields empty state: the pool must still start when
// the file is gone or corrupt. The second result marks the blocks an operator
// banned by hand.
func loadPenalties(path string) (map[string]time.Time, map[string]bool) {
	cooling := make(map[string]time.Time)
	manual := make(map[string]bool)
	if path == "" {
		return cooling, manual
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return cooling, manual
	}
	var state coolingState
	if err := json.Unmarshal(raw, &state); err != nil {
		return cooling, manual
	}
	now := time.Now()
	for key, until := range state.Cooling {
		if key != "" && until.After(now) {
			cooling[key] = until
			if state.Manual[key] {
				manual[key] = true
			}
		}
	}
	return cooling, manual
}

// saveCooling writes the fixed 24h blacklist beside the config so a restart does not register a block the upstream already rejected. Writes are
// serialized and atomic; a failure only costs the restart guarantee, never the
// pool itself.
func (p *Pool) saveCooling() {
	if p.statePath == "" {
		return
	}
	p.mu.Lock()
	now := time.Now()
	state := coolingState{
		Cooling:       make(map[string]time.Time, len(p.cooling)),
		Manual:        make(map[string]bool, len(p.manual)),
		FalsePositive: make(map[string]bool),
	}
	for key, until := range p.cooling {
		if until.After(now) {
			state.Cooling[key] = until
			state.FalsePositive[key] = p.falsePositive[key]
			if p.manual[key] {
				state.Manual[key] = true
			}
		}
	}
	p.mu.Unlock()
	raw, err := json.Marshal(state)
	if err != nil {
		if p.logger != nil {
			p.logger.Warnf("Cannot encode OpenCode WARP penalty state: %v", err)
		}
		return
	}
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	temp := p.statePath + ".tmp"
	if err := os.WriteFile(temp, raw, 0o600); err != nil {
		if p.logger != nil {
			p.logger.Warnf("Cannot persist OpenCode WARP penalty state to %s: %v (check the config directory is writable)", p.statePath, err)
		}
		return
	}
	if err := os.Rename(temp, p.statePath); err != nil && p.logger != nil {
		p.logger.Warnf("Cannot replace OpenCode WARP penalty state at %s: %v", p.statePath, err)
	}
}

func (p *Pool) replace(old *exit, reason string) {
	replacement, err := p.createExit(p.ctx)
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		if replacement != nil {
			p.releaseReservation(replacement)
			replacement.close()
		}
		old.retire()
		return
	}
	if err != nil {
		kind := old.penaltyKind()
		restorable := kind == penaltyTransport || kind == penaltyNone
		restored := len(p.active) == 0 && restorable
		if restored {
			old.clearPenalty()
			if key := old.egressKey(); key != "" {
				p.reserved[key] = true
				delete(p.cooling, key)
			}
			p.active = append(p.active, old)
			p.notifyLocked()
		}
		p.mu.Unlock()
		p.saveCooling()
		if !restored {
			old.retire()
		}
		if !restorable && p.logger != nil {
			p.logger.Warnf("OpenCode WARP egress %s stays out of rotation (%s); waiting for a fresh /64", old.id, reason)
		}
		if p.logger != nil {
			if pause := registrationPause(err); pause > 0 {
				p.logger.Infof("OpenCode WARP replacement paused for %s", pause.Round(time.Second))
			} else {
				p.logger.Errorf("Cannot replace OpenCode WARP egress %s: %v", old.id, err)
			}
		}
		p.startRefill()
		return
	}
	if p.activeIndexLocked(old) >= 0 {
		p.mu.Unlock()
		p.releaseReservation(replacement)
		replacement.close()
		old.retire()
		return
	}
	placed := false
	if len(p.active) < p.size {
		p.active = append(p.active, replacement)
		placed = true
	}
	p.notifyLocked()
	p.mu.Unlock()
	if !placed {
		p.releaseReservation(replacement)
		replacement.close()
		old.retire()
		return
	}
	if p.logger != nil {
		p.logger.Infof("OpenCode WARP egress %s replaced by %s at %s (%s), active=%d", old.id, replacement.id, replacement.egress, reason, len(p.active))
	}
	old.retire()
	p.startRefill()
}

func (p *Pool) refill() {
	p.mu.Lock()
	if p.closed || p.refilling || time.Now().Before(p.refillPaused) || !p.needsRefillLocked() {
		p.mu.Unlock()
		return
	}
	p.refilling = true
	p.mu.Unlock()

	replacement, err := p.createExit(p.ctx)
	p.mu.Lock()
	p.refilling = false
	placed := false
	if err == nil && !p.closed && len(p.active) < p.size {
		p.active = append(p.active, replacement)
		replacement = nil
		placed = true
	}
	if err == nil {
		p.refillFailures = 0
		p.refillPaused = time.Time{}
	}
	if placed || p.closed {
		p.notifyLocked()
	}
	p.mu.Unlock()

	if replacement != nil {
		p.releaseReservation(replacement)
		replacement.close()
	}
	if err != nil && p.ctx.Err() == nil {
		delay := refillRetryDelay
		if pause := registrationPause(err); pause > 0 {
			// Cloudflare's rate limit is the pacing: wait out the window it
			// asked for instead of retrying into the same answer.
			delay = pause
			if delay < refillRetryDelay {
				delay = refillRetryDelay
			}
			if delay > maxRateLimitPause {
				delay = maxRateLimitPause
			}
			if p.logger != nil {
				p.logger.Infof("OpenCode WARP registration paused for %s", delay.Round(time.Second))
			}
		} else if errors.Is(err, errDuplicateEgress) {
			p.mu.Lock()
			p.refillFailures++
			failures := p.refillFailures
			p.mu.Unlock()
			if failures >= refillFailureThreshold {
				// The egress supply is smaller than the target. Back off
				// exponentially so a saturated colo is not registered into
				// forever.
				shift := failures - refillFailureThreshold
				if shift > 3 {
					shift = 3
				}
				delay = refillBackoffBase << shift
				if delay > refillBackoffMax {
					delay = refillBackoffMax
				}
				if p.logger != nil {
					p.logger.Infof("OpenCode WARP preload paused for %s after %d duplicate egresses", delay.Round(time.Second), failures)
				}
			}
		} else if pause := relayUnavailablePause(err); pause > 0 {
			// The shared relay circuit is open: do not register through it.
			// Pace the next attempt to the cooldown so refill stays quiet.
			delay = pause
			if delay < refillRetryDelay {
				delay = refillRetryDelay
			}
			if delay > maxRateLimitPause {
				delay = maxRateLimitPause
			}
			if p.logger != nil {
				p.logger.Infof("OpenCode WARP relay unavailable; next registration in %s", delay.Round(time.Second))
			}
		} else if p.logger != nil {
			p.logger.Warnf("Cannot pre-register OpenCode WARP egress: %v", err)
		}
		p.mu.Lock()
		if paused := time.Now().Add(delay); paused.After(p.refillPaused) {
			p.refillPaused = paused
		}
		p.mu.Unlock()
		p.after(delay, p.retryRefill)
		return
	}
	p.startRefill()
}

// retryRefill is the timer half of a failed refill: it clears the pause that
// kept ordinary triggers quiet and asks for another attempt. Going through
// startRefill (rather than calling refill directly) keeps the single-flight
// guard.
func (p *Pool) retryRefill() {
	p.mu.Lock()
	p.refillPaused = time.Time{}
	p.mu.Unlock()
	p.startRefill()
}

func (p *Pool) startRefill() {
	p.mu.Lock()
	if p.closed || p.refilling || time.Now().Before(p.refillPaused) || !p.needsRefillLocked() {
		p.mu.Unlock()
		return
	}
	p.wait.Add(1)
	p.mu.Unlock()
	go func() {
		defer p.wait.Done()
		p.refill()
	}()
}

// released records that a lease was returned and trims the ring back toward
// the demand of the moment. Freed exits stay hot for scaleDownDelay, so a
// burst that pauses does not immediately pay for re-registering them.
func (p *Pool) released() {
	p.mu.Lock()
	if p.inflight > 0 {
		p.inflight--
	}
	closed := p.closed
	wantRefill := !closed && len(p.active) < p.desiredLocked()
	started := p.scaling
	if !closed && !started && len(p.active) > p.desiredLocked() {
		p.scaling = true
		started = true
	}
	p.mu.Unlock()
	if closed {
		return
	}
	if wantRefill {
		p.startRefill()
	}
	if started {
		p.after(scaleDownDelay, p.scaleDown)
	}
}

// scaleDown retires every idle exit above the current demand. Busy surplus
// exits are left to their release, which arms another pass if the ring is
// still over target then.
func (p *Pool) scaleDown() {
	p.mu.Lock()
	p.scaling = false
	if p.closed {
		p.mu.Unlock()
		return
	}
	want := p.desiredLocked()
	var dropped []*exit
	if surplus := len(p.active) - want; surplus > 0 {
		kept := p.active[:0]
		for _, candidate := range p.active {
			if surplus > 0 && candidate.idle() {
				dropped = append(dropped, candidate)
				surplus--
				continue
			}
			kept = append(kept, candidate)
		}
		p.active = kept
	}
	if p.next >= len(p.active) {
		p.next = 0
	}
	p.mu.Unlock()
	for _, drop := range dropped {
		p.releaseReservation(drop)
		drop.retire()
		if p.logger != nil {
			p.logger.Infof("OpenCode WARP egress %s scaled down at %s", drop.id, drop.egress)
		}
	}
}

// after runs fn once delay has elapsed, unless the pool closes first. The wait
// group is incremented under the pool lock so it cannot race with Close's
// Wait: a timer scheduled after Close is not started at all, and one scheduled
// before it is counted before Close can begin waiting.
func (p *Pool) after(delay time.Duration, fn func()) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.wait.Add(1)
	p.mu.Unlock()
	go func() {
		defer p.wait.Done()
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-p.ctx.Done():
		case <-timer.C:
			fn()
		}
	}()
}

func (p *Pool) createExit(ctx context.Context) (*exit, error) {
	p.createMu.Lock()
	defer p.createMu.Unlock()
	var lastErr error
	attempts := 4
	p.mu.Lock()
	if p.verify != nil {
		attempts = verifyAttempts
	}
	p.mu.Unlock()
	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			if lastErr == nil {
				lastErr = err
			}
			break
		}
		p.mu.Lock()
		closed := p.closed
		p.mu.Unlock()
		if closed {
			return nil, ErrClosed
		}
		candidate, err := p.newExit(ctx)
		if err != nil {
			return nil, err
		}
		key := candidate.egressKey()
		now := time.Now()
		p.mu.Lock()
		for subnet, until := range p.cooling {
			if !until.After(now) {
				delete(p.cooling, subnet)
				delete(p.manual, subnet)
			}
		}
		usable := key != "" && !p.reserved[key] && !p.cooling[key].After(now)
		if usable {
			p.reserved[key] = true
		}
		p.mu.Unlock()
		if usable {
			if err := p.checkQuota(ctx, candidate); err != nil {
				// The block answered the free-tier quota limit before any
				// client saw it: park it and register a fresh /64 instead.
				p.quarantine(candidate, time.Now().Add(quotaCooldownFor(0, 0)))
				lastErr = fmt.Errorf("%w: %s", err, key)
				continue
			}
			if p.logger != nil {
				p.logger.Infof("OpenCode WARP/%s egress %s ready at %s", candidate.stack, candidate.id, candidate.egress)
			}
			return candidate, nil
		}
		lastErr = fmt.Errorf("%w: %s", errDuplicateEgress, key)
		if p.logger != nil {
			p.logger.Warnf("Discarding duplicate WARP exit %s: %v", candidate.id, lastErr)
		}
		candidate.close()
	}
	return nil, lastErr
}

func (p *Pool) releaseReservation(e *exit) {
	if e == nil {
		return
	}
	key := e.egressKey()
	if key == "" {
		return
	}
	p.mu.Lock()
	delete(p.reserved, key)
	p.mu.Unlock()
}

func (p *Pool) notifyLocked() {
	close(p.changed)
	p.changed = make(chan struct{})
}

func (p *Pool) activeIndexLocked(target *exit) int {
	for index, candidate := range p.active {
		if candidate == target {
			return index
		}
	}
	return -1
}

// bindAffinityLocked remembers which exit serves one conversation. The map is
// bounded: when it is full, an arbitrary conversation loses its pin and the
// next turn re-binds through the ring, which is no worse than the old behavior.
func (p *Pool) bindAffinityLocked(affinity string, e *exit) {
	if _, exists := p.affinity[affinity]; !exists && len(p.affinity) >= maxAffinity {
		for key := range p.affinity {
			delete(p.affinity, key)
			break
		}
	}
	p.affinity[affinity] = e
}

func (p *Pool) needsRefillLocked() bool {
	return len(p.active) < p.desiredLocked()
}

func (p *Pool) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	active := p.active
	p.active = nil
	p.notifyLocked()
	p.mu.Unlock()

	p.cancel()
	for _, item := range active {
		p.releaseReservation(item)
		item.retire()
	}
	p.wait.Wait()
}

func (l *Lease) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if l == nil || l.exit == nil {
		return nil, errors.New("WARP lease is closed")
	}
	return l.exit.dialContext(ctx, network, address)
}

func (l *Lease) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		if l.exit != nil {
			l.exit.release()
		}
		if l.pool != nil {
			l.pool.released()
		}
	})
}

func (l *Lease) EgressKey() string {
	if l == nil || l.exit == nil {
		return ""
	}
	return l.exit.egressKey()
}

// DialNetwork is the explicit TCP address family for this lease. Callers should
// pass it to DialContext instead of assuming IPv6.
func (l *Lease) DialNetwork() string {
	if l == nil || l.exit == nil {
		return ""
	}
	return l.exit.dialNetwork()
}

func (l *Lease) String() string {
	if l == nil || l.exit == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s/%s", l.exit.id, l.exit.egress)
}
