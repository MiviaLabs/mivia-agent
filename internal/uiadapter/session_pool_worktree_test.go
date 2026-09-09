package uiadapter

// Coverage for session_pool_worktree.go's own error/branch paths not
// already exercised by the other pool test files in this package:
// CreateFreshInDir's two guards (no config, drained pool), the
// nil-session skip in contextStoreLocked, and storedRouteLocked's
// swallowed-vs-propagated error branches (no repository above cwd vs a
// real WorktreeSessionBinding failure), reached through
// resolveEntryRouteLocked and GetOrCreateInDir exactly as production
// callers reach them.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// TestCreateFreshInDir_NilConfigErrors covers the "no config provided"
// guard (lines 26-27): a pool built without a Resolved config must refuse
// to mint a fresh entry rather than panic on a nil p.res later in
// newEntrySessionLocked/wireEntryLocked.
func TestCreateFreshInDir_NilConfigErrors(t *testing.T) {
	pool := NewSessionPool(nil, nil, nil, false)

	conv, err := pool.CreateFreshInDir(nil, "")
	if conv != nil {
		t.Fatalf("expected no conversation, got %v", conv)
	}
	if err == nil || !strings.Contains(err.Error(), "no config provided") {
		t.Fatalf("err = %v, want \"no config provided\"", err)
	}
}

// TestCreateFreshInDir_DrainedPoolRefuses covers CreateFreshInDir's own
// refuseIfDrainedLocked call (lines 39-40): once ReleaseLeases has drained
// the pool, a fresh entry built afterwards must be refused instead of
// being published with a lease heartbeat nothing will ever stop (see the
// refuseIfDrainedLocked doc comment).
func TestCreateFreshInDir_DrainedPoolRefuses(t *testing.T) {
	res := &config.Resolved{Model: "test-model"}
	pool := NewSessionPool(nil, res, nil, false)
	pool.ReleaseLeases(context.Background())

	conv, err := pool.CreateFreshInDir(nil, "")
	if conv != nil {
		t.Fatalf("expected no conversation from a drained pool, got %v", conv)
	}
	if err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("err = %v, want \"session pool is shutting down\"", err)
	}
}

// TestContextStoreLocked_SkipsNilSessionEntries covers the nil-guard
// (line 220) in contextStoreLocked's scan of p.sessions. No production
// path ever stores a nil session (every insertion in this file assigns a
// freshly built *chat.Session first), so this drives the unexported
// helper directly rather than contorting a public-API path to smuggle a
// nil entry into the map.
func TestContextStoreLocked_SkipsNilSessionEntries(t *testing.T) {
	res := &config.Resolved{Model: "test-model"}
	pool := NewSessionPool(nil, res, nil, false)

	pool.mu.Lock()
	pool.sessions["ghost"] = nil
	store := pool.contextStoreLocked()
	pool.mu.Unlock()

	if store != nil {
		t.Fatalf("contextStoreLocked() = %v, want nil (only a nil entry in the pool)", store)
	}
}

// TestGetOrCreateInDir_StoredRouteBindingFailurePropagates covers
// storedRouteLocked's real WorktreeSessionBinding error branch
// (lines 250-251) and its propagation through resolveEntryRouteLocked
// (lines 296-297) and GetOrCreateInDir (lines 124-125). A session id
// containing "/" fails storage's validateSessionCatalogName - the
// simplest genuine WorktreeSessionBinding failure - and must surface as
// a wrapped "resolve worktree binding" error rather than a bind panic or
// a silently empty route.
func TestGetOrCreateInDir_StoredRouteBindingFailurePropagates(t *testing.T) {
	res := &config.Resolved{ProviderName: "fake", Model: "m1"}
	store := approvalTestStore(t)
	seed := contextBoundSession(t, res, store, "seed")

	pool := NewSessionPool(seed, res, nil, false)

	conv, err := pool.GetOrCreateInDir("bad/id", nil, "")
	if conv != nil {
		t.Fatalf("expected no conversation, got %v", conv)
	}
	if err == nil {
		t.Fatal("expected an error for a session id storage rejects")
	}
	if !strings.Contains(err.Error(), `resolve worktree binding for session "bad/id"`) {
		t.Fatalf("err = %v, want it wrapped as a worktree-binding resolution failure", err)
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("err = %v, want the underlying validateSessionCatalogName cause", err)
	}
}

// TestGetOrCreateInDir_NoRepositoryFallsBackToPlainSession covers
// storedRouteLocked's swallowed worktreeroute.Root error (lines 242-243):
// running outside any git repository must NOT surface a route-resolution
// error - it falls back to the plain, unbound id exactly like a session
// with no context store at all. The eventual "session not found" from
// Load (the id was never saved) proves the plain path ran; a
// route-resolution error instead would mean Root's failure leaked out of
// storedRouteLocked despite its own "not a repository: nothing can be
// worktree-bound" contract.
func TestGetOrCreateInDir_NoRepositoryFallsBackToPlainSession(t *testing.T) {
	res := &config.Resolved{ProviderName: "fake", Model: "m1"}
	store := approvalTestStore(t)
	seed := contextBoundSession(t, res, store, "seed")

	pool := NewSessionPool(seed, res, nil, false)

	t.Chdir(t.TempDir()) // no git repository anywhere above cwd

	conv, err := pool.GetOrCreateInDir("never-saved-id", nil, "")
	if conv != nil {
		t.Fatalf("expected no conversation for an id nothing ever saved, got %v", conv)
	}
	if err == nil {
		t.Fatal("expected Load to fail for an id nothing ever saved")
	}
	if strings.Contains(err.Error(), "resolve repository root") {
		t.Fatalf("err = %v, want Root's not-a-repository failure swallowed, not propagated", err)
	}
	if !errors.Is(err, contextstate.ErrSessionNotFound) {
		t.Fatalf("err = %v, want contextstate.ErrSessionNotFound (proves the plain load path ran)", err)
	}
}
