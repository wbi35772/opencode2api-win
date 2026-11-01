package warp

import (
	"context"
	"errors"
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
		strikes:   make(map[string]int),
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
	if !parked || until.Before(time.Now().Add(25*time.Minute)) {
		t.Fatalf("expected at least the base cooldown, got %v (parked=%v)", until, parked)
	}
	if got := pool.strikes[blocked.egressKey()]; got != 1 {
		t.Fatalf("expected one quota strike, got %d", got)
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

func TestQuotaCooldownEscalates(t *testing.T) {
	cases := []struct {
		strikes int
		want    time.Duration
	}{
		{1, 30 * time.Minute},
		{2, 2 * time.Hour},
		{3, 8 * time.Hour},
		{4, 24 * time.Hour},
		{8, 24 * time.Hour},
	}
	for _, test := range cases {
		if got := quotaCooldownFor(test.strikes, 0); got != test.want {
			t.Fatalf("strikes=%d: got %v want %v", test.strikes, got, test.want)
		}
	}
	if got := quotaCooldownFor(1, 3*time.Hour); got != 3*time.Hour {
		t.Fatalf("Retry-After should win, got %v", got)
	}
	if got := quotaCooldownFor(1, 48*time.Hour); got != 24*time.Hour {
		t.Fatalf("Retry-After should cap at 24h, got %v", got)
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
	pool.strikes[key] = 3
	pool.mu.Unlock()
	pool.saveCooling()

	cooling, strikes := loadPenalties(path)
	if got := cooling[key]; !got.Equal(until) {
		t.Fatalf("cooling round trip = %v want %v", got, until)
	}
	if got := strikes[key]; got != 3 {
		t.Fatalf("strikes round trip = %d want 3", got)
	}
}

func TestSuccessClearsStrikes(t *testing.T) {
	first := testExit()
	pool := testPool("", first)
	key := first.egressKey()
	pool.strikes[key] = 2
	pool.noteSuccess(first)
	if _, exists := pool.strikes[key]; exists {
		t.Fatal("success did not clear the quota strike")
	}
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
		{10, maxPreload},
		{32, maxPreload},
	}
	for _, test := range cases {
		if got := preloadFor(test.size); got != test.want {
			t.Fatalf("preloadFor(%d) = %d, want %d", test.size, got, test.want)
		}
	}
}
