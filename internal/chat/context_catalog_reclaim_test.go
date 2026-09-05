package chat

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// TestReclaimContextSessionRequiresAReclaimerCapableStore covers
// reclaimContextSession's two "store does not support resuming" branches:
// heartbeatFakeStore implements the base contextstate.Store plus
// SessionLeaseRenewer, but neither SessionReclaimer nor
// WorktreeSessionReclaimer, matching a store that predates that capability.
func TestReclaimContextSessionRequiresAReclaimerCapableStore(t *testing.T) {
	res := &config.Resolved{ProviderName: "fake", Model: "model", Models: []string{"model"}}
	sess := NewSession(res, &fakeCompleter{out: "answer"})

	principal, err := contextstate.NewPrincipal("workspace", sess.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}
	store := &heartbeatFakeStore{}
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: store},
		Enabled:             true,
	}
	if err := sess.SetContextManager(manager, principal); err != nil {
		t.Fatalf("SetContextManager: %v", err)
	}
	if err := sess.SetContextStore(store); err != nil {
		t.Fatalf("SetContextStore: %v", err)
	}

	t.Run("plain session", func(t *testing.T) {
		if _, err := sess.reclaimContextSession("some-session"); err == nil {
			t.Fatal("reclaimContextSession accepted a store with no SessionReclaimer support")
		}
	})

	t.Run("worktree-bound session", func(t *testing.T) {
		sess.mu.Lock()
		sess.contextWorktree = contextstate.WorktreeInstance{Worktree: "wt", ID: "wt_1111111111111111"}
		sess.mu.Unlock()
		if _, err := sess.reclaimContextSession("some-session"); err == nil {
			t.Fatal("reclaimContextSession accepted a store with no WorktreeSessionReclaimer support")
		}
	})
}
