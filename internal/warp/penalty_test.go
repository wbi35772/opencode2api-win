package warp

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"path/filepath"
	"testing"
	"time"
)

func testExit() *exit {
	return &exit{id: "test", egress: netip.MustParseAddr("2606:4700:4700::1"), stack: StackIPv6}
}

func testPool(statePath string, first *exit) *Pool {
	return &Pool{
		ctx:       context.Background(),
		size:      0,
		statePath: statePath,
		active:    []*exit{first},
		reserved:  make(map[string]bool),
		cooling:   make(map[string]time.Time),
		changed:   make(chan struct{}),
		newExit: func(context.Context) (*exit, error) {
			return nil, errors.New("registrar unavailable")
		},
	}
}

func TestQuotaPenaltyIsNotRestored(t *testing.T) {
	blocked := testExit()
	pool := testPool("", blocked)
	pool.penalize(blocked, "HTTP 429", 0, penaltyQuota)
	pool.wait.Wait()

	pool.mu.Lock()
	defer pool.mu.Unlock()
	if len(pool.active) != 0 {
		t.Fatalf("quota-penalized exit was restored: active=%d", len(pool.active))
	}
	until, parked := pool.cooling[blocked.egressKey()]
	if !parked || until.Before(time.Now().Add(24*time.Hour-time.Minute)) {
		t.Fatalf("expected at least the base cooldown, got %v (parked=%v)", until, parked)
	}
}

func TestTransportPenaltyIsRestored(t *testing.T) {
	broken := testExit()
	pool := testPool("", broken)
	pool.penalize(broken, "dial failed", 0, penaltyTransport)
	pool.wait.Wait()

	pool.mu.Lock()
	defer pool.mu.Unlock()
	if len(pool.active) != 1 || pool.active[0] != broken {
		t.Fatalf("transport-penalized exit was not restored: active=%d", len(pool.active))
	}
	if until, parked := pool.cooling[broken.egressKey()]; parked {
		t.Fatalf("restored transport exit kept a cooldown until %v", until)
	}
	if kind := broken.penaltyKind(); kind != penaltyNone {
		t.Fatalf("restored transport exit kept penalty %d", kind)
	}
}

func TestQuotaCooldownFixed24h(t *testing.T) {
	if got := quotaCooldownFor(1, 0); got != 24*time.Hour {
		t.Fatalf("fixed cooldown = %v want 24h", got)
	}
	if got := quotaCooldownFor(1, 3*time.Hour); got != 24*time.Hour {
		t.Fatalf("quota cooldown stays fixed at 24h, got %v", got)
	}
	if got := quotaCooldownFor(1, 48*time.Hour); got != 24*time.Hour {
		t.Fatalf("Retry-After capped at 24h, got %v", got)
	}
}

func TestRateLimitCooldownStaysShort(t *testing.T) {
	if got := rateLimitCooldownFor(0); got != time.Minute {
		t.Fatalf("default rate limit cooldown = %v", got)
	}
	if got := rateLimitCooldownFor(10 * time.Minute); got != 5*time.Minute {
		t.Fatalf("rate limit cap = %v", got)
	}
}

func TestClassifyPenalty(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   penaltyKind
	}{
		{429, `{"error":{"type":"insufficient_quota"}}`, penaltyQuota},
		{429, `{"error":{"type":"rate_limit_error"}}`, penaltyRateLimit},
		{429, ``, penaltyQuota},
		{403, `network anomaly`, penaltyQuota},
		{429, `free usage limit reached`, penaltyQuota},
	}
	for _, test := range cases {
		if got := classifyPenalty(test.status, []byte(test.body)); got != test.want {
			t.Fatalf("status=%d body=%q: got %d want %d", test.status, test.body, got, test.want)
		}
	}
}

func TestPenaltyStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "warp-state.json")
	first := testExit()
	pool := testPool(path, first)
	key := first.egressKey()
	until := time.Now().Add(2 * time.Hour).Round(time.Second)
	pool.mu.Lock()
	pool.cooling[key] = until
	pool.mu.Unlock()
	pool.saveCooling()

	cooling, _ := loadPenalties(path)
	if got := cooling[key]; !got.Equal(until) {
		t.Fatalf("cooling round trip = %v want %v", got, until)
	}
	// strikes removed: cooling only persists
}

func TestNoteSuccessNoStrikes(t *testing.T) {
	first := testExit()
	pool := testPool("", first)
	pool.noteSuccess(first)
}

func TestPreloadFor(t *testing.T) {
	cases := []struct {
		size int
		want int
	}{
		{0, DefaultPoolSize},
		{1, 1},
		{2, 2},
		{4, 4},
		{8, 8},
		{10, 10},
		{12, 12},
		{16, maxPreload},
		{32, maxPreload},
	}
	for _, test := range cases {
		if got := preloadFor(test.size); got != test.want {
			t.Fatalf("preloadFor(%d) = %d, want %d", test.size, got, test.want)
		}
	}
}

func TestPenalizeEgressRetiresHeldExit(t *testing.T) {
	held := testExit()
	pool := testPool("", held)
	if !pool.PenalizeEgress(held.egressKey(), http.StatusTooManyRequests, []byte(`{"error":{"type":"insufficient_quota"}}`), 0) {
		t.Fatal("PenalizeEgress did not match the active exit")
	}
	pool.wait.Wait()

	pool.mu.Lock()
	defer pool.mu.Unlock()
	if len(pool.active) != 0 {
		t.Fatalf("held exit still active: active=%d", len(pool.active))
	}
	until, parked := pool.cooling[held.egressKey()]
	if !parked || until.Before(time.Now().Add(24*time.Hour-time.Minute)) {
		t.Fatalf("held exit not parked for the quota window: %v (parked=%v)", until, parked)
	}
}

func TestPenalizeEgressIgnoresUnknownKey(t *testing.T) {
	pool := testPool("", testExit())
	if pool.PenalizeEgress("2606:4700:4701::/64", http.StatusTooManyRequests, nil, 0) {
		t.Fatal("PenalizeEgress matched a key that is not active")
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if len(pool.active) != 1 {
		t.Fatalf("active exits changed: active=%d", len(pool.active))
	}
}
