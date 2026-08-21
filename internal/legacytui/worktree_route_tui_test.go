package legacytui

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"github.com/MiviaLabs/mivia-agent/internal/cliworktree"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

func TestWorktreeDialogCreateStoresRouteInMainRepositoryCatalog(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	storePath := filepath.Join(repoRoot, "repository.db")
	writeWorktreeStoreConfig(t, repoRoot, "repository.db")
	environmentConfig := filepath.Join(t.TempDir(), "mivia.toml")
	if err := os.WriteFile(environmentConfig, []byte(worktreeStoreConfig("environment.db")), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIVIA_CONFIG", environmentConfig)
	linked, err := vcs.Create(context.Background(), repoRoot, "linked", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	m := newReadyChatModel(30, 90)
	m.workspaceDir = linked.Path
	sessionStore, err := cli.OpenContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer sessionStore.Close()
	if err := m.session.SetContextStore(sessionStore); err != nil {
		t.Fatal(err)
	}

	msg := m.createWorktreeAsync(repoRoot, "new-route", nil)()
	created, ok := msg.(worktreeCreatedMsg)
	if !ok || created.err != nil || created.wt == nil {
		t.Fatalf("create result = %#v", msg)
	}
	store, err := cli.OpenContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := cliworktree.WorktreeRoutePrincipal(repoRoot)
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
	contextStore, err := cli.SetupRepositorySessionContext(restarted.session, repoRoot, storePath, &config.Resolved{Subagents: config.DefaultSubagentConfig})
	if err != nil {
		t.Fatal(err)
	}
	defer contextStore.Close()
	restarted.workspaceDir = created.wt.Path
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
