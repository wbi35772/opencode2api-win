package warp

import (
	"context"
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func blacklistPool(statePath string) *Pool {
	return &Pool{
		ctx:       context.Background(),
		statePath: statePath,
		reserved:  make(map[string]bool),
		cooling:   make(map[string]time.Time),
		manual:    make(map[string]bool),
		changed:   make(chan struct{}),
	}
}

func TestNormalizeEgressBlock(t *testing.T) {
	good := map[string]string{
		"1.2.3.4":                  "1.2.3.4/32",
		"1.2.3.4/32":               "1.2.3.4/32",
		" 2a09:bac5:952c:3af::1 ":  "2a09:bac5:952c:3af::/64",
		"2a09:bac5:952c:3af::/64":  "2a09:bac5:952c:3af::/64",
		"2a09:bac5:952c:3af::1/64": "2a09:bac5:952c:3af::/64",
		"2606:4700:4700::1111/64":  "2606:4700:4700::/64",
	}
	for input, want := range good {
		got, err := NormalizeEgressBlock(input)
		if err != nil {
			t.Fatalf("NormalizeEgressBlock(%q) errored: %v", input, err)
		}
		if got != want {
			t.Fatalf("NormalizeEgressBlock(%q) = %q want %q", input, got, want)
		}
	}
	for _, input := range []string{"", "   ", "not-an-ip", "1.2.3.0/24", "2a09::/48", "1.2.3.4/33", "::/0"} {
		if got, err := NormalizeEgressBlock(input); err == nil {
			t.Fatalf("NormalizeEgressBlock(%q) accepted as %q", input, got)
		}
	}
}

func TestBanEgressPersistsAndLists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "warp-state.json")
	pool := blacklistPool(path)
	until := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)

	entry, err := pool.BanEgress("1.2.3.4", until)
	if err != nil {
		t.Fatalf("BanEgress: %v", err)
	}
	if entry.Subnet != "1.2.3.4/32" || !entry.Manual {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if entry.RemainingSeconds < 7100 || entry.RemainingSeconds > 7200 {
		t.Fatalf("remaining = %d want ~7200", entry.RemainingSeconds)
	}

	listed := pool.Blacklist()
	if len(listed) != 1 || listed[0].Subnet != "1.2.3.4/32" || !listed[0].Manual {
		t.Fatalf("Blacklist = %+v", listed)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("state file not written: %v", err)
	}
	var state coolingState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("state file is not JSON: %v", err)
	}
	if !state.Manual["1.2.3.4/32"] {
		t.Fatalf("manual flag not persisted: %s", raw)
	}
	if got := state.Cooling["1.2.3.4/32"]; !got.Equal(until) {
		t.Fatalf("persisted expiry = %v want %v", got, until)
	}

	released, err := pool.ReleaseEgress("1.2.3.4")
	if err != nil || !released {
		t.Fatalf("ReleaseEgress = %v, %v", released, err)
	}
	if listed := pool.Blacklist(); len(listed) != 0 {
		t.Fatalf("block still listed after release: %+v", listed)
	}
	if released, err := pool.ReleaseEgress("1.2.3.4"); err != nil || released {
		t.Fatalf("second release = %v, %v want false, nil", released, err)
	}
}

func TestBanEgressNeverShortensLongerPenalty(t *testing.T) {
	pool := blacklistPool("")
	key := "1.2.3.4/32"
	long := time.Now().Add(20 * time.Hour)
	pool.cooling[key] = long

	entry, err := pool.BanEgress("1.2.3.4", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("BanEgress: %v", err)
	}
	if !entry.Until.Equal(long.UTC()) {
		t.Fatalf("ban shortened the penalty: %v want %v", entry.Until, long.UTC())
	}
	if until := pool.cooling[key]; !until.Equal(long) {
		t.Fatalf("cooling = %v want %v", until, long)
	}
}

func TestBanEgressRejectsExpiredUnity(t *testing.T) {
	pool := blacklistPool("")
	if _, err := pool.BanEgress("1.2.3.4", time.Now().Add(-time.Hour)); err == nil {
		t.Fatal("BanEgress accepted an expiry in the past")
	}
}

func TestBanEgressRetiresIdleExitOnBlock(t *testing.T) {
	pool := blacklistPool("")
	victim := &exit{id: "victim", egress: netip.MustParseAddr("2606:4700:4700::1"), stack: StackIPv6}
	other := &exit{id: "other", egress: netip.MustParseAddr("2606:4700:4701::1"), stack: StackIPv6}
	pool.active = []*exit{victim, other}

	if _, err := pool.BanEgress("2606:4700:4700::/64", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("BanEgress: %v", err)
	}
	if len(pool.active) != 1 || pool.active[0] != other {
		t.Fatalf("idle exit on the banned block was not retired: %d active", len(pool.active))
	}
	if !victim.retired {
		t.Fatal("banned exit is not retired")
	}
}

func TestReloadBlacklistFollowsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "warp-state.json")
	until := time.Now().Add(6 * time.Hour).UTC().Truncate(time.Second)
	state := coolingState{
		Cooling: map[string]time.Time{
			"2a09:bac5:952c:3af::/64": until,
			"1.2.3.4/32":              until,
		},
		Manual:        map[string]bool{"2a09:bac5:952c:3af::/64": true},
		FalsePositive: map[string]bool{},
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}

	pool := blacklistPool(path)
	added, removed, err := pool.ReloadBlacklist()
	if err != nil {
		t.Fatalf("ReloadBlacklist: %v", err)
	}
	if added != 2 || removed != 0 {
		t.Fatalf("added=%d removed=%d want 2, 0", added, removed)
	}
	listed := pool.Blacklist()
	if len(listed) != 2 {
		t.Fatalf("Blacklist = %+v", listed)
	}
	for _, entry := range listed {
		if entry.Subnet == "2a09:bac5:952c:3af::/64" && !entry.Manual {
			t.Fatalf("manual flag lost on reload: %+v", entry)
		}
	}

	// An operator deleting a line and reloading drops it from memory too.
	state.Cooling = map[string]time.Time{"2a09:bac5:952c:3af::/64": until}
	state.Manual = map[string]bool{"2a09:bac5:952c:3af::/64": true}
	raw, err = json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}
	added, removed, err = pool.ReloadBlacklist()
	if err != nil {
		t.Fatalf("ReloadBlacklist: %v", err)
	}
	if added != 0 || removed != 1 {
		t.Fatalf("added=%d removed=%d want 0, 1", added, removed)
	}
	if listed := pool.Blacklist(); len(listed) != 1 || listed[0].Subnet != "2a09:bac5:952c:3af::/64" {
		t.Fatalf("Blacklist after delete = %+v", listed)
	}
}

func TestReloadBlacklistDropsExpiredAndJunk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "warp-state.json")
	state := coolingState{
		Cooling: map[string]time.Time{
			"1.2.3.4/32": time.Now().Add(-time.Minute),
			"2a09::/48":  time.Now().Add(time.Hour),
			"5.6.7.8/32": time.Now().Add(time.Hour),
		},
		Manual:        map[string]bool{},
		FalsePositive: map[string]bool{},
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}
	pool := blacklistPool(path)
	added, _, err := pool.ReloadBlacklist()
	if err != nil {
		t.Fatalf("ReloadBlacklist: %v", err)
	}
	if added != 1 {
		t.Fatalf("added=%d want 1 (expired and wider-than-/64 entries skipped)", added)
	}
	if listed := pool.Blacklist(); len(listed) != 1 || listed[0].Subnet != "5.6.7.8/32" {
		t.Fatalf("Blacklist = %+v", listed)
	}
}

func TestReloadBlacklistNeedsStatePath(t *testing.T) {
	pool := blacklistPool("")
	if _, _, err := pool.ReloadBlacklist(); err == nil {
		t.Fatal("ReloadBlacklist accepted an empty state path")
	}
}
