package warp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"sort"
	"strings"
	"time"
)

// ErrInvalidEgressBlock reports a subnet the pool cannot address: the free tier
// scopes its quota to one public block, so only a single IPv4 /32 or IPv6 /64 is
// meaningful.
var ErrInvalidEgressBlock = errors.New("invalid egress block")

// BlacklistEntry is one public egress block the pool refuses to register.
type BlacklistEntry struct {
	Subnet string    `json:"subnet"`
	Until  time.Time `json:"until"`
	// Manual marks a block an operator banned by hand rather than an upstream
	// quota penalty.
	Manual bool `json:"manual"`
	// RemainingSeconds is how long the block stays out of rotation. It is
	// derived at read time so a long-lived page does not show a stale clock.
	RemainingSeconds int64 `json:"remaining_seconds"`
	// Active counts the live exits still serving this block. A manual ban on a
	// leased exit takes effect once that lease ends.
	Active int `json:"active"`
}

// NormalizeEgressBlock turns an operator-typed address or prefix into the key
// the pool uses: a bare IPv4 address or /32, a bare IPv6 address or /64.
// Anything wider is refused because banning a /48 would silently cover more
// quota than the operator can see in the table.
func NormalizeEgressBlock(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("%w: empty", ErrInvalidEgressBlock)
	}
	var (
		addr netip.Addr
		bits = -1
	)
	if strings.Contains(value, "/") {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrInvalidEgressBlock, err)
		}
		addr, bits = prefix.Addr(), prefix.Bits()
	} else {
		parsed, err := netip.ParseAddr(value)
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrInvalidEgressBlock, err)
		}
		addr = parsed
	}
	addr = addr.Unmap()
	want := 64
	if addr.Is4() {
		want = 32
	}
	if bits >= 0 && bits != want {
		return "", fmt.Errorf("%w: expected an address or /%d block, got /%d", ErrInvalidEgressBlock, want, bits)
	}
	return netip.PrefixFrom(addr, want).Masked().String(), nil
}

// StatePath is the file the pool persists its bans to. It is empty when the
// operator disabled persistence.
func (p *Pool) StatePath() string {
	return p.statePath
}

// BanEgress keeps one public block out of rotation until the given expiry. The
// ban survives a restart through the same state file the quota penalties use,
// and an existing longer penalty is never shortened. Idle exits already serving
// the block are retired immediately so the next request draws a different one.
func (p *Pool) BanEgress(raw string, until time.Time) (BlacklistEntry, error) {
	key, err := NormalizeEgressBlock(raw)
	if err != nil {
		return BlacklistEntry{}, err
	}
	until = until.UTC()
	if !until.After(time.Now()) {
		return BlacklistEntry{}, fmt.Errorf("%w: expiry must be in the future", ErrInvalidEgressBlock)
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return BlacklistEntry{}, ErrClosed
	}
	if p.manual == nil {
		p.manual = make(map[string]bool)
	}
	if current, ok := p.cooling[key]; !ok || until.After(current) {
		p.cooling[key] = until
	}
	p.manual[key] = true
	delete(p.reserved, key)
	evicted := p.evictLocked(key)
	effective := p.cooling[key]
	entry := BlacklistEntry{Subnet: key, Until: effective, Manual: true, RemainingSeconds: int64(time.Until(effective).Seconds()), Active: countActiveLocked(p, key)}
	p.notifyLocked()
	p.mu.Unlock()
	p.saveCooling()
	if p.logger != nil {
		p.logger.Warnf("OpenCode WARP egress %s banned by hand until %s (evicted %d idle exit(s))", key, until.Format(time.RFC3339), evicted)
	}
	return entry, nil
}

// ReleaseEgress removes a ban, whether it came from an operator or from an
// upstream quota rejection. It reports whether anything was removed so a stale
// page can tell "already lifted" from "unknown subnet".
func (p *Pool) ReleaseEgress(raw string) (bool, error) {
	key, err := NormalizeEgressBlock(raw)
	if err != nil {
		return false, err
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return false, ErrClosed
	}
	_, hadPenalty := p.cooling[key]
	_, hadManual := p.manual[key]
	if !hadPenalty && !hadManual {
		p.mu.Unlock()
		return false, nil
	}
	delete(p.cooling, key)
	delete(p.manual, key)
	delete(p.reserved, key)
	p.notifyLocked()
	p.mu.Unlock()
	p.saveCooling()
	if p.logger != nil {
		p.logger.Infof("OpenCode WARP egress %s released; it may be registered again", key)
	}
	return true, nil
}

// Blacklist returns every block still out of rotation, soonest expiry first.
func (p *Pool) Blacklist() []BlacklistEntry {
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	entries := make([]BlacklistEntry, 0, len(p.cooling))
	for key, until := range p.cooling {
		if !until.After(now) {
			continue
		}
		entries = append(entries, BlacklistEntry{
			Subnet:           key,
			Until:            until,
			Manual:           p.manual[key],
			RemainingSeconds: int64(until.Sub(now).Seconds()),
			Active:           countActiveLocked(p, key),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Until.Equal(entries[j].Until) {
			return entries[i].Subnet < entries[j].Subnet
		}
		return entries[i].Until.Before(entries[j].Until)
	})
	return entries
}

// ReloadBlacklist replaces the in-memory bans with the current contents of the
// state file, so an operator can edit the JSON by hand and apply it without a
// restart. The file is authoritative: entries written by the pool itself are
// already on disk, and a live penalty added between the read and the swap is
// re-added by the next rejection.
func (p *Pool) ReloadBlacklist() (added, removed int, err error) {
	path := p.statePath
	if path == "" {
		return 0, 0, errors.New("WARP penalty persistence is disabled; there is no file to load")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, err
	}
	var state coolingState
	if err := json.Unmarshal(raw, &state); err != nil {
		return 0, 0, fmt.Errorf("decode %s: %w", path, err)
	}
	now := time.Now()
	nextCooling := make(map[string]time.Time, len(state.Cooling))
	nextManual := make(map[string]bool, len(state.Manual))
	for rawKey, until := range state.Cooling {
		if !until.After(now) {
			continue
		}
		key, normErr := NormalizeEgressBlock(rawKey)
		if normErr != nil {
			if p.logger != nil {
				p.logger.Warnf("Skipping unreadable WARP blacklist entry %q in %s: %v", rawKey, path, normErr)
			}
			continue
		}
		nextCooling[key] = until
		if state.Manual[rawKey] || state.Manual[key] {
			nextManual[key] = true
		}
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return 0, 0, ErrClosed
	}
	for key := range nextCooling {
		if _, ok := p.cooling[key]; !ok {
			added++
		}
	}
	for key := range p.cooling {
		if _, ok := nextCooling[key]; !ok {
			removed++
		}
	}
	p.cooling = nextCooling
	p.manual = nextManual
	p.notifyLocked()
	p.mu.Unlock()
	if p.logger != nil {
		p.logger.Infof("OpenCode WARP blacklist reloaded from %s: %d added, %d removed", path, added, removed)
	}
	return added, removed, nil
}

// evictLocked retires the idle exits serving one banned block. A leased exit is
// left alone: its request is already on the wire and closing it would truncate
// a stream, so the ban catches it at the next rotation. The caller holds p.mu
// and is responsible for notifyLocked.
func (p *Pool) evictLocked(key string) int {
	kept := p.active[:0]
	evicted := 0
	for _, candidate := range p.active {
		if candidate.egressKey() == key && candidate.idle() {
			candidate.retire()
			evicted++
			continue
		}
		kept = append(kept, candidate)
	}
	p.active = kept
	if p.next >= len(p.active) {
		p.next = 0
	}
	return evicted
}

// countActiveLocked reports how many live exits serve one block. The caller
// holds p.mu.
func countActiveLocked(p *Pool, key string) int {
	count := 0
	for _, candidate := range p.active {
		if candidate.egressKey() == key {
			count++
		}
	}
	return count
}
