package cli

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

func TestWorktreeDialogCreateStoresRouteInMainRepositoryCatalog(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	linked, err := vcs.Create(context.Background(), repoRoot, "linked", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	m := newReadyChatModel(30, 90)
	m.workspaceDir = linked.Path

	msg := m.createWorktreeAsync(repoRoot, "new-route", nil)()
	created, ok := msg.(worktreeCreatedMsg)
	if !ok || created.err != nil || created.wt == nil {
		t.Fatalf("create result = %#v", msg)
	}
	store, err := openContextStore(repoRoot, config.DefaultSubagentConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := worktreeRoutePrincipal(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	infos, err := store.ListSessions(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || !infos[0].WorktreeRoute || infos[0].Worktree != created.wt.Name {
		t.Fatalf("route catalog = %#v", infos)
	}
	restarted := newTUIModel(chat.NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, nullCompleter{}), nil, true)
	contextStore, err := setupSessionContext(restarted.session, created.wt.Path, &config.Resolved{Subagents: config.DefaultSubagentConfig})
	if err != nil {
		t.Fatal(err)
	}
	defer contextStore.Close()
	restarted.workspaceDir = created.wt.Path
	restarted.worktreeRouteRoot = repoRoot
	visible, err := restarted.listSessions()
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range visible {
		if info.WorktreeRoute && info.Worktree == created.wt.Name {
			return
		}
	}
	t.Fatalf("restarted worktree sessions omit route: %#v", visible)
}
