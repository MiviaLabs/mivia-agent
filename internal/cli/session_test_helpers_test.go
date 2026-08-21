package cli

import (
	"context"
	"io"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

func newTestSessionForModel(model string) *chat.Session {
	return chat.NewSession(&config.Resolved{Model: model}, nil)
}

// createManagedWorktreeWithInstance is a package-local copy of
// internal/legacytui's helper of the same name: the TUI worktree-dialog
// creation flow owns the real implementation now, but cli-only tests still
// need a worktree plus its instance metadata for fixture setup.
func createManagedWorktreeWithInstance(root, name, baseRef, branchPrefix string) (*vcs.WorktreeInfo, contextstate.WorktreeInstance, error) {
	store, err := OpenRepositoryContextStore(root)
	if err != nil {
		return nil, contextstate.WorktreeInstance{}, err
	}
	defer store.Close()
	var instance contextstate.WorktreeInstance
	worktree, err := CreateManagedWorktreeInStoreWithInstance(store, root, name, baseRef, branchPrefix, &instance)
	return worktree, instance, err
}

// recoverManagedWorktreeRemovalInfoInStore and reactivateManagedWorktreeForSession
// are package-local copies of internal/legacytui's helpers of the same name
// (worktree_dialog.go): both are thin wrappers over already-exported
// worktree-lifecycle functions, duplicated here for cli-only tests.
func recoverManagedWorktreeRemovalInfoInStore(store *storage.SQLite, root string, info contextstate.WorktreeInstanceInfo, branchPrefix string) error {
	lock, err := LockWorktreeLifecycle(root, info.Instance.Worktree)
	if err != nil {
		return err
	}
	defer lock.Close()
	return RecoverManagedWorktreeRemovalInfoInStoreLocked(store, root, info, branchPrefix, lock.File())
}

func reactivateManagedWorktreeForSession(sess *chat.Session, root string, instance contextstate.WorktreeInstance) error {
	if store, ok := sess.ContextStore().(*storage.SQLite); ok && store != nil {
		return ReactivateManagedWorktreeInStore(store, root, instance)
	}
	return ReactivateManagedWorktree(root, instance)
}

// stubAgentCompleter is a package-local copy of internal/legacytui's helper
// of the same name (agent_switch_test.go): a minimal provider.Completer that
// always answers "ok", used where the tests below only need a wired session,
// not a specific model response.
type stubAgentCompleter struct{}

func (stubAgentCompleter) Name() string { return "stub" }
func (stubAgentCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "ok", nil
}
func (stubAgentCompleter) ChatStream(_ context.Context, _ provider.Request, w io.Writer) (string, error) {
	_, _ = io.WriteString(w, "ok")
	return "ok", nil
}
func (stubAgentCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	return &provider.Response{Content: "ok"}, nil
}

// stubWorkspaceRestart satisfies workspaceRestartError without importing the
// concrete WorkspaceRestart type. Tests that call validateWorkspaceRestart
// directly use this instead of a WorkspaceRestart struct literal, so they
// stay buildable once WorkspaceRestart moves to package legacytui.
type stubWorkspaceRestart struct {
	dir, resumeSessionName string
	wt                     contextstate.WorktreeInstance
}

func (s stubWorkspaceRestart) Error() string { return "restart chat in workspace " + s.dir }

func (s stubWorkspaceRestart) WorkspaceRestartInfo() (string, string, contextstate.WorktreeInstance) {
	return s.dir, s.resumeSessionName, s.wt
}
