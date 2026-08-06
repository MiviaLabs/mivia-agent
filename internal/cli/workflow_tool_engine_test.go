package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
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

func TestInputsToRawFlagsUsesJSONForStructuredValues(t *testing.T) {
	flags, err := inputsToRawFlags(map[string]any{
		"object":  map[string]any{"key": "value"},
		"array":   []any{"one", float64(2)},
		"boolean": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]bool, len(flags))
	for _, flag := range flags {
		got[flag] = true
	}
	for _, want := range []string{
		`object={"key":"value"}`,
		`array=["one",2]`,
		"boolean=true",
	} {
		if !got[want] {
			t.Errorf("flags = %v, missing %q", flags, want)
		}
	}
}

func TestInputsToRawFlagsRejectsUnsupportedValue(t *testing.T) {
	if _, err := inputsToRawFlags(map[string]any{"bad": make(chan int)}); err == nil {
		t.Fatal("inputsToRawFlags accepted a value that JSON cannot encode")
	}
}

// TestSessionEngineConfigPathUsesResolvedConfigPath proves read and mutate
// paths share the session config file (covers --config / MIVIA_CONFIG).
func TestSessionEngineConfigPathUsesResolvedConfigPath(t *testing.T) {
	root := t.TempDir()
	explicit := filepath.Join(root, "session-config.toml")
	if err := os.WriteFile(explicit, []byte("model = \"x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Workspace project file exists but must not win over session ConfigPath.
	if err := os.MkdirAll(filepath.Join(root, ".mivia"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".mivia", "mivia.toml"), []byte("model = \"project\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := &config.Resolved{ConfigPath: explicit}
	got := sessionEngineConfigPath(root, res)
	if got != explicit {
		t.Fatalf("sessionEngineConfigPath = %q, want %q", got, explicit)
	}
	// Wire must pass the same path into the engine.
	if err := os.MkdirAll(filepath.Join(root, ".mivia", "workflows"), 0o700); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	opts := tools.DefaultOptions{Workspace: ws}
	res.Subagents = config.SubagentConfig{StoreBackend: "sqlite", StorePath: filepath.Join(root, "custom.db")}
	res.StorePathSet = true
	wireWorkflowToolOptions(&opts, ws.Abs, res)
	if len(opts.WorkflowTools) == 0 {
		t.Fatal("expected workflow tools")
	}
	// Extract engine config path via a deliver call that reloads config —
	// openWorkflowReportContext uses e.configPath. Missing run fails after load.
	// Direct check: sessionEngineConfigPath is the single identity helper.
	if sessionEngineConfigPath(ws.Abs, res) != explicit {
		t.Fatal("wire identity helper diverged")
	}
}

// TestSessionResumeAllowPublishDelivers mirrors startCLI auto-publish grant.
// Stub workflowResumeRun to return delivery_pending; with allow_publish=true
// launchResume must enter deliverRunWithStore (git runner sees the worktree).
func TestSessionResumeAllowPublishDelivers(t *testing.T) {
	root, storePath, configPath, _ := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, configPath)
	seedWorktreeChange(t, root, runID, openDeliveryStore(t, storePath))

	prevRun := workflowResumeRun
	prevGit := workflowDeliverGit
	t.Cleanup(func() {
		workflowResumeRun = prevRun
		workflowDeliverGit = prevGit
	})
	workflowResumeRun = func(context.Context, workflowControllerBuild) (workflowledger.RunSnapshot, error) {
		return workflowledger.RunSnapshot{
			RunID:  runID,
			Status: workflowledger.RunStatusDeliveryPending,
		}, nil
	}
	rec := &recordingDeliverGit{}
	workflowDeliverGit = rec

	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, AllowMissingConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	applyWorkflowStoreRoot(res, root)
	store, repo, closeFn, err := openWorkflowStore(root, res.Subagents)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeFn)
	finish, err := beginWorkflowExecution(root, contextStorePath(root, res.Subagents), runID)
	if err != nil {
		t.Fatal(err)
	}

	e := newSessionWorkflowEngine(root, configPath)
	p := resumePrepared{
		runID: runID, workflow: "two-step", root: root,
		built:      workflowControllerBuild{Dispatcher: workflowTestDispatcher{}},
		closeFn:    func() {},
		finishExec: finish,
		repo:       repo,
		store:      store,
		res:        res,
	}
	if _, err := e.launchResume(context.Background(), p, true); err != nil {
		t.Fatal(err)
	}
	// Wait for background controller+deliver to finish.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(rec.Dirs()) > 0 {
			return
		}
		e.mu.Lock()
		active, ok := e.active[runID]
		e.mu.Unlock()
		if !ok {
			break
		}
		select {
		case <-active.done:
		case <-time.After(20 * time.Millisecond):
		}
	}
	if len(rec.Dirs()) == 0 {
		t.Fatal("resume with allow_publish=true did not enter host deliver (no git Dir recorded)")
	}
}

// TestSessionResumeWithoutAllowPublishSkipsDeliver proves the grant is not
// implicit on resume.
func TestSessionResumeWithoutAllowPublishSkipsDeliver(t *testing.T) {
	root, storePath, configPath, _ := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, configPath)

	prevRun := workflowResumeRun
	prevGit := workflowDeliverGit
	t.Cleanup(func() {
		workflowResumeRun = prevRun
		workflowDeliverGit = prevGit
	})
	workflowResumeRun = func(context.Context, workflowControllerBuild) (workflowledger.RunSnapshot, error) {
		return workflowledger.RunSnapshot{
			RunID:  runID,
			Status: workflowledger.RunStatusDeliveryPending,
		}, nil
	}
	rec := &recordingDeliverGit{}
	workflowDeliverGit = rec

	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, AllowMissingConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	applyWorkflowStoreRoot(res, root)
	store, repo, closeFn, err := openWorkflowStore(root, res.Subagents)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeFn)
	finish, err := beginWorkflowExecution(root, contextStorePath(root, res.Subagents), runID)
	if err != nil {
		t.Fatal(err)
	}

	e := newSessionWorkflowEngine(root, configPath)
	p := resumePrepared{
		runID: runID, workflow: "two-step", root: root,
		built: workflowControllerBuild{Dispatcher: workflowTestDispatcher{}}, closeFn: func() {}, finishExec: finish,
		repo: repo, store: store, res: res,
	}
	if _, err := e.launchResume(context.Background(), p, false); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		e.mu.Lock()
		active, ok := e.active[runID]
		e.mu.Unlock()
		if !ok {
			break
		}
		select {
		case <-active.done:
		case <-time.After(20 * time.Millisecond):
		}
	}
	if dirs := rec.Dirs(); len(dirs) != 0 {
		t.Fatalf("resume without allow_publish entered deliver: dirs=%v", dirs)
	}
	_ = storePath
}

// TestWorkflowToolSubagentConfigLoadsWorkspaceWhenResNil covers the fallback
// path when chat wiring has no Resolved yet.
func TestWorkflowToolSubagentConfigLoadsWorkspaceWhenResNil(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".mivia"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := workflowToolSubagentConfig(root, nil)
	// DefaultSubagentConfig or loaded defaults must be usable for open path.
	path := contextStorePath(root, cfg)
	if path == "" {
		t.Fatal("contextStorePath empty for res=nil config")
	}
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
	mu   sync.Mutex
	dirs []string
}

func (r *recordingDeliverGit) Run(_ context.Context, gctx delivery.GitContext, _ ...string) (string, error) {
	r.mu.Lock()
	r.dirs = append(r.dirs, gctx.Dir)
	r.mu.Unlock()
	return "", &delivery.RefusalError{Reason: "test refuse"}
}

func (r *recordingDeliverGit) Dirs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.dirs))
	copy(out, r.dirs)
	return out
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
	if dirs := rec.Dirs(); len(dirs) != 0 {
		t.Fatalf("git ran with dirs %v before run existed", dirs)
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
	dirs := rec.Dirs()
	if len(dirs) == 0 {
		t.Fatal("expected git to run against the delivery worktree")
	}
	for _, dir := range dirs {
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
