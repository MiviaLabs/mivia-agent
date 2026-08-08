package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	tea "github.com/charmbracelet/bubbletea"
)

func TestSetupSessionContextIsAlwaysEnabled(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(filepath.Join(root, ".mivia"), 0o755); err != nil {
		t.Fatal(err)
	}
	session := chat.NewSession(&config.Resolved{Model: "model"}, nullCompleter{})
	store, err := setupSessionContext(session, root, &config.Resolved{Subagents: config.DefaultSubagentConfig})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if !session.ContextEnabled() {
		t.Fatal("session context is disabled")
	}
	if _, ok := session.ContextStore().(*storage.SQLite); !ok {
		t.Fatalf("context store = %T, want SQLite", session.ContextStore())
	}
	if _, _, ok := session.ContextPreparation(); !ok {
		t.Fatal("session did not expose isolated preparation capability")
	}
}

func TestReactivationRejectsConcurrentlyRemovedWorktree(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	worktree, err := createManagedWorktree(repoRoot, "concurrent-remove", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	instance, err := beginManagedWorktreeRemoval(repoRoot, worktree)
	if err != nil {
		t.Fatal(err)
	}
	if err := vcs.RemoveWithPrefix(context.Background(), repoRoot, worktree.Name, "mivia/"); err != nil {
		t.Fatal(err)
	}
	if err := reactivateManagedWorktree(repoRoot, instance); err == nil {
		t.Fatal("reactivation accepted an absent Git worktree and marker")
	}
	store, err := openRepositoryContextStore(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := worktreeRoutePrincipal(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	deleting, err := store.ListDeletingWorktreeInstances(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	if len(deleting) != 1 || deleting[0].Instance != instance {
		t.Fatalf("deleting rows = %+v, want exact instance %+v", deleting, instance)
	}
}

func TestContextDispatcherUsesSessionStoreWithMemoryConfig(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(filepath.Join(root, ".mivia"), 0o755); err != nil {
		t.Fatal(err)
	}
	session := chat.NewSession(&config.Resolved{Model: "model"}, nullCompleter{})
	store, err := setupSessionContext(session, root, &config.Resolved{Subagents: config.DefaultSubagentConfig})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	wiring := contextDispatcherFor(session, config.DefaultSubagentConfig)
	if wiring.sharedSQLite != store {
		t.Fatalf("shared store = %p, want session store %p", wiring.sharedSQLite, store)
	}
	dispatcher, err := NewSessionDispatcher(SessionDispatcherOpts{
		Registry: tools.NewRegistry(), Completer: nullCompleter{}, Model: "model",
		Config: config.DefaultSubagentConfig, SharedSQLite: wiring.sharedSQLite,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	repo, ok := orchestrationRepoForDispatcher(dispatcher).(*ledger.StorageLedgerRepository)
	if !ok || repo.UnderlyingStore() != store {
		t.Fatalf("ledger store = %#v, want session store %p", repo, store)
	}
	if err := repo.CreateRun(context.Background(), "", ledger.RunSnapshot{RunID: "shared-run", Status: ledger.RunStatusCreated}); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.NewStorageLedgerRepository(store).GetRun(context.Background(), "shared-run"); err != nil {
		t.Fatalf("shared session database does not contain ledger run: %v", err)
	}
}

func TestSetupSessionContextListsExistingSQLiteContextSessions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(filepath.Join(root, ".mivia"), 0o755); err != nil {
		t.Fatal(err)
	}
	first := chat.NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, nullCompleter{})
	store, err := setupSessionContext(first, root, &config.Resolved{Subagents: config.DefaultSubagentConfig})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.SendUser(context.Background(), "persist this", io.Discard); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	second := chat.NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, nullCompleter{})
	store, err = setupSessionContext(second, root, &config.Resolved{Subagents: config.DefaultSubagentConfig})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.SendUser(context.Background(), "second session", io.Discard); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	loader := chat.NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, nullCompleter{})
	store, err = setupSessionContext(loader, root, &config.Resolved{Subagents: config.DefaultSubagentConfig})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	infos, err := loader.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) == 0 {
		t.Fatal("SQLite context session was not presented in session list")
	}
	if len(infos) != 2 {
		t.Fatalf("SQLite context session list = %#v, want two sessions", infos)
	}
	for i, sessionID := range []string{first.SessionID, second.SessionID, first.SessionID, second.SessionID} {
		if err := loader.Load(sessionID); err != nil {
			t.Fatalf("load %d (%s): %v", i, sessionID, err)
		}
	}
	if got := loader.MessagesCopy(); len(got) < 1 || got[0].Content != "second session" {
		t.Fatalf("loaded SQLite context history = %#v", got)
	}
	if err := loader.DeleteSession(first.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := loader.DeleteSession(second.SessionID); err != nil {
		t.Fatal(err)
	}
	if infos, err := loader.ListSessions(); err != nil || len(infos) != 0 {
		t.Fatalf("sessions after delete = %#v, err=%v", infos, err)
	}
}

func TestListSessionsIncludesMainRepositoryWorktreeRoutes(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	worktree, err := vcs.Create(context.Background(), repoRoot, "route-target", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if err := registerWorktreeRoute(repoRoot, worktree); err != nil {
		t.Fatal(err)
	}
	containsRoute := func(infos []chat.SessionInfo) bool {
		for _, info := range infos {
			if info.WorktreeRoute && info.Worktree == worktree.Name && info.Dir == worktree.Path {
				return true
			}
		}
		return false
	}
	rootModel := newTUIModel(chat.NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, nullCompleter{}), nil, true)
	storePath, err := repositorySessionStorePath(repoRoot, chatInvocation{}, &config.Resolved{})
	if err != nil {
		t.Fatal(err)
	}
	rootStore, err := setupRepositorySessionContext(rootModel.session, repoRoot, storePath, &config.Resolved{Subagents: config.DefaultSubagentConfig})
	if err != nil {
		t.Fatal(err)
	}
	defer rootStore.Close()
	rootModel.workspaceDir = repoRoot
	rootInfos, err := rootModel.listSessions()
	if err != nil {
		t.Fatal(err)
	}
	if !containsRoute(rootInfos) {
		t.Fatalf("worktree route is missing from main repository sessions: %#v", rootInfos)
	}

	linkedModel := newTUIModel(chat.NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, nullCompleter{}), nil, true)
	store, err := setupRepositorySessionContext(linkedModel.session, repoRoot, storePath, &config.Resolved{Subagents: config.DefaultSubagentConfig})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	linkedModel.workspaceDir = worktree.Path
	infos, err := linkedModel.listSessions()
	if err != nil {
		t.Fatal(err)
	}
	if !containsRoute(infos) {
		t.Fatalf("worktree route is missing from linked-worktree sessions: %#v", infos)
	}
}

func TestOpenRepositoryContextStoreIgnoresLegacyStore(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	writeWorktreeStoreConfig(t, repoRoot, "catalog.db")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	legacyPath := config.DefaultStorePathForWorkspace(repoRoot)
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte("not a SQLite database"), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := openRepositoryContextStore(repoRoot)
	if err != nil {
		t.Fatalf("open repository catalog: %v", err)
	}
	defer store.Close()
}

func TestWorktreeSessionListRestartsToResumeMainRepositorySession(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })
	repoRoot := newWorktreeCommandRepo(t)
	storePath := contextStorePath(repoRoot, config.DefaultSubagentConfig)
	invocation := chatInvocation{repositorySessionStorePath: storePath}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatal(err)
	}
	rootSession := chat.NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, nullCompleter{})
	rootStore, err := setupChatSessionContext(rootSession, repoRoot, invocation, &config.Resolved{Subagents: config.DefaultSubagentConfig})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rootSession.SendUser(context.Background(), "main history", io.Discard); err != nil {
		_ = rootStore.Close()
		t.Fatal(err)
	}
	rootID := rootSession.SessionID
	if err := rootStore.Close(); err != nil {
		t.Fatal(err)
	}
	worktree, err := vcs.Create(context.Background(), repoRoot, "resume-target", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	worktreeStore, err := openContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registerManagedWorktreeInStore(worktreeStore, repoRoot, worktree); err != nil {
		_ = worktreeStore.Close()
		t.Fatal(err)
	}
	if err := worktreeStore.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(worktree.Path); err != nil {
		t.Fatal(err)
	}
	model := newTUIModel(chat.NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, nullCompleter{}), nil, true)
	store, err := setupChatSessionContext(model.session, worktree.Path, invocation, &config.Resolved{Subagents: config.DefaultSubagentConfig})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	model.workspaceDir = worktree.Path
	if _, err := model.session.SendUser(context.Background(), "worktree history", io.Discard); err != nil {
		t.Fatal(err)
	}
	worktreeID := model.session.SessionID
	infos, err := model.listSessions()
	if err != nil {
		t.Fatal(err)
	}
	foundWorktreeSession := false
	for _, info := range infos {
		if info.Name == worktreeID && info.Dir == worktree.Path {
			foundWorktreeSession = true
		}
	}
	if !foundWorktreeSession {
		t.Fatalf("worktree session %q is missing from shared catalog: %#v", worktreeID, infos)
	}
	assertRepositoryCatalogSessions(t, repoRoot, invocation, rootID)
	assertSessionAbsent(t, infos, rootID)
}

func assertSessionAbsent(t *testing.T, infos []chat.SessionInfo, sessionID string) {
	t.Helper()
	for _, info := range infos {
		if info.Name == sessionID {
			t.Fatalf("session %q unexpectedly appears in scoped list: %#v", sessionID, infos)
		}
	}
}

func assertRepositoryCatalogSessions(t *testing.T, repoRoot string, invocation chatInvocation, sessionIDs ...string) {
	t.Helper()
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatal(err)
	}
	loader := chat.NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, nullCompleter{})
	store, err := setupChatSessionContext(loader, repoRoot, invocation, &config.Resolved{Subagents: config.DefaultSubagentConfig})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	infos, err := loader.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	for _, sessionID := range sessionIDs {
		found := false
		for _, info := range infos {
			found = found || info.Name == sessionID
		}
		if !found {
			t.Fatalf("main tree does not see session %q: %#v", sessionID, infos)
		}
	}
}

func assertSessionRestart(t *testing.T, model *tuiModel, infos []chat.SessionInfo, sessionID, root string) {
	t.Helper()
	for _, info := range infos {
		if info.Name != sessionID || info.WorktreeRoute {
			continue
		}
		if err := model.openSessionInfo(info); err != nil {
			t.Fatal(err)
		}
		if model.restartWorkspace != root || model.resumeSessionName != sessionID {
			t.Fatalf("restart = (%q, %q), want (%q, %q)", model.restartWorkspace, model.resumeSessionName, root, sessionID)
		}
		return
	}
	t.Fatalf("session %q is missing from worktree list: %#v", sessionID, infos)
}

func TestWorktreeSessionListReadsMainRepositoryCustomStore(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	storePath := filepath.Join(repoRoot, "session-catalog.db")
	rootConfig := config.SubagentConfig{StoreBackend: "sqlite", StorePath: storePath}
	rootSession := chat.NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, nullCompleter{})
	rootStore, err := setupSessionContext(rootSession, repoRoot, &config.Resolved{Subagents: rootConfig})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rootSession.SendUser(context.Background(), "custom store history", io.Discard); err != nil {
		_ = rootStore.Close()
		t.Fatal(err)
	}
	rootID := rootSession.SessionID
	if err := rootStore.Close(); err != nil {
		t.Fatal(err)
	}
	worktree, err := vcs.Create(context.Background(), repoRoot, "custom-store-target", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	model := newTUIModel(chat.NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, nullCompleter{}), nil, true)
	store, err := setupRepositorySessionContext(model.session, repoRoot, storePath, &config.Resolved{Subagents: config.DefaultSubagentConfig})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	model.workspaceDir = worktree.Path
	infos, err := model.listSessions()
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range infos {
		if info.Name == rootID {
			return
		}
	}
	t.Fatalf("custom-store root session %q is missing: %#v", rootID, infos)
}

func TestSQLiteSessionsAreSelectableThroughSplashAndDialog(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(filepath.Join(root, ".mivia"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(prompt string) string {
		s := chat.NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, nullCompleter{})
		store, err := setupSessionContext(s, root, &config.Resolved{Subagents: config.DefaultSubagentConfig})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.SendUser(context.Background(), prompt, io.Discard); err != nil {
			_ = store.Close()
			t.Fatal(err)
		}
		id := s.SessionID
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		return id
	}
	firstID, secondID := write("first history"), write("second history")
	loader := chat.NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, nullCompleter{})
	store, err := setupSessionContext(loader, root, &config.Resolved{Subagents: config.DefaultSubagentConfig})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	infos, err := loader.ListSessions()
	if err != nil || len(infos) != 2 {
		t.Fatalf("SQLite sessions = %#v, err=%v", infos, err)
	}
	indexOf := func(id string) int {
		for i, info := range infos {
			if info.Name == id {
				return i
			}
		}
		return -1
	}
	res := &config.Resolved{ProviderName: "fake", Model: "model"}
	m := newTUIModel(loader, res, true)
	m.mode, m.ready, m.width, m.height = modeWelcome, true, 100, 40
	m.sessions, m.sessionSel = infos, indexOf(firstID)
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := loader.MessagesCopy(); len(got) == 0 || got[0].Content != "first history" {
		t.Fatalf("splash did not load first SQLite session: %#v", got)
	}
	for i, id := range []string{secondID, firstID, secondID} {
		m.mode = modeChat
		m.sessionsSidebar = nil
		if !m.handleSlash("/sessions") {
			t.Fatal("/sessions was not handled")
		}
		m.sessionsSidebar.cursor = indexOf(id)
		m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		want := "second history"
		if id == firstID {
			want = "first history"
		}
		got := loader.MessagesCopy()
		if len(got) == 0 || got[0].Content != want {
			t.Fatalf("dialog load %d (%s) = %#v, want %q", i, id, got, want)
		}
	}
}
