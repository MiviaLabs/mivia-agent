package localengine_test

// Engine-level regression tests for the automatic stack drive
// (drive-before-delivery): a stacking plan run whose controller parks at
// delivery_pending must drive its chunk stack automatically (admit chunk
// runs, deliver per the merge policy, wait for merges, admit the integration
// run, settle the plan run) - the CLI and session paths already do, the
// agent-tools engine did not, which is the harness bug these tests pin. They
// also pin the drive-before-delivery gate: an explicit workflow_deliver on an
// undriven plan run must be refused.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/localengine"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/stacking"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/tasks"
)

// stackDriveWorkflowTOML is a stacking workflow with an active delivery
// policy (the controller parks the run at delivery_pending) and the stacking
// merge policy parameterized: mergePolicy "auto" delivers chunk runs
// automatically, "approve" parks them for the publish grant. deliverPlanRun
// toggles delivery.deliver_plan_run (default false: the plan run settles
// succeeded without its own PR once the stack drove).
func stackDriveWorkflowTOML(name, mergePolicy string, deliverPlanRun bool) string {
	deliveryExtra := ""
	if deliverPlanRun {
		deliveryExtra = "deliver_plan_run = true\n"
	}
	stackingExtra := ""
	if mergePolicy != "" {
		stackingExtra = "merge_policy = \"" + mergePolicy + "\"\n"
	}
	return `version = 1
name = "` + name + `"
initial_step = "plan"

[inputs.task]
type = "string"
required = true
max_bytes = 100

[delivery]
kind = "pull_request"
mode = "draft"
provider = "github"
base = "main"
` + deliveryExtra + `
[stacking]
plan_step = "plan"
implement_step = "implement"
` + stackingExtra + `
[[steps]]
id = "plan"
kind = "agent"
agent = "planner"

[[steps]]
id = "implement"
kind = "agent"
agent = "implementer"

[[transitions]]
from = "plan"
to = "implement"
[transitions.match]
status = "succeeded"

[[transitions]]
from = "implement"
to = "success"
[transitions.match]
status = "succeeded"
`
}

// writeStackDriveWorkspace writes a workspace with the drive workflow(s).
// The repo is origin-backed (main + bare origin) because the workflows
// declare an active [delivery] policy that resolves the push remote.
func writeStackDriveWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runGitT(t, root, "init", "-b", "main")
	runGitT(t, root, "config", "user.email", "test@example.com")
	runGitT(t, root, "config", "user.name", "Test")
	writeFileT(t, filepath.Join(root, "base.txt"), "base\n")
	runGitT(t, root, "add", "base.txt")
	runGitT(t, root, "commit", "-m", "base")
	originDir := filepath.Join(t.TempDir(), "origin.git")
	runGitT(t, filepath.Dir(originDir), "init", "--bare", filepath.Base(originDir))
	runGitT(t, root, "remote", "add", "origin", originDir)
	runGitT(t, root, "push", "-u", "origin", "main")
	wfRoot := filepath.Join(root, ".mivia", "workflows")
	if err := os.MkdirAll(wfRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	// The engine-synthesized steps reference engine-reserved output schemas;
	// admission loads and pins them exactly like declared steps'.
	schemasDir := filepath.Join(wfRoot, "schemas")
	if err := os.MkdirAll(schemasDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, schemaBody := range map[string]string{
		"chunk-plan-v1.json":        `{"type":"object"}`,
		"chunk-plan-review-v1.json": `{"type":"object"}`,
	} {
		if err := os.WriteFile(filepath.Join(schemasDir, name), []byte(schemaBody), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		"stack-drive-me":    stackDriveWorkflowTOML("stack-drive-me", "auto", false),
		"stack-approve-me":  stackDriveWorkflowTOML("stack-approve-me", "approve", false),
		"stack-deliver-me":  stackDriveWorkflowTOML("stack-deliver-me", "auto", true),
		"stack-no-store-me": stackDriveWorkflowTOML("stack-no-store-me", "auto", false),
	} {
		if err := os.WriteFile(filepath.Join(wfRoot, name+".toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// stackDriveEngine builds an engine over the drive workspace with a shared
// SQLite store (the task ledger) and the scripted runner.
func stackDriveEngine(t *testing.T) (*localengine.Engine, *agenttools.Service, *storage.SQLite) {
	t.Helper()
	root := writeStackDriveWorkspace(t)
	repo := workflowledger.NewMemoryRepository()
	db, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	engine := &localengine.Engine{
		WorkspaceRoot: root,
		Repo:          repo,
		Store:         db,
		PR:            &recordingPR{},
		NewRunner: func() controller.AgentStepRunner {
			return &scriptedAttemptRunner{byStepCall: map[string]json.RawMessage{
				"plan":      json.RawMessage(`{"summary":"p"}`),
				"decompose": json.RawMessage(stackValidMultiPlan),
				"implement": json.RawMessage(`{"summary":"s"}`),
			}}
		},
	}
	return engine, mustService(t, engine, repo), db
}

// stackDriveEngineNoStore is the same engine without the shared store: the
// drive cannot seed a task ledger and the engine degrades to the operator
// drive.
func stackDriveEngineNoStore(t *testing.T) (*localengine.Engine, *agenttools.Service) {
	t.Helper()
	root := writeStackDriveWorkspace(t)
	repo := workflowledger.NewMemoryRepository()
	engine := &localengine.Engine{
		WorkspaceRoot: root,
		Repo:          repo,
		PR:            &recordingPR{},
		NewRunner: func() controller.AgentStepRunner {
			return &scriptedAttemptRunner{byStepCall: map[string]json.RawMessage{
				"plan":      json.RawMessage(`{"summary":"p"}`),
				"decompose": json.RawMessage(stackValidMultiPlan),
				"implement": json.RawMessage(`{"summary":"s"}`),
			}}
		},
	}
	return engine, mustService(t, engine, repo)
}

// waitPlanRunStatus polls a run until its status matches want or the deadline
// passes.
func waitPlanRunStatus(t *testing.T, svc *agenttools.Service, runID, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		view := stackStatusView(t, svc, runID)
		if view.Status == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s status = %q, want %q (attempts %+v)", runID, view.Status, want, view.Attempts)
		}
		<-time.After(200 * time.Millisecond)
	}
}

// integrationRunSettled reports whether the stack's final integration run
// (stable key <stack-id>:integration) has settled succeeded, the auto-policy
// completion requirement stackDriveCompleted checks before the plan run's
// own publication is allowed.
func integrationRunSettled(t *testing.T, engine *localengine.Engine, stackID string) bool {
	t.Helper()
	runs, err := engine.Repo.ListRuns(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := stackID + ":" + stacking.IntegrationChunkID
	for _, r := range runs {
		if r.InvocationKey == want {
			return r.Status == workflowledger.RunStatusSucceeded
		}
	}
	return false
}

// TestEngineStackAutoDrivesAfterPark is the harness-bug regression: a
// multi-chunk plan run parks at delivery_pending and the engine's
// drive-before-delivery must drive the stack automatically - admit both
// chunks (stable keys), deliver them (no_diff -> merged), admit the
// integration run, and settle the plan run succeeded without publishing it.
func TestEngineStackAutoDrivesAfterPark(t *testing.T) {
	engine, svc, db := stackDriveEngine(t)
	started := startStackingRunFor(t, svc, "stack-drive-me", nil)
	waitRun(t, engine, started.RunID)
	// The automatic drive settles the parked plan run.
	waitPlanRunStatus(t, svc, started.RunID, "succeeded", 60*time.Second)

	runs, err := engine.Repo.ListRuns(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var chunkRuns, integrationRuns []workflowledger.RunSnapshot
	for _, r := range runs {
		if r.RunID == started.RunID {
			continue // the plan run itself
		}
		key := r.InvocationKey
		if strings.HasSuffix(key, ":"+stacking.IntegrationChunkID) {
			integrationRuns = append(integrationRuns, r)
			continue
		}
		if strings.Contains(key, ":") {
			chunkRuns = append(chunkRuns, r)
		}
	}
	if len(chunkRuns) < 2 {
		t.Fatalf("chunk runs admitted = %d, want >= 2: %+v", len(chunkRuns), runs)
	}
	for _, r := range chunkRuns {
		if r.Status != workflowledger.RunStatusSucceeded {
			t.Fatalf("chunk run %s (key %s) status = %q, want succeeded", r.RunID, r.InvocationKey, r.Status)
		}
	}
	if len(integrationRuns) != 1 {
		t.Fatalf("integration runs = %d, want 1: %+v", len(integrationRuns), runs)
	}
	if integrationRuns[0].Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("integration run status = %q, want succeeded", integrationRuns[0].Status)
	}

	// The task ledger shows every chunk merged.
	ledger := tasks.NewStore(db)
	byID, err := stacking.TaskMap(context.Background(), ledger, started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"c1", "c2"} {
		task, ok := byID[id]
		if !ok || task.Status != stacking.StatusMerged {
			t.Fatalf("task %s = %+v, want merged", id, task)
		}
	}
}

// TestEngineStackApproveParksChunksAndGateRefusesUndrivenPublish pins the
// grant-policy behavior and the drive-before-delivery gate: under
// merge_policy=approve the drive admits the first wave and parks it at
// delivery_pending (reviewed, awaiting the publish grant), then stops; the
// plan run stays delivery_pending and an explicit workflow_deliver is refused
// because the stack has not fully driven.
func TestEngineStackApproveParksChunksAndGateRefusesUndrivenPublish(t *testing.T) {
	engine, svc, db := stackDriveEngine(t)
	started := startStackingRunFor(t, svc, "stack-approve-me", nil)
	waitRun(t, engine, started.RunID)

	ledger := tasks.NewStore(db)
	deadline := time.Now().Add(30 * time.Second)
	for {
		byID, err := stacking.TaskMap(context.Background(), ledger, started.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if task, ok := byID["c1"]; ok && task.Status == stacking.StatusReviewed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("chunk c1 never reached reviewed: %+v", byID)
		}
		<-time.After(200 * time.Millisecond)
	}

	view := stackStatusView(t, svc, started.RunID)
	if view.Status != "delivery_pending" {
		t.Fatalf("plan run status = %q, want delivery_pending", view.Status)
	}
	_, err := engine.Deliver(context.Background(), started.RunID, true)
	if err == nil || !strings.Contains(err.Error(), "has not fully driven") {
		t.Fatalf("Deliver on the undriven plan run = %v, want the drive-before-delivery gate refusal", err)
	}
	// The refused publish must not have moved the run.
	view = stackStatusView(t, svc, started.RunID)
	if view.Status != "delivery_pending" {
		t.Fatalf("plan run status after refused deliver = %q, want delivery_pending", view.Status)
	}
}

// TestEngineStackGateAllowsPublishAfterDrive pins the gate's positive side:
// with deliver_plan_run=true the automatic drive leaves the plan run
// delivery_pending once the stack drove to completion, and an explicit
// workflow_deliver is then allowed and settles it succeeded.
func TestEngineStackGateAllowsPublishAfterDrive(t *testing.T) {
	engine, svc, db := stackDriveEngine(t)
	started := startStackingRunFor(t, svc, "stack-deliver-me", nil)
	waitRun(t, engine, started.RunID)

	// The drive completes the stack but leaves the plan run parked for its
	// own explicit deliver.
	waitPlanRunStatus(t, svc, started.RunID, "delivery_pending", 60*time.Second)

	// Wait for the ledger to show the stack drove to completion: every chunk
	// merged AND the integration run settled (stackDriveCompleted requires
	// both; the chunks-merged wait alone races the drive's final
	// integration-run settle).
	ledger := tasks.NewStore(db)
	deadline := time.Now().Add(30 * time.Second)
	for {
		byID, err := stacking.TaskMap(context.Background(), ledger, started.RunID)
		if err != nil {
			t.Fatal(err)
		}
		merged := 0
		for _, id := range []string{"c1", "c2"} {
			if task, ok := byID[id]; ok && task.Status == stacking.StatusMerged {
				merged++
			}
		}
		if merged == 2 && integrationRunSettled(t, engine, started.RunID) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stack never drove to completion: %+v", byID)
		}
		<-time.After(200 * time.Millisecond)
	}

	res, err := engine.Deliver(context.Background(), started.RunID, true)
	if err != nil {
		t.Fatalf("Deliver on the fully driven plan run: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("deliver result = %+v, want succeeded", res)
	}
	waitPlanRunStatus(t, svc, started.RunID, "succeeded", 30*time.Second)
}

// TestEngineStackNoStoreDegradesToOperatorDrive pins the safe degradation
// when the engine has no shared store: it cannot seed a task ledger, so it
// must not deliver the plan run (the gate refuses) and must not loop - the
// run stays delivery_pending and the operator drive (`mivia stack drive`)
// owns the stack.
func TestEngineStackNoStoreDegradesToOperatorDrive(t *testing.T) {
	engine, svc := stackDriveEngineNoStore(t)
	started := startStackingRunFor(t, svc, "stack-no-store-me", nil)
	waitRun(t, engine, started.RunID)

	view := stackStatusView(t, svc, started.RunID)
	if view.Status != "delivery_pending" {
		t.Fatalf("plan run status = %q, want delivery_pending", view.Status)
	}
	// Give any (wrong) drive a few poll intervals to act; it must not.
	<-time.After(4 * time.Second)
	view = stackStatusView(t, svc, started.RunID)
	if view.Status != "delivery_pending" {
		t.Fatalf("plan run status without a store = %q, want delivery_pending (the operator drive owns it)", view.Status)
	}
	// An explicit publish is refused: the stack drive cannot be verified.
	_, err := engine.Deliver(context.Background(), started.RunID, true)
	if err == nil || !strings.Contains(err.Error(), "has not fully driven") {
		t.Fatalf("Deliver without a store = %v, want the drive-before-delivery gate refusal", err)
	}
}

// stackDriveEngineWithGit is stackDriveEngine with a caller-supplied git
// runner, so the drive tests can script refused and transient delivery
// outcomes through the automatic stack drive.
func stackDriveEngineWithGit(t *testing.T, git delivery.GitRunner) (*localengine.Engine, *agenttools.Service, *storage.SQLite, string) {
	t.Helper()
	root := writeStackDriveWorkspace(t)
	repo := workflowledger.NewMemoryRepository()
	db, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	engine := &localengine.Engine{
		WorkspaceRoot: root,
		Repo:          repo,
		Store:         db,
		PR:            &recordingPR{},
		Git:           git,
		NewRunner: func() controller.AgentStepRunner {
			return &scriptedAttemptRunner{byStepCall: map[string]json.RawMessage{
				"plan":      json.RawMessage(`{"summary":"p"}`),
				"decompose": json.RawMessage(stackValidMultiPlan),
				"implement": json.RawMessage(`{"summary":"s"}`),
			}}
		},
	}
	return engine, mustService(t, engine, repo), db, root
}

// refusingStackGit permanently refuses every delivery git command with a
// delivery.RefusalError - the engine's permanent-refusal outcome, where the
// run CASes to delivery_failed and Deliver returns a NIL error - and counts
// delivery attempts for the no-re-delivery assertions.
type refusingStackGit struct {
	mu      sync.Mutex
	attempt int
}

func (g *refusingStackGit) Run(context.Context, delivery.GitContext, ...string) (string, error) {
	g.mu.Lock()
	g.attempt++
	g.mu.Unlock()
	return "", &delivery.RefusalError{Reason: "test: stack drive git refusal"}
}

func (g *refusingStackGit) attempts() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.attempt
}

// plainFaultStackGit fails every delivery git command with a plain, non-
// refusal error - the engine's transient-fault outcome, where the run stays
// delivery_pending and the drive retries - and records attempt timestamps for
// the backoff assertions.
type plainFaultStackGit struct {
	mu    sync.Mutex
	calls []time.Time
}

func (g *plainFaultStackGit) Run(context.Context, delivery.GitContext, ...string) (string, error) {
	g.mu.Lock()
	g.calls = append(g.calls, time.Now())
	g.mu.Unlock()
	return "", fmt.Errorf("test: transient stack drive git fault")
}

func (g *plainFaultStackGit) timestamps() []time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]time.Time(nil), g.calls...)
}

// TestStackDriveRefusedDeliveryDoesNotMarkPublished pins STACK-1: the
// engine's Deliver returns a nil error for a permanent refusal too (the run
// CASes to delivery_failed), so the drive must not treat derr==nil as success
// and mark the chunk published - it applies the bounded reopen-or-fail
// decision instead, and never re-delivers the failed run. Before the fix a
// refused delivery wedged the stack in published-never-merged while the drive
// re-polled forever.
func TestStackDriveRefusedDeliveryDoesNotMarkPublished(t *testing.T) {
	git := &refusingStackGit{}
	engine, svc, db, _ := stackDriveEngineWithGit(t, git)
	started := startStackingRunFor(t, svc, "stack-drive-me", nil)
	waitRun(t, engine, started.RunID)

	ledger := tasks.NewStore(db)
	waitChunkReopened(t, ledger, started.RunID)
	if got := git.attempts(); got != 1 {
		t.Fatalf("delivery attempts = %d, want exactly 1 (the refused attempt)", got)
	}
	// The refused run settled delivery_failed and the task reopened with one
	// attempt recorded in the durable transition journal.
	if task := mustStackChunk(t, ledger, started.RunID, "c1"); task.Status != stacking.StatusReopened {
		t.Fatalf("chunk c1 = %+v, want reopened", task)
	}
	if reopens := countStackReopens(t, ledger, started.RunID, "c1"); reopens != 1 {
		t.Fatalf("chunk c1 reopen transitions = %d, want 1", reopens)
	}
	assertChunkRunSettled(t, engine, started.RunID)

	// STACK-2: a delivery_failed chunk is never blindly re-delivered - the
	// drive halts instead of burning a delivery attempt every poll tick.
	after := git.attempts()
	<-time.After(2500 * time.Millisecond)
	if got := git.attempts(); got != after {
		t.Fatalf("delivery attempts grew after the refusal: %d -> %d; a delivery_failed chunk must not be re-delivered", after, got)
	}

	// The plan run stays delivery_pending: the stack is resumable, not
	// settled against a wedged chunk.
	if view := stackStatusView(t, svc, started.RunID); view.Status != "delivery_pending" {
		t.Fatalf("plan run status = %q, want delivery_pending", view.Status)
	}
}

// waitChunkReopened polls the task map until chunk c1 of the stack is
// reopened after a refused delivery, failing immediately if it is ever
// marked published (STACK-1) or if the deadline passes.
func waitChunkReopened(t *testing.T, ledger *tasks.Store, stackID string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		byID, err := stacking.TaskMap(context.Background(), ledger, stackID)
		if err != nil {
			t.Fatal(err)
		}
		task, ok := byID["c1"]
		if !ok {
			if time.Now().After(deadline) {
				t.Fatalf("chunk c1 never seeded: %+v", byID)
			}
			<-time.After(100 * time.Millisecond)
			continue
		}
		if task.Status == stacking.StatusPublished {
			t.Fatalf("chunk c1 was marked published by a refused delivery")
		}
		if task.Status == stacking.StatusReopened {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("chunk c1 never reopened after the refused delivery (last status %q): %+v", task.Status, byID)
		}
		<-time.After(100 * time.Millisecond)
	}
}

// mustStackChunk returns a chunk's current task map entry.
func mustStackChunk(t *testing.T, ledger *tasks.Store, stackID, chunkID string) tasks.Task {
	t.Helper()
	byID, err := stacking.TaskMap(context.Background(), ledger, stackID)
	if err != nil {
		t.Fatal(err)
	}
	task, ok := byID[chunkID]
	if !ok {
		t.Fatalf("chunk %s missing from stack %s: %+v", chunkID, stackID, byID)
	}
	return task
}

// assertChunkRunSettled asserts the stack's admitted chunk run (the plan run
// and the never-admitted integration run are excluded by the key shape)
// settled delivery_failed after the refused delivery.
func assertChunkRunSettled(t *testing.T, engine *localengine.Engine, stackID string) {
	t.Helper()
	runs, err := engine.Repo.ListRuns(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range runs {
		if strings.HasPrefix(r.InvocationKey, stackID+":") && !strings.HasSuffix(r.InvocationKey, ":"+stacking.IntegrationChunkID) {
			found = true
			if r.Status != workflowledger.RunStatusDeliveryFailed {
				t.Fatalf("chunk run %s (key %s) = %q, want delivery_failed after the refusal", r.RunID, r.InvocationKey, r.Status)
			}
		}
	}
	if !found {
		t.Fatalf("no chunk run admitted under stack %s: %+v", stackID, runs)
	}
}

// TestStackDriveTransientFaultBackoffBoundsRetries pins STACK-2: a chunk
// whose delivery keeps failing with transient faults stays delivery_pending
// and is retried, but the drive doubles the wait between passes (2s -> 4s ->
// 8s -> ... cap 30s) instead of burning one delivery attempt every poll tick.
// The observed attempt gaps must grow apart: gap2 > gap1 with the real 2s
// base (pre-fix every attempt was exactly one poll interval apart).
func TestStackDriveTransientFaultBackoffBoundsRetries(t *testing.T) {
	git := &plainFaultStackGit{}
	engine, svc, _, _ := stackDriveEngineWithGit(t, git)
	started := startStackingRunFor(t, svc, "stack-drive-me", nil)
	waitRun(t, engine, started.RunID)

	// Attempts land at ~t0, t0+2s, t0+6s under the backoff schedule; a
	// pre-fix drive retried every 2s.
	deadline := time.Now().Add(15 * time.Second)
	for {
		if len(git.timestamps()) >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("delivery attempts never reached 3 in 15s (the drive must keep retrying a delivery_pending chunk): %v", git.timestamps())
		}
		<-time.After(100 * time.Millisecond)
	}
	ts := git.timestamps()
	gap1 := ts[1].Sub(ts[0])
	gap2 := ts[2].Sub(ts[1])
	if gap2 <= gap1 {
		t.Fatalf("delivery attempt gaps did not grow: %v then %v (want gap2 > gap1); the drive must back off, not retry every poll tick", gap1, gap2)
	}
	if n := len(ts); n > 4 {
		t.Fatalf("delivery attempts = %d in the observation window, want <= 4 (bounded by the backoff schedule): %v", n, ts)
	}
}

// integrationFaultStackGit wraps the real git runner and fails every delivery
// command for one pinned worktree directory (the integration run's), letting
// the chunk deliveries succeed so the drive reaches finishStack; it records
// the failing attempt timestamps for the backoff assertions.
type integrationFaultStackGit struct {
	inner   delivery.GitRunner
	failDir string
	mu      sync.Mutex
	calls   []time.Time
}

func (g *integrationFaultStackGit) Run(ctx context.Context, gc delivery.GitContext, args ...string) (string, error) {
	if gc.Dir == g.failDir {
		g.mu.Lock()
		g.calls = append(g.calls, time.Now())
		g.mu.Unlock()
		return "", fmt.Errorf("test: transient integration delivery git fault")
	}
	return g.inner.Run(ctx, gc, args...)
}

func (g *integrationFaultStackGit) timestamps() []time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]time.Time(nil), g.calls...)
}

func (g *integrationFaultStackGit) setFailDir(dir string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failDir = dir
}

// TestStackDriveIntegrationFaultBackoffBoundsRetries pins the STACK-2 backoff
// at the finishStack site: a transiently faulting delivery of the final
// integration run must not burn one attempt every poll tick. Chunk deliveries
// succeed (real git) so the drive reaches finishStack; the fake then fails
// only the integration run's worktree, and the observed attempt gaps must
// grow (2s -> 4s -> ...) instead of staying flat.
func TestStackDriveIntegrationFaultBackoffBoundsRetries(t *testing.T) {
	git := &integrationFaultStackGit{inner: &delivery.RealGit{}}
	engine, svc, _, root := stackDriveEngineWithGit(t, git)
	started := startStackingRunFor(t, svc, "stack-drive-me", nil)
	waitRun(t, engine, started.RunID)

	// The stack drives to completion (chunk deliveries succeed) and
	// finishStack admits the integration run; its first delivery attempt is
	// at least one poll interval later, so pinning the fault dir on
	// admission is race-free.
	runID := waitIntegrationRun(t, engine, started.RunID)
	git.setFailDir(filepath.Join(root, ".mivia", "worktrees", "workflow-"+runID))

	deadline := time.Now().Add(20 * time.Second)
	for {
		if len(git.timestamps()) >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("integration delivery attempts never reached 3 in 20s: %v", git.timestamps())
		}
		<-time.After(100 * time.Millisecond)
	}
	ts := git.timestamps()
	gap1 := ts[1].Sub(ts[0])
	gap2 := ts[2].Sub(ts[1])
	if gap2 <= gap1 {
		t.Fatalf("integration delivery attempt gaps did not grow: %v then %v (want gap2 > gap1)", gap1, gap2)
	}
}

// waitIntegrationRun polls the repo until the stack's final integration run
// is admitted and returns its run id.
func waitIntegrationRun(t *testing.T, engine *localengine.Engine, stackID string) string {
	t.Helper()
	want := stackID + ":" + stacking.IntegrationChunkID
	deadline := time.Now().Add(30 * time.Second)
	for {
		runs, err := engine.Repo.ListRuns(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range runs {
			if r.InvocationKey == want {
				return r.RunID
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("integration run never admitted for stack %s: %+v", stackID, runs)
		}
		<-time.After(100 * time.Millisecond)
	}
}

// countStackReopens counts the durable reopened transitions recorded for a
// chunk (the journal, not the TaskMap Attempts field, is authoritative).
func countStackReopens(t *testing.T, ledger *tasks.Store, stackID, chunkID string) int {
	t.Helper()
	trs, err := ledger.ListTransitions(stackID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, tr := range trs {
		if tr.TaskID == chunkID && tr.ToStatus == stacking.StatusReopened {
			n++
		}
	}
	return n
}
