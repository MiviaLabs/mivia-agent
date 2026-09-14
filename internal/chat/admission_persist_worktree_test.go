package chat

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	contextmgr "github.com/MiviaLabs/mivia-agent/internal/context/manager"
	"github.com/MiviaLabs/mivia-agent/internal/context/state"
)

// plainAdmissionCatalog implements only state.SessionAdmissionCatalog,
// deliberately not WorktreeAdmissionCatalog, so saveAdmission/loadAdmission's
// own scoped-catalog guard fails for a worktree-bound session.
type plainAdmissionCatalog struct {
	heartbeatFakeStore
}

func (*plainAdmissionCatalog) SaveSessionAdmission(context.Context, state.Principal, string, state.SessionAdmission) error {
	return nil
}
func (*plainAdmissionCatalog) LoadSessionAdmission(context.Context, state.Principal, string) (state.SessionAdmission, error) {
	return state.SessionAdmission{}, nil
}

// TestSaveLoadAdmission_WorktreeBoundRequiresScopedCatalog pins both
// saveAdmission and loadAdmission's own "catalog cannot scope by
// worktree" guard.
func TestSaveLoadAdmission_WorktreeBoundRequiresScopedCatalog(t *testing.T) {
	res := &config.Resolved{ProviderName: "fake", Model: "model"}
	sess := NewSession(res, &fakeCompleter{out: "answer"})
	principal, err := state.NewPrincipal("workspace", sess.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}
	store := &plainAdmissionCatalog{}
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
	sess.mu.Lock()
	sess.contextWorktree = state.WorktreeInstance{Worktree: "wt", ID: "wt_1111111111111111"}
	sess.mu.Unlock()

	sess.admissionAgent = "agent-x"
	if err := sess.persistAdmission("agent-x"); err != state.ErrWorktreeDeleted {
		t.Fatalf("persistAdmission err = %v, want ErrWorktreeDeleted", err)
	}
	if _, err := sess.loadAdmission("agent-x"); err != state.ErrWorktreeDeleted {
		t.Fatalf("loadAdmission err = %v, want ErrWorktreeDeleted", err)
	}
}
