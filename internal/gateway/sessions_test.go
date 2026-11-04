package gateway

import (
	"strings"
	"testing"

	"opencode2api/internal/identity"
)

func TestSessionGuardResolveAndRotate(t *testing.T) {
	g := newSessionGuard()
	base := "ses_0f2c1b9a4d7eConversationAlpha"
	if got := g.Resolve(base); got != base {
		t.Fatalf("generation zero must keep the derived id, got %q", got)
	}
	if gen := g.Generation(base); gen != 0 {
		t.Fatalf("fresh conversation generation = %d, want 0", gen)
	}

	first := g.Rotate(base)
	if first == base || !strings.HasPrefix(first, "ses_") {
		t.Fatalf("rotated id %q is not a fresh canonical session", first)
	}
	if got := g.Resolve(base); got != first {
		t.Fatalf("Resolve after Rotate = %q, want %q", got, first)
	}
	if gen := g.Generation(base); gen != 1 {
		t.Fatalf("generation after one rotation = %d, want 1", gen)
	}
	if again := g.Rotate(base); again == first {
		t.Fatalf("second rotation reused the first replacement: %q", again)
	}
}

func TestSessionGuardResolveIDs(t *testing.T) {
	g := newSessionGuard()
	ids := identity.RequestIDs{Session: "parent-conversation", ChildSession: "parent-conversation"}
	rotated := g.RotateIDs(ids)
	if rotated.Session == ids.Session {
		t.Fatalf("RotateIDs did not change the session")
	}
	if rotated.ChildSession != rotated.Session {
		t.Fatalf("child that mirrors the parent must follow it: session=%q child=%q", rotated.Session, rotated.ChildSession)
	}
	if got := g.ResolveIDs(ids); got.Session != rotated.Session || got.ChildSession != rotated.ChildSession {
		t.Fatalf("ResolveIDs after rotation = %+v, want %+v", got, rotated)
	}

	other := identity.RequestIDs{Session: "root-session", ChildSession: "child-session"}
	rotated = g.RotateIDs(other)
	if rotated.Session == other.Session || rotated.ChildSession == other.ChildSession {
		t.Fatalf("both ids must rotate independently: %+v", rotated)
	}
	if rotated.Session == rotated.ChildSession {
		t.Fatalf("independent parent and child collapsed into one id")
	}
	if got := g.ResolveIDs(other); got.Session != rotated.Session || got.ChildSession != rotated.ChildSession {
		t.Fatalf("ResolveIDs for independent ids = %+v, want %+v", got, rotated)
	}
}

func TestSessionGuardEmpty(t *testing.T) {
	g := newSessionGuard()
	if got := g.Rotate(""); got != "" {
		t.Fatalf("empty session rotated to %q", got)
	}
	ids := identity.RequestIDs{Session: "only-parent"}
	if got := g.RotateIDs(ids); got.Session == ids.Session || got.ChildSession != "" {
		t.Fatalf("RotateIDs without a child = %+v", got)
	}
}
