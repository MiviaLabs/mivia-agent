package chat

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

type allSessionsCatalog struct {
	contextstate.Store
	all    []contextstate.SessionCatalogInfo
	scoped []contextstate.SessionCatalogInfo
}

func (c *allSessionsCatalog) EnsureSession(context.Context, contextstate.EnsureSessionRequest) error {
	return nil
}
func (c *allSessionsCatalog) Load(context.Context, contextstate.Principal, string) (contextstate.Snapshot, error) {
	return contextstate.Snapshot{}, nil
}
func (c *allSessionsCatalog) LoadWorktree(context.Context, contextstate.Principal, string, contextstate.WorktreeInstance) (contextstate.Snapshot, error) {
	return contextstate.Snapshot{}, nil
}
func (c *allSessionsCatalog) SaveSession(context.Context, contextstate.Principal, string, []byte, string, string, int, int, int, contextstate.SessionSaveOptions) error {
	return nil
}
func (c *allSessionsCatalog) LoadSession(context.Context, contextstate.Principal, string) ([]byte, contextstate.SessionCatalogInfo, error) {
	return nil, contextstate.SessionCatalogInfo{}, nil
}
func (c *allSessionsCatalog) ListSessions(context.Context, contextstate.Principal) ([]contextstate.SessionCatalogInfo, error) {
	return c.all, nil
}
func (c *allSessionsCatalog) ListWorktreeSessions(context.Context, contextstate.Principal, contextstate.WorktreeInstance) ([]contextstate.SessionCatalogInfo, error) {
	return c.scoped, nil
}
func (c *allSessionsCatalog) DeleteSessionSnapshot(context.Context, contextstate.Principal, string) error {
	return nil
}
func (c *allSessionsCatalog) PruneSessionSnapshots(context.Context, contextstate.Principal, []string) error {
	return nil
}

func TestListAllSessionsIncludesOtherWorktrees(t *testing.T) {
	instance := contextstate.WorktreeInstance{Worktree: "wt-current", ID: "wt_1234567890abcdef"}
	catalog := &allSessionsCatalog{
		all: []contextstate.SessionCatalogInfo{
			{Name: "session-current", Worktree: "wt-current", WorktreeInstance: instance},
			{Name: "session-other", Worktree: "wt-other", WorktreeInstance: contextstate.WorktreeInstance{Worktree: "wt-other", ID: "wt_fedcba0987654321"}},
		},
		scoped: []contextstate.SessionCatalogInfo{{Name: "session-current", Worktree: "wt-current", WorktreeInstance: instance}},
	}
	sess := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{})
	principal, err := contextstate.NewPrincipal("workspace", sess.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.SetContextWorktreeBinding(instance); err != nil {
		t.Fatal(err)
	}
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: catalog},
		Enabled:             true,
	}
	if err := sess.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	if err := sess.SetContextStore(catalog); err != nil {
		t.Fatal(err)
	}
	infos, err := sess.ListAllSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 || infos[1].Name != "session-other" {
		t.Fatalf("ListAllSessions = %+v, want current and other-worktree sessions", infos)
	}
}
