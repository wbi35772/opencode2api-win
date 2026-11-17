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

func TestSessionGuardProjectRotation(t *testing.T) {
	g := newSessionGuard()
	base := "0f2c1b9a4d7eConversationAlpha"
	if got := g.ResolveProject(base); got != base {
		t.Fatalf("generation zero must keep the client project, got %q", got)
	}
	if gen := g.ProjectGeneration(base); gen != 0 {
		t.Fatalf("fresh project generation = %d, want 0", gen)
	}

	first := g.RotateProject(base)
	if first == base || len(first) != 40 {
		t.Fatalf("rotated project %q must be a 40-hex replacement, not the spent bucket", first)
	}
	if got := g.ResolveProject(base); got != first {
		t.Fatalf("ResolveProject after rotation = %q, want %q", got, first)
	}
	if gen := g.ProjectGeneration(base); gen != 1 {
		t.Fatalf("project generation after one rotation = %d, want 1", gen)
	}
	second := g.RotateProject(base)
	if second == first {
		t.Fatalf("second rotation reused the first replacement: %q", second)
	}
}

func TestSessionGuardResolveIDsCarriesProject(t *testing.T) {
	g := newSessionGuard()
	ids := identity.RequestIDs{Session: "parent-conversation", Project: "project" + "\x00" + "must-stay-verbatim"}
	resolved := g.ResolveIDs(ids)
	if resolved.Project != ids.Project {
		t.Fatalf("generation zero must pass the client project through, got %q", resolved.Project)
	}
	g.RotateProject(ids.Project)
	resolved = g.ResolveIDs(ids)
	if resolved.Project == ids.Project {
		t.Fatalf("after a project rotation ResolveIDs must return the replacement, got the spent bucket %q", resolved.Project)
	}
	if len(resolved.Project) != 40 {
		t.Fatalf("rotated project %q must be 40-hex", resolved.Project)
	}
}

func TestIdentityCanonicalProjectID(t *testing.T) {
	a := identity.CanonicalProjectID("prj\x00seed-a")
	b := identity.CanonicalProjectID("prj\x00seed-b")
	if a == b {
		t.Fatalf("distinct seeds must not derive the same project id")
	}
	if len(a) != 40 {
		t.Fatalf("canonical project id %q must be 40-hex like the official CLI, got length %d", a, len(a))
	}
	for _, r := range a {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			t.Fatalf("canonical project id %q contains non-lowercase-hex rune %q", a, r)
		}
	}
	if got := identity.CanonicalProjectID("prj\x00seed-a"); got != a {
		t.Fatalf("canonical project id must be stable within a process: %q then %q", a, got)
	}
}
