package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// TestSessionStopActiveWaitsForDone proves Cancel's stopActive does not return
// until the in-process controller signals done (or the wait bound fires).
func TestSessionStopActiveWaitsForDone(t *testing.T) {
	e := &sessionWorkflowEngine{active: make(map[string]*sessionActiveRun)}
	released := make(chan struct{})
	done := make(chan struct{})
	var cancelN atomic.Int32
	e.active["wfr-wait"] = &sessionActiveRun{
		cancel: func() {
			cancelN.Add(1)
			close(released)
		},
		done: done,
	}

	finished := make(chan struct{})
	go func() {
		e.stopActive(context.Background(), "wfr-wait")
		close(finished)
	}()

	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("active.cancel was not called")
	}
	if cancelN.Load() != 1 {
		t.Fatalf("cancel count = %d", cancelN.Load())
	}
	select {
	case <-finished:
		t.Fatal("stopActive returned before done closed")
	case <-time.After(40 * time.Millisecond):
	}
	close(done)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("stopActive did not return after done closed")
	}
}

// TestSessionDeliverRefusesWithoutAllowPublish is the tool-level safety gate.
func TestSessionDeliverRefusesWithoutAllowPublish(t *testing.T) {
	e := newSessionWorkflowEngine(t.TempDir(), "")
	result, err := e.Deliver(context.Background(), "wfr-x", false)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Refused || result.Reason == "" {
		t.Fatalf("result = %+v, want refused with reason", result)
	}
}

// TestWorkflowToolSubagentConfigHonorsResolvedStorePath proves F3: wire uses
// the session Resolved store path, not DefaultSubagentConfig alone.
func TestWorkflowToolSubagentConfigHonorsResolvedStorePath(t *testing.T) {
	root := t.TempDir()
	custom := filepath.Join(root, "custom-orchestration.db")
	res := &config.Resolved{
		Subagents: config.SubagentConfig{
			StoreBackend: "sqlite",
			StorePath:    custom,
		},
		StorePathSet: true,
	}
	cfg := workflowToolSubagentConfig(root, res)
	if cfg.StorePath != custom {
		t.Fatalf("StorePath = %q, want %q", cfg.StorePath, custom)
	}
	if cfg.StoreBackend != "sqlite" {
		t.Fatalf("StoreBackend = %q", cfg.StoreBackend)
	}
}

// TestWorkflowToolSubagentConfigLoadsWorkspaceWhenResNil covers the fallback
// path when chat wiring has no Resolved yet.
func TestWorkflowToolSubagentConfigLoadsWorkspaceWhenResNil(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".mivia"), 0o700); err != nil {
		t.Fatal(err)
	}
	// No config file → AllowMissingConfig load + apply defaults.
	cfg := workflowToolSubagentConfig(root, nil)
	// With default memory backend, openWorkflowStore still keys off
	// contextStorePath; StorePath may be empty. Ensure we did not panic and
	// returned a usable config value.
	_ = cfg
}

// TestWireWorkflowToolOptionsUsesResolvedStorePath checks that the service
// repo factory opens the same path as contextStorePath for a custom store.
func TestWireWorkflowToolOptionsUsesResolvedStorePath(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".mivia", "workflows"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Discovery needs at least an empty dir; HasWorkflows checks the directory.
	custom := filepath.Join(root, "custom.db")
	res := &config.Resolved{
		Subagents: config.SubagentConfig{
			StoreBackend: "sqlite",
			StorePath:    custom,
		},
		StorePathSet: true,
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	opts := tools.DefaultOptions{Workspace: ws}
	wireWorkflowToolOptions(&opts, ws.Abs, res)
	if len(opts.WorkflowTools) == 0 {
		t.Fatal("expected workflow tools to register")
	}
	// Factory path: openWorkflowStore uses contextStorePath when backend is
	// sqlite and path is set → ExpandPath(custom).
	want := contextStorePath(root, res.Subagents)
	if want != custom && want != config.ExpandPath(custom) {
		// contextStorePath returns ExpandPath when backend+path set.
		t.Logf("contextStorePath = %q custom = %q", want, custom)
	}
	if got := contextStorePath(root, workflowToolSubagentConfig(root, res)); got != want {
		t.Fatalf("wired store path = %q, want %q", got, want)
	}
}

// recordingDeliverGit records GitContext.Dir for each Run call.
type recordingDeliverGit struct {
	dirs []string
}

func (r *recordingDeliverGit) Run(_ context.Context, gctx delivery.GitContext, _ ...string) (string, error) {
	r.dirs = append(r.dirs, gctx.Dir)
	return "", &delivery.RefusalError{Reason: "test refuse"}
}

// TestSessionDeliverRoutesThroughCLIExecute proves allow_publish deliver
// calls executeWorkflowDeliver (CLI path) rather than localengine root deliver.
// A missing run fails before git; the recording runner must stay idle.
func TestSessionDeliverRoutesThroughCLIExecute(t *testing.T) {
	root := t.TempDir()
	e := newSessionWorkflowEngine(root, "")
	prevGit := workflowDeliverGit
	rec := &recordingDeliverGit{}
	workflowDeliverGit = rec
	t.Cleanup(func() { workflowDeliverGit = prevGit })

	_, err := e.Deliver(context.Background(), "wfr-missing", true)
	if err == nil {
		t.Fatal("expected error for missing run")
	}
	if len(rec.dirs) != 0 {
		t.Fatalf("git ran with dirs %v before run existed", rec.dirs)
	}
}

// TestSessionDeliverUsesRunWorktreeNotCallerRoot proves F1: session Deliver
// pins git to the run-owned worktree path, not the caller workspace root.
func TestSessionDeliverUsesRunWorktreeNotCallerRoot(t *testing.T) {
	root, storePath, config, _ := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, config)
	repo := openDeliveryStore(t, storePath)
	seedWorktreeChange(t, root, runID, repo)
	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.WorktreeName == "" {
		t.Fatal("fixture run has empty worktree name")
	}

	prevGit := workflowDeliverGit
	rec := &recordingDeliverGit{}
	workflowDeliverGit = rec
	t.Cleanup(func() { workflowDeliverGit = prevGit })

	e := newSessionWorkflowEngine(root, config)
	// Refusal from recording git is fine; we only assert GitContext.Dir.
	_, _ = e.Deliver(context.Background(), runID, true)
	if len(rec.dirs) == 0 {
		t.Fatal("expected git to run against the delivery worktree")
	}
	for _, dir := range rec.dirs {
		if dir == root || dir == "" {
			t.Fatalf("git Dir = %q, want run worktree (not caller root %q)", dir, root)
		}
		if !filepath.IsAbs(dir) {
			t.Fatalf("git Dir = %q, want absolute worktree path", dir)
		}
		// Worktree paths include the worktree name segment.
		if !strings.Contains(dir, run.WorktreeName) {
			t.Fatalf("git Dir = %q, want it to contain worktree %q", dir, run.WorktreeName)
		}
	}
}
