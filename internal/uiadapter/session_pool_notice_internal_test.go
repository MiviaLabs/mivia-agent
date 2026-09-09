package uiadapter

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/worktreeroute"
)

func TestAppendToolScope_JoinsAllShapes(t *testing.T) {
	if got := appendToolScope("", "warn"); got != "warn" {
		t.Fatalf("warning-only: %q", got)
	}
	if got := appendToolScope("base", ""); got != "base" {
		t.Fatalf("no warning: %q", got)
	}
	if got := appendToolScope("base", "warn"); got != "base warn" {
		t.Fatalf("join: %q", got)
	}
}

func TestFencePooledWorktree_PureBuilders(t *testing.T) {
	removed := removedInstanceText("wt1")
	for _, want := range []string{
		`cannot resume session in worktree "wt1"`,
		`worktree "wt1" was removed`,
	} {
		if !strings.Contains(removed, want) {
			t.Errorf("removed text %q missing %q", removed, want)
		}
	}
	recreated := recreatedInstanceText("wt1", "wt_old", "wt_new")
	for _, want := range []string{
		`cannot resume session in worktree "wt1"`,
		"was recreated under the same name",
		"(pooled wt_old, live wt_new)",
	} {
		if !strings.Contains(recreated, want) {
			t.Errorf("recreated text %q missing %q", recreated, want)
		}
	}
}

// ---- self-contained helpers (unique *_Notice suffixes avoid clashes) ----

type noticeCompleter struct{}

func (noticeCompleter) Name() string { return "notice-null" }

func (noticeCompleter) ChatStream(_ context.Context, _ provider.Request, w io.Writer) (string, error) {
	return "", nil
}

func (noticeCompleter) Chat(context.Context, provider.Request) (string, error) { return "", nil }

func (noticeCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	return &provider.Response{FinishReason: "stop"}, nil
}

func noticeGitInit(t *testing.T, mainDir string) {
	t.Helper()
	run := func(args ...string) {
		out, gerr := exec.Command("git", append([]string{"-C", mainDir}, args...)...).CombinedOutput()
		if gerr != nil {
			t.Fatalf("git %v: %v\n%s", args, gerr, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.invalid")
	run("config", "user.name", "test")
}

// noticeFixture registers managed wt1 + wtB and returns both canonical dirs.
func noticeFixture(t *testing.T) (*storage.SQLite, string, string, string) {
	t.Helper()
	mainDir := filepath.Join(t.TempDir(), "main")
	wt1 := filepath.Join(filepath.Dir(mainDir), ".mivia", "worktrees", "wt1")
	wtB := filepath.Join(filepath.Dir(mainDir), ".mivia", "worktrees", "wtB")
	for _, dir := range []string{mainDir, wt1, wtB} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create dirs: %v", err)
		}
	}
	canonWt1, c1err := filepath.EvalSymlinks(wt1)
	if c1err != nil {
		t.Fatal(c1err)
	}
	canonWtB, cBerr := filepath.EvalSymlinks(wtB)
	if cBerr != nil {
		t.Fatal(cBerr)
	}
	noticeGitInit(t, mainDir)

	dbPath := filepath.Join(t.TempDir(), "ctx.db")
	store, err := storage.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	principal, perr := worktreeroute.Principal(mainDir)
	if perr != nil {
		t.Fatal(perr)
	}
	instWt1 := contextstate.WorktreeInstance{Worktree: "wt1", ID: "wt_0001020304050607"}
	if err := store.BeginWorktreeCreation(context.Background(), principal, instWt1, canonWt1); err != nil {
		t.Fatalf("begin wt1: %v", err)
	}
	if err := store.RegisterWorktreeInstance(context.Background(), principal, instWt1, canonWt1); err != nil {
		t.Fatalf("register wt1: %v", err)
	}
	writeNoticeMarker(t, canonWt1, instWt1)
	instWtB := contextstate.WorktreeInstance{Worktree: "wtB", ID: "wt_00000000000000ff"}
	if err := store.BeginWorktreeCreation(context.Background(), principal, instWtB, canonWtB); err != nil {
		t.Fatalf("begin wtB: %v", err)
	}
	if err := store.RegisterWorktreeInstance(context.Background(), principal, instWtB, canonWtB); err != nil {
		t.Fatalf("register wtB: %v", err)
	}
	writeNoticeMarker(t, canonWtB, instWtB)
	return store, mainDir, canonWt1, canonWtB
}

func writeNoticeMarker(t *testing.T, root string, instance contextstate.WorktreeInstance) {
	t.Helper()
	dir := filepath.Join(root, ".mivia")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"version":1,"worktree":"` + instance.Worktree + `","id":"` + instance.ID + `"}`)
	if err := os.WriteFile(filepath.Join(dir, "worktree-instance.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

func noticeEnableCtx(t *testing.T, sess *chat.Session, store *storage.SQLite, mainDir string) {
	t.Helper()
	principal, err := contextstate.NewPrincipal(worktreeroute.WorkspaceID(mainDir), sess.SessionID, "local-user")
	if err != nil {
		t.Fatal(err)
	}
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: store},
		Enabled:             true,
	}
	if err := sess.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	if err := sess.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
}

func TestCommandRunner_ResumeInWorktree_FenceReportsRecreatedAcrossNames(t *testing.T) {
	store, mainDir, canonWt1, canonWtB := noticeFixture(t)
	res := &config.Resolved{ProviderName: "fake", Model: "m1", SystemPrompt: "sys"}
	sess := chat.NewSession(res, &noticeCompleter{})
	sess.SessionID = "session-main"
	noticeEnableCtx(t, sess, store, mainDir)
	sess.UseTools = true
	regA := tools.NewDefaultRegistry(tools.DefaultOptions{})
	sess.Tools = regA
	t.Chdir(mainDir)

	bound := chat.NewSession(res, &noticeCompleter{})
	bound.SessionID = "wt-save"
	bound.UseTools = true
	if _, err := worktreeroute.StartInRoute(context.Background(), bound, store, mainDir,
		worktreeroute.Route{Worktree: "wt1", Dir: canonWt1}); err != nil {
		t.Fatalf("seed bound session: %v", err)
	}
	// Mirror the pool: enable context (manager + store) only after binding.
	noticeEnableCtx(t, bound, store, mainDir)

	pool := NewSessionPool(bound, res, nil, true)
	r := &CommandRunner{sess: bound, pool: pool}

	first := ports.SessionSummary{ID: "wt-save", Worktree: "wt1", WorktreeDir: canonWt1}
	if out := r.ResumeInWorktree(context.Background(), first); out.Err != "" {
		t.Fatalf("first resume errored: %s", out.Err)
	}

	drifted := ports.SessionSummary{ID: "wt-save", Worktree: "wtB", WorktreeDir: canonWtB}
	out := r.ResumeInWorktree(context.Background(), drifted)
	for _, want := range []string{
		`cannot resume session in worktree "wtB"`,
		"was recreated under the same name",
		"(pooled ",
		", live ",
	} {
		if !strings.Contains(out.Err, want) {
			t.Errorf("fence text %q missing %q", out.Err, want)
		}
	}
	if out.Conversation != nil {
		t.Error("recreated fence must not install a conversation")
	}
}
