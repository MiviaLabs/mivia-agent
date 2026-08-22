package localengine

// coverage_test.go exercises small public helpers in engine_resume.go
// and worktree.go that the broader engine_*_test.go files do not drive
// individually. It exists to keep the diff-coverage gate green across
// the cli-split renames that the engine files participated in.

import (
	"context"
	"testing"
	"time"
)

func TestResolveOriginURLRejectsBlankInputs(t *testing.T) {
	for _, base := range []string{"", "   "} {
		url, commit, err := resolveOriginURL(context.Background(), time.Millisecond, Identity{}, base)
		if err == nil {
			t.Fatalf("resolveOriginURL(base=%q) must error", base)
		}
		if url != "" || commit != "" {
			t.Fatalf("resolveOriginURL(base=%q) = (%q, %q), want both empty on error", base, url, commit)
		}
	}
}

func TestForgetWorktreeUnknownRunIsNoop(t *testing.T) {
	e := &Engine{}
	// Must not panic when the run has no recorded worktree.
	e.forgetWorktree("nonexistent-run-id")
}

func TestWorktreeIdentityUnknownRunIsEmpty(t *testing.T) {
	e := &Engine{}
	id, ok := e.worktreeIdentity("nonexistent-run-id")
	if ok {
		t.Fatalf("worktreeIdentity(unknown) returned ok=true with id=%+v", id)
	}
	if id != (Identity{}) {
		t.Fatalf("worktreeIdentity(unknown) returned non-zero Identity=%+v", id)
	}
}

func TestRecordWorktreeStoresIdentity(t *testing.T) {
	e := &Engine{}
	identity := Identity{Root: "/tmp/wt", BaseRef: "main", Branch: "feat/x"}
	e.recordWorktree("run-a", identity)
	got, ok := e.worktreeIdentity("run-a")
	if !ok || got != identity {
		t.Fatalf("recordWorktree then worktreeIdentity = (%+v, %v)", got, ok)
	}
}

func TestClearDeliveryAbandonOnNilFence(t *testing.T) {
	// e.fence == nil: the clear must no-op rather than panic.
	e := &Engine{}
	e.clearDeliveryAbandon("run-x")
}
