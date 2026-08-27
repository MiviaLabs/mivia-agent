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
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/localengine"
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
	// Placeholder templates for the engine-synthesized steps. Their content
	// is unused by the scripted runner, but admission pins them so resume
	// cannot be altered by a changed workspace file.
	templatesDir := filepath.Join(wfRoot, "templates")
	if err := os.MkdirAll(templatesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"decompose.md":           "synthetic decompose template",
		"chunk-plan-validate.md": "synthetic chunk plan validate template",
	} {
		if err := os.WriteFile(filepath.Join(templatesDir, name), []byte(body), 0o600); err != nil {
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
func stackDriveEngine(t *testing.T) (*localengine.Engine, *workflowledger.Service, *storage.SQLite) {
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
func stackDriveEngineNoStore(t *testing.T) (*localengine.Engine, *workflowledger.Service) {
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
// ciDeadline scales a wall-clock test budget for the platform it runs on:
// windows-latest CI runners execute the real git and SQLite steps in the
// stack-drive suites an order of magnitude slower than the Linux baseline
// these constants were measured on. Non-Windows budgets stay tight.
func ciDeadline(d time.Duration) time.Duration {
	if runtime.GOOS == "windows" {
		return 3 * d
	}
	return d
}

func waitPlanRunStatus(t *testing.T, svc *workflowledger.Service, runID, want string, timeout time.Duration) {
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
	want := stackID + ":" + delivery.IntegrationChunkID
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
	waitPlanRunStatus(t, svc, started.RunID, "succeeded", ciDeadline(60*time.Second))

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
		if strings.HasSuffix(key, ":"+delivery.IntegrationChunkID) {
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
	ledger := workflowledger.NewStore(db)
	byID, err := delivery.TaskMap(context.Background(), ledger, started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"c1", "c2"} {
		task, ok := byID[id]
		if !ok || task.Status != delivery.StatusMerged {
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

	ledger := workflowledger.NewStore(db)
	deadline := time.Now().Add(ciDeadline(30 * time.Second))
	for {
		byID, err := delivery.TaskMap(context.Background(), ledger, started.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if task, ok := byID["c1"]; ok && task.Status == delivery.StatusReviewed {
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
	waitPlanRunStatus(t, svc, started.RunID, "delivery_pending", ciDeadline(60*time.Second))

	// Wait for the ledger to show the stack drove to completion: every chunk
	// merged AND the integration run settled (stackDriveCompleted requires
	// both; the chunks-merged wait alone races the drive's final
	// integration-run settle).
	ledger := workflowledger.NewStore(db)
	deadline := time.Now().Add(ciDeadline(30 * time.Second))
	for {
		byID, err := delivery.TaskMap(context.Background(), ledger, started.RunID)
		if err != nil {
			t.Fatal(err)
		}
		merged := 0
		for _, id := range []string{"c1", "c2"} {
			if task, ok := byID[id]; ok && task.Status == delivery.StatusMerged {
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
	waitPlanRunStatus(t, svc, started.RunID, "succeeded", ciDeadline(30*time.Second))
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
	<-time.After(ciDeadline(4 * time.Second))
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
func stackDriveEngineWithGit(t *testing.T, git delivery.GitRunner) (*localengine.Engine, *workflowledger.Service, *storage.SQLite, string) {
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

	ledger := workflowledger.NewStore(db)
	waitChunkReopened(t, ledger, started.RunID)
	if got := git.attempts(); got != 1 {
		t.Fatalf("delivery attempts = %d, want exactly 1 (the refused attempt)", got)
	}
	// The refused run settled delivery_failed and the task reopened with one
	// attempt recorded in the durable transition journal.
	if task := mustStackChunk(t, ledger, started.RunID, "c1"); task.Status != delivery.StatusReopened {
		t.Fatalf("chunk c1 = %+v, want reopened", task)
	}
	if reopens := countStackReopens(t, ledger, started.RunID, "c1"); reopens != 1 {
		t.Fatalf("chunk c1 reopen transitions = %d, want 1", reopens)
	}
	assertChunkRunSettled(t, engine, started.RunID)

	// STACK-2: a delivery_failed chunk is never blindly re-delivered - the
	// drive halts instead of burning a delivery attempt every poll tick.
	after := git.attempts()
	<-time.After(ciDeadline(2500 * time.Millisecond))
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
func waitChunkReopened(t *testing.T, ledger *workflowledger.Store, stackID string) {
	t.Helper()
	deadline := time.Now().Add(ciDeadline(30 * time.Second))
	for {
		byID, err := delivery.TaskMap(context.Background(), ledger, stackID)
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
		if task.Status == delivery.StatusPublished {
			t.Fatalf("chunk c1 was marked published by a refused delivery")
		}
		if task.Status == delivery.StatusReopened {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("chunk c1 never reopened after the refused delivery (last status %q): %+v", task.Status, byID)
		}
		<-time.After(100 * time.Millisecond)
	}
}

// mustStackChunk returns a chunk's current task map entry.
func mustStackChunk(t *testing.T, ledger *workflowledger.Store, stackID, chunkID string) workflowledger.Task {
	t.Helper()
	byID, err := delivery.TaskMap(context.Background(), ledger, stackID)
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
		if strings.HasPrefix(r.InvocationKey, stackID+":") && !strings.HasSuffix(r.InvocationKey, ":"+delivery.IntegrationChunkID) {
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
	deadline := time.Now().Add(ciDeadline(15 * time.Second))
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

	deadline := time.Now().Add(ciDeadline(20 * time.Second))
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
	want := stackID + ":" + delivery.IntegrationChunkID
	deadline := time.Now().Add(ciDeadline(30 * time.Second))
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
func countStackReopens(t *testing.T, ledger *workflowledger.Store, stackID, chunkID string) int {
	t.Helper()
	trs, err := ledger.ListTransitions(stackID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, tr := range trs {
		if tr.TaskID == chunkID && tr.ToStatus == delivery.StatusReopened {
			n++
		}
	}
	return n
}

// unmergedStackPR is a PR oracle that behaves like a real host that never
// merges: FindByHead misses until a PR is created for the head, then returns
// the open (Draft matching the delivery mode) PR; IsMerged is always false.
// FindByHead calls are counted, so tests can pin both the delivery gate's
// merge confirmation (H-1) and the drive's unmerged-PR polling (P-1).
type unmergedStackPR struct {
	mu      sync.Mutex
	finds   map[string]int
	created map[string]delivery.PRRef
}

func (p *unmergedStackPR) FindByHead(_ context.Context, _, head string) (*delivery.PRRef, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finds == nil {
		p.finds = map[string]int{}
	}
	p.finds[head]++
	if ref, ok := p.created[head]; ok {
		r := ref
		return &r, nil
	}
	return nil, nil
}

func (p *unmergedStackPR) IsMerged(context.Context, string, string) (bool, error) {
	return false, nil
}

func (p *unmergedStackPR) Create(_ context.Context, _ string, in delivery.PRInput) (delivery.PRRef, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.created == nil {
		p.created = map[string]delivery.PRRef{}
	}
	ref := delivery.PRRef{
		RemoteID: "pr-open-" + strconv.Itoa(len(p.created)+1),
		URL:      "https://example.com/pull/" + strconv.Itoa(len(p.created)+1),
		Draft:    in.Draft,
	}
	p.created[in.Head] = ref
	return ref, nil
}

// preCreate records an already-open PR for a head, the H-1 seeded integration
// PR that predates this engine instance (the oracle can see it, it is just
// never merged).
func (p *unmergedStackPR) preCreate(head string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.created == nil {
		p.created = map[string]delivery.PRRef{}
	}
	p.created[head] = delivery.PRRef{RemoteID: "pr-open", URL: "https://example.com/pull/open", Draft: true}
}

func (p *unmergedStackPR) findCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, c := range p.finds {
		n += c
	}
	return n
}

// TestEngineStackGateRefusesPublishWhenIntegrationPRUnmerged pins H-1: the
// drive-before-delivery gate (stackDriveCompleted) must confirm, under
// merge_policy=auto, that the succeeded integration run's PR actually merged
// before an explicit workflow_deliver may publish the plan run. The seeded
// state is exactly what a crash between the drive's integration delivery and
// its merge wait leaves behind: every chunk merged, the integration run
// settled succeeded with durable pushed evidence (CommitSHA +
// WorktreeName/RemoteURL), and the PR oracle reports the final PR still open.
// Pre-fix the gate passed on the succeeded status alone and the publish
// proceeded (settling the plan run delivery_failed on the missing worktree
// instead of refusing); post-fix the gate refuses and the run stays
// delivery_pending (resumable). The negative path pins that the same durable
// state under the approve (grant) policy must still pass the gate.
func TestEngineStackGateRefusesPublishWhenIntegrationPRUnmerged(t *testing.T) {
	t.Run("auto refuses publish while the integration PR is open", func(t *testing.T) {
		engine, _, _, stackID := seedFullyDrivenStackWithOpenIntegrationPR(t, "auto")
		_, err := engine.Deliver(context.Background(), stackID, true)
		if err == nil || !strings.Contains(err.Error(), "has not fully driven") {
			t.Fatalf("Deliver on a fully driven stack with an unmerged integration PR = %v, want the drive-before-delivery gate refusal", err)
		}
		// The refused publish must not have moved the run.
		run, err := engine.Repo.GetRun(context.Background(), stackID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != workflowledger.RunStatusDeliveryPending {
			t.Fatalf("plan run status after the refused deliver = %q, want delivery_pending", run.Status)
		}
	})
	t.Run("approve policy passes the gate on the same durable state", func(t *testing.T) {
		engine, _, _, stackID := seedFullyDrivenStackWithOpenIntegrationPR(t, "approve")
		_, err := engine.Deliver(context.Background(), stackID, true)
		if err != nil && strings.Contains(err.Error(), "has not fully driven") {
			t.Fatalf("Deliver under approve policy on a fully driven stack = %v, want the gate to pass (no undriven refusal)", err)
		}
	})
}

// seedFullyDrivenStackWithOpenIntegrationPR builds a multi-chunk stack whose
// drive reached completion except for the final integration merge: the task
// ledger shows every chunk merged, the integration run settled succeeded with
// durable pushed evidence, and the PR oracle reports the integration PR still
// open. The plan run parks at delivery_pending (deliver_plan_run=true).
func seedFullyDrivenStackWithOpenIntegrationPR(t *testing.T, mergePolicy string) (*localengine.Engine, workflowledger.Repository, *storage.SQLite, string) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	repo := workflowledger.NewMemoryRepository()
	db, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	workflowName := "stack-drive-me"
	if mergePolicy == "approve" {
		workflowName = "stack-approve-me"
	}
	oracle := &unmergedStackPR{}
	engine := &localengine.Engine{
		WorkspaceRoot: root,
		Repo:          repo,
		Store:         db,
		PR:            oracle,
	}
	stackID := "wfr-h1-open-pr"
	toml := stackDriveWorkflowTOML(workflowName, mergePolicy, true)
	// The seeded integration PR predates this engine instance: register it on
	// the oracle so FindByHead can see the open PR (it is just never merged).
	oracle.preCreate("wf/wt-integration")
	snapshot := workflowledger.Snapshot{
		SchemaVersion:    1,
		DefinitionTOML:   []byte(toml),
		DefinitionDigest: workflowledger.DigestHex([]byte(toml)),
		Inputs:           map[string]string{"task": "build"},
	}
	snapshotJSON, err := workflowledger.MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	seedDeliveryPendingRun(t, repo, workflowledger.RunSnapshot{RunID: stackID, WorkflowName: workflowName, BaseRef: "main"}, snapshotJSON)

	// The plan run's succeeded decompose output: the chunk plan the drive and
	// the gate both read.
	seedSucceededDecomposeAttempt(t, repo, stackID, []byte(stackValidMultiPlan))

	_, chunks, _, _, err := delivery.ParseStackPlanOutput([]byte(stackValidMultiPlan))
	if err != nil || len(chunks) != 2 {
		t.Fatalf("parse stack plan = %v, %v; want 2 chunks", chunks, err)
	}
	ledger := workflowledger.NewStore(db)
	if err := delivery.SeedStackLedger(ctx, ledger, stackID, chunks); err != nil {
		t.Fatal(err)
	}
	for _, c := range chunks {
		if err := ledger.TransitionTask(stackID, c.ID, delivery.StatusMerged); err != nil {
			t.Fatalf("transition chunk %s to merged: %v", c.ID, err)
		}
	}

	// The final integration run: <stack>:integration, settled succeeded, with
	// a durable pushed delivery record and an open PR the oracle can see.
	intRunID := stackID + "-integration"
	seedDeliveryPendingRun(t, repo, workflowledger.RunSnapshot{
		RunID: intRunID, InvocationKey: stackID + ":" + delivery.IntegrationChunkID,
		WorkflowName: workflowName, BaseRef: "main",
		WorktreeName: "wt-integration", RemoteURL: "https://github.com/acme/stack.git",
	}, snapshotJSON)
	setRunStatus(t, repo, intRunID, workflowledger.RunStatusSucceeded)
	if err := repo.UpsertDelivery(ctx, workflowledger.DeliveryRecord{
		RunID: intRunID, IdempotencyKey: "k1", Mode: "draft", BaseRef: "main",
		HeadRef: "wf/wt-integration", CommitSHA: "abc123", Status: "pushed", UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	return engine, repo, db, stackID
}

// seedDeliveryPendingRun admits a run row and settles it to delivery_pending
// through the valid pending -> running -> delivery_pending transitions.
func seedDeliveryPendingRun(t *testing.T, repo workflowledger.Repository, run workflowledger.RunSnapshot, snapshotJSON []byte) {
	t.Helper()
	run.Status = workflowledger.RunStatusPending
	if err := repo.CreateRun(context.Background(), run, snapshotJSON); err != nil {
		t.Fatal(err)
	}
	step := func(to workflowledger.RunStatus) {
		t.Helper()
		stored, err := repo.GetRun(context.Background(), run.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.CompareAndSetRunStatus(context.Background(), run.RunID, stored.Version, to, nil); err != nil {
			t.Fatal(err)
		}
	}
	step(workflowledger.RunStatusRunning)
	step(workflowledger.RunStatusDeliveryPending)
}

// setRunStatus moves an existing run to the requested status through valid
// transitions, routing through running when no direct edge exists.
func setRunStatus(t *testing.T, repo workflowledger.Repository, runID string, want workflowledger.RunStatus) {
	t.Helper()
	ctx := context.Background()
	for {
		stored, err := repo.GetRun(ctx, runID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.Status == want {
			return
		}
		next := want
		if !workflowledger.ValidRunTransition(stored.Status, want) {
			next = workflowledger.RunStatusRunning
		}
		if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, next, nil); err != nil {
			t.Fatal(err)
		}
	}
}

// seedSucceededDecomposeAttempt records a succeeded decompose step attempt
// with a stored output on the given run, the shape delivery.LoadStackPlanOutput
// reads.
func seedSucceededDecomposeAttempt(t *testing.T, repo workflowledger.Repository, runID string, output []byte) {
	t.Helper()
	ctx := context.Background()
	ref := "sha256:" + workflowledger.DigestHex(output)
	if err := repo.StoreContent(ctx, ref, output); err != nil {
		t.Fatal(err)
	}
	attempt := workflowledger.StepAttempt{AttemptID: "wfa-decompose-1", RunID: runID, StepID: delivery.DecomposeStepID, AttemptNo: 1}
	if err := repo.RecordStepAttemptOutcome(ctx, attempt, workflowledger.AttemptOutcome{
		Status: workflowledger.AttemptStatusSucceeded, OutputRef: ref, OutputDigest: workflowledger.DigestHex(output),
	}); err != nil {
		t.Fatal(err)
	}
}

// stackDriveWriteRunner settles every agent step like the scripted runner
// but, on implement, writes a REAL file into the run's worktree so delivery
// has a diff to commit and push - a pushed PR the merge oracle can poll - and
// the stack reaches the published-never-merged state, mirroring the CLI
// fixture's stackITRunner. Without it a chunk delivery records no_diff and the
// stack completes without ever opening a PR.
type stackDriveWriteRunner struct {
	root string
	repo workflowledger.Repository

	mu    sync.Mutex
	calls map[string]int
}

func (r *stackDriveWriteRunner) RunStep(ctx context.Context, req controller.AgentStepRequest) (controller.AgentStepResult, error) {
	r.mu.Lock()
	if r.calls == nil {
		r.calls = map[string]int{}
	}
	r.calls[req.StepID]++
	r.mu.Unlock()
	out := json.RawMessage(`{"ok":true}`)
	switch req.StepID {
	case "plan":
		out = json.RawMessage(`{"summary":"p"}`)
	case "decompose":
		out = json.RawMessage(stackValidMultiPlan)
	case "chunk_plan_validate":
		out = json.RawMessage(`{"valid":true,"reasons":[]}`)
	case "implement":
		if err := r.writeChunkFile(ctx, req); err != nil {
			return controller.AgentStepResult{}, err
		}
		out = json.RawMessage(`{"summary":"implemented"}`)
	}
	return controller.AgentStepResult{
		CoordinatorRunID: "coord-" + req.StepID + "-" + strconv.Itoa(req.AttemptNo),
		TaskID:           req.TaskID,
		Output:           out,
		EvidenceJSON:     []byte(`[]`),
	}, nil
}

// writeChunkFile writes the run's change into its real worktree so delivery
// publishes a real diff. The chunk plan declares the exact file each chunk
// may touch (delivery's diff-size gate enforces the declared slice), so the
// runner writes that declared file, mirroring the CLI stackITRunner.
func (r *stackDriveWriteRunner) writeChunkFile(ctx context.Context, req controller.AgentStepRequest) error {
	run, err := r.repo.GetRun(ctx, req.WorkflowRunID)
	if err != nil {
		return err
	}
	wt, err := vcs.Resolve(ctx, r.root, run.WorktreeName)
	if err != nil || wt == nil {
		return fmt.Errorf("resolve worktree %q: %w", run.WorktreeName, err)
	}
	chunk, _ := req.Inputs["chunk"].(string)
	file := "a.go"
	if chunk == "c2" {
		file = "b.go"
	}
	if chunk == "" {
		file = "integration.txt"
	}
	return os.WriteFile(filepath.Join(wt.Path, file), []byte("implemented "+chunk+"\n"), 0o600)
}

// TestEngineStackDriveAfterParkInterruptible pins P-1: the run's active
// handle must survive the controller's park at delivery_pending for the whole
// lifetime of the automatic stack drive, so Interrupt can cancel the drive's
// unmerged-PR poll. Pre-fix launch dropped the entry the moment the controller
// parked, so Interrupt answered "not active in this engine" and the drive
// polled a never-merging PR oracle forever. The drive keeps the run registered
// and defers cancel()/releaseActiveRun on exit; Interrupt then stops the poll.
func TestEngineStackDriveAfterParkInterruptible(t *testing.T) {
	root := writeStackDriveWorkspace(t)
	repo := workflowledger.NewMemoryRepository()
	db, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	oracle := &unmergedStackPR{}
	engine := &localengine.Engine{
		WorkspaceRoot: root,
		Repo:          repo,
		Store:         db,
		PR:            oracle,
		NewRunner: func() controller.AgentStepRunner {
			return &stackDriveWriteRunner{root: root, repo: repo}
		},
	}
	svc := mustService(t, engine, repo)
	started := startStackingRunFor(t, svc, "stack-drive-me", nil)
	waitRun(t, engine, started.RunID)

	// The drive delivers the first wave with a real diff, opens a PR, and
	// then polls the oracle waiting for a merge that never comes.
	deadline := time.Now().Add(60 * time.Second)
	for oracle.findCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the drive never polled the merge oracle; the stack did not reach the published-never-merged state")
		}
		<-time.After(100 * time.Millisecond)
	}

	// Pre-fix: Interrupt refuses because launch dropped the handle when the
	// controller parked. Post-fix the drive keeps the run registered, so
	// Interrupt cancels the poll.
	if err := engine.Interrupt(started.RunID); err != nil {
		t.Fatalf("Interrupt on the parked, drive-polling run: %v", err)
	}
	// Let any in-flight pass observe the cancellation, then verify the poll
	// truly stopped (the pre-fix leak kept polling forever).
	<-time.After(ciDeadline(2500 * time.Millisecond))
	before := oracle.findCount()
	<-time.After(3500 * time.Millisecond) // more than one 2s poll interval
	if got := oracle.findCount(); got != before {
		t.Fatalf("merge-oracle polls kept growing after Interrupt: %d -> %d; the drive must be cancelled", before, got)
	}
	// The plan run stays non-terminal (resumable), as Interrupt guarantees.
	if run := getRunInterrupt(t, repo, started.RunID, 0); workflowledger.IsTerminalRunStatus(run.Status) {
		t.Fatalf("plan run status after Interrupt = %q, want non-terminal", run.Status)
	}
}
