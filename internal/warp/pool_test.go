package warp

import (
	"context"
	"fmt"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

// A 429 must not leave the conversation pinned to the spent exit: the retry has
// to draw from the remaining ring instead of repeating the same /64.
func TestUnbindDropsConversationPin(t *testing.T) {
	first := testExit()
	pool := testPool("", first)
	pool.affinity = map[string]*exit{"ses_test": first}

	pool.Unbind("ses_test")
	if _, bound := pool.affinity["ses_test"]; bound {
		t.Fatal("Unbind kept the conversation pin")
	}

	pool.Unbind("")
}

// A fresh WARP block is often already spent, so AcquireFor must hand out an
// exit with a success history before an untested one.
func TestAcquireForPrefersProvenExit(t *testing.T) {
	untested := testExit()
	proven := testExit()
	proven.id = "proven"
	proven.egress = netip.MustParseAddr("2606:4700:4701::1")
	pool := testPool("", untested)
	pool.active = []*exit{untested, proven}
	pool.next = 0
	pool.noteSuccess(proven)

	lease, err := pool.AcquireFor(context.Background(), "")
	if err != nil {
		t.Fatalf("AcquireFor: %v", err)
	}
	if lease.exit != proven {
		t.Fatalf("leased untested exit %q; want the proven one", lease.exit.id)
	}
	lease.Release()
}

// A saturated spent-block set makes the first few registrations land on /64s
// that are already cooling. newPool must keep trying inside the caller's
// deadline instead of failing startup after createExit's fixed attempt budget.
func TestNewPoolRetriesDuplicateFirstEgress(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var calls atomic.Int32
	create := func(context.Context) (*exit, error) {
		n := calls.Add(1)
		if n <= 6 {
			return nil, fmt.Errorf("%w: 2a09:bac5:c82c:2832::/64", errDuplicateEgress)
		}
		return &exit{
			id:     fmt.Sprintf("fresh-%d", n),
			egress: netip.MustParseAddr("2a09:bac5:c82f:32a0::50b:81"),
			stack:  StackIPv6,
		}, nil
	}

	pool, err := newPool(ctx, nil, 1, "", create)
	if err != nil {
		t.Fatalf("newPool: %v", err)
	}
	defer pool.Close()

	if len(pool.active) != 1 {
		t.Fatalf("active exits = %d, want 1", len(pool.active))
	}
	if got := pool.active[0].egressKey(); got != "2a09:bac5:c82f:32a0::/64" {
		t.Fatalf("first exit egress = %s, want 2a09:bac5:c82f:32a0::/64", got)
	}
	if got := calls.Load(); got < 7 {
		t.Fatalf("create calls = %d, want at least 7", got)
	}
}

// A freshly registered block that already exhausted the free tier must be
// parked before it joins the ring: the precheck pays one minimal completion,
// not a live request's first attempt.
func TestCreateExitQuarantinesSpentEgress(t *testing.T) {
	spent := testExit()
	spent.id = "spent"
	fresh := testExit()
	fresh.id = "fresh"
	fresh.egress = netip.MustParseAddr("2606:4700:4701::1")

	queue := []*exit{spent, fresh}
	pool := &Pool{
		ctx:      context.Background(),
		size:     4,
		reserved: make(map[string]bool),
		cooling:  make(map[string]time.Time),
		changed:  make(chan struct{}),
		newExit: func(context.Context) (*exit, error) {
			if len(queue) == 0 {
				return nil, fmt.Errorf("no candidates left")
			}
			next := queue[0]
			queue = queue[1:]
			return next, nil
		},
	}
	var calls atomic.Int32
	pool.verify = func(context.Context, DialFunc) error {
		if calls.Add(1) == 1 {
			return ErrEgressSpent
		}
		return nil
	}

	got, err := pool.createExit(context.Background())
	if err != nil {
		t.Fatalf("createExit: %v", err)
	}
	if got != fresh {
		t.Fatalf("admitted %q; want the unspent exit", got.id)
	}
	if got := pool.activeIndexLocked(fresh); got != -1 {
		t.Fatalf("createExit must not mutate the ring (index %d)", got)
	}
	pool.mu.Lock()
	until, parked := pool.cooling[spent.egressKey()]
	_, reserved := pool.reserved[spent.egressKey()]
	pool.mu.Unlock()
	if !parked || !until.After(time.Now().Add(23*time.Hour)) {
		t.Fatalf("spent block not quarantined for the quota cooldown: %v (parked=%t)", until, parked)
	}
	if reserved {
		t.Fatal("spent block still holds its reservation")
	}
}
