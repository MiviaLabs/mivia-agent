package chat

import (
	"context"
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

type allSessionsCatalog struct {
	contextstate.Store
	all     []contextstate.SessionCatalogInfo
	scoped  []contextstate.SessionCatalogInfo
	listErr error
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
	if c.listErr != nil {
		return nil, c.listErr
	}
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

// TestListAllSessionsPropagatesCatalogError confirms ListAllSessions returns
// the catalog's own ListSessions error verbatim (not swallowed and not
// wrapped into a different message), rather than returning a zero-value
// slice on failure.
func TestListAllSessionsPropagatesCatalogError(t *testing.T) {
	wantErr := errors.New("catalog unavailable")
	catalog := &allSessionsCatalog{listErr: wantErr}
	sess := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{})
	principal, err := contextstate.NewPrincipal("workspace", sess.SessionID, "subject")
	if err != nil {
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
	if !errors.Is(err, wantErr) {
		t.Fatalf("ListAllSessions() error = %v, want %v", err, wantErr)
	}
	if infos != nil {
		t.Fatalf("ListAllSessions() infos = %+v, want nil on error", infos)
	}
}

// TestListSessions_WorktreeScopedRequiresCapableCatalog pins ListSessions'
// own worktree-scoping guard: a bound worktree instance with a catalog
// that does not implement WorktreeSessionCatalog (allSessionsCatalog only
// defines ListWorktreeSessions, not the rest of that interface) must
// refuse rather than silently falling back to the unscoped list.
func TestListSessions_WorktreeScopedRequiresCapableCatalog(t *testing.T) {
	instance := contextstate.WorktreeInstance{Worktree: "wt-current", ID: "wt_1234567890abcdef"}
	catalog := &allSessionsCatalog{}
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
	if _, err := sess.ListSessions(); err == nil {
		t.Fatal("ListSessions accepted a worktree-bound session whose catalog cannot scope by worktree")
	}
}

// TestListSessions_WorktreeScopedPropagatesCatalogError pins ListSessions'
// own error propagation from the scoped ListWorktreeSessions call, and its
// success path scoping to the bound worktree (as opposed to the unscoped
// ListSessions ListAllSessions uses).
func TestListSessions_WorktreeScopedPropagatesCatalogError(t *testing.T) {
	instance := contextstate.WorktreeInstance{Worktree: "wt-current", ID: "wt_1234567890abcdef"}
	catalog := &scopedErrCatalog{err: errors.New("scoped catalog unavailable")}
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
	if _, err := sess.ListSessions(); err == nil {
		t.Fatal("ListSessions hid the scoped catalog's own error")
	}
}

// TestDeleteSession_WorktreeScopedRequiresCapableCatalog and its error-
// propagation sibling mirror the ListSessions pair above for
// DeleteSession's own worktree-scoping branches.
func TestDeleteSession_WorktreeScopedRequiresCapableCatalog(t *testing.T) {
	instance := contextstate.WorktreeInstance{Worktree: "wt-current", ID: "wt_1234567890abcdef"}
	catalog := &allSessionsCatalog{}
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
	if err := sess.DeleteSession("some-session"); err == nil {
		t.Fatal("DeleteSession accepted a worktree-bound session whose catalog cannot scope by worktree")
	}
}

func TestDeleteSession_WorktreeScopedSucceeds(t *testing.T) {
	instance := contextstate.WorktreeInstance{Worktree: "wt-current", ID: "wt_1234567890abcdef"}
	catalog := &scopedErrCatalog{}
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
	if err := sess.DeleteSession("some-session"); err != nil {
		t.Fatalf("DeleteSession scoped call = %v, want nil", err)
	}
	if !catalog.deletedScoped {
		t.Fatal("DeleteSession did not call the scoped DeleteWorktreeSessionSnapshot")
	}
}

// scopedErrCatalog implements the full contextstate.WorktreeSessionCatalog
// surface (unlike allSessionsCatalog, which only defines ListWorktreeSessions
// and so deliberately fails the interface assertion), so ListSessions' and
// DeleteSession's scoped SUCCESS and error-propagation branches can be
// driven independently of the "catalog cannot scope" guard.
type scopedErrCatalog struct {
	contextstate.Store
	err           error
	deletedScoped bool
}

func (c *scopedErrCatalog) EnsureSession(context.Context, contextstate.EnsureSessionRequest) error {
	return nil
}
func (c *scopedErrCatalog) Load(context.Context, contextstate.Principal, string) (contextstate.Snapshot, error) {
	return contextstate.Snapshot{}, nil
}
func (c *scopedErrCatalog) LoadWorktree(context.Context, contextstate.Principal, string, contextstate.WorktreeInstance) (contextstate.Snapshot, error) {
	return contextstate.Snapshot{}, nil
}
func (c *scopedErrCatalog) SaveSession(context.Context, contextstate.Principal, string, []byte, string, string, int, int, int, contextstate.SessionSaveOptions) error {
	return nil
}
func (c *scopedErrCatalog) LoadSession(context.Context, contextstate.Principal, string) ([]byte, contextstate.SessionCatalogInfo, error) {
	return nil, contextstate.SessionCatalogInfo{}, nil
}
func (c *scopedErrCatalog) ListSessions(context.Context, contextstate.Principal) ([]contextstate.SessionCatalogInfo, error) {
	return nil, nil
}
func (c *scopedErrCatalog) ListWorktreeSessions(context.Context, contextstate.Principal, contextstate.WorktreeInstance) ([]contextstate.SessionCatalogInfo, error) {
	if c.err != nil {
		return nil, c.err
	}
	return nil, nil
}
func (c *scopedErrCatalog) DeleteSessionSnapshot(context.Context, contextstate.Principal, string) error {
	return nil
}
func (c *scopedErrCatalog) DeleteWorktreeSessionSnapshot(context.Context, contextstate.Principal, string, contextstate.WorktreeInstance) error {
	c.deletedScoped = true
	return c.err
}
func (c *scopedErrCatalog) PruneSessionSnapshots(context.Context, contextstate.Principal, []string) error {
	return nil
}
func (c *scopedErrCatalog) BeginWorktreeCreation(context.Context, contextstate.Principal, contextstate.WorktreeInstance, string) error {
	return nil
}
func (c *scopedErrCatalog) RegisterWorktreeInstance(context.Context, contextstate.Principal, contextstate.WorktreeInstance, string) error {
	return nil
}
func (c *scopedErrCatalog) AbandonWorktreeCreation(context.Context, contextstate.Principal, contextstate.WorktreeInstance) error {
	return nil
}
func (c *scopedErrCatalog) BeginWorktreeDeletion(context.Context, contextstate.Principal, contextstate.WorktreeInstance) error {
	return nil
}
func (c *scopedErrCatalog) DeleteWorktreeSessions(context.Context, contextstate.Principal, contextstate.WorktreeInstance) (int, error) {
	return 0, nil
}
func (c *scopedErrCatalog) LoadWorktreeSession(context.Context, contextstate.Principal, string, contextstate.WorktreeInstance) ([]byte, contextstate.SessionCatalogInfo, error) {
	return nil, contextstate.SessionCatalogInfo{}, nil
}
func (c *scopedErrCatalog) PruneWorktreeSessionSnapshots(context.Context, contextstate.Principal, []string, contextstate.WorktreeInstance) error {
	return nil
}
