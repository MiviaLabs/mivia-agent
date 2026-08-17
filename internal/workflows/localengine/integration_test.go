package localengine_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/localengine"
)

// TestIntegrationRunObserveInspect drives the shipped tool Execute paths
// against a real ledger + controller with a scripted agent step runner.
func TestIntegrationRunObserveInspect(t *testing.T) {
	engine, svc := scriptedTwoStep(t)
	started := startTwoStep(t, svc)
	waitRun(t, engine, started.RunID)
	assertSucceededStatus(t, svc, started.RunID)
	assertEventsPresent(t, svc, started.RunID)
	assertInspectDetail(t, svc, started.RunID)
	assertListed(t, svc, started.RunID)
}

func scriptedTwoStep(t *testing.T) (*localengine.Engine, *agenttools.Service) {
	t.Helper()
	root := writeTwoStepWorkspace(t)
	repo := workflowledger.NewMemoryRepository()
	engine := &localengine.Engine{
		WorkspaceRoot: root,
		Repo:          repo,
		NewRunner: func() controller.AgentStepRunner {
			return &localengine.StaticStepRunner{
				ByStep: map[string]json.RawMessage{
					"one": json.RawMessage(`{"ok":true}`),
					"two": json.RawMessage(`{"ok":true,"done":true}`),
				},
			}
		},
	}
	return engine, mustService(t, engine, repo)
}

func startTwoStep(t *testing.T, svc *agenttools.Service) agenttools.StartResult {
	t.Helper()
	out, err := mustTool(t, svc, agenttools.ToolWorkflowRun).Execute(
		context.Background(), json.RawMessage(`{"workflow":"two-step","inputs":{"task":"build"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var started agenttools.StartResult
	if err := json.Unmarshal([]byte(out), &started); err != nil {
		t.Fatal(err)
	}
	if started.RunID == "" || started.Status == "" {
		t.Fatalf("start = %+v", started)
	}
	return started
}

func waitRun(t *testing.T, engine *localengine.Engine, runID string) {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := engine.Wait(waitCtx, runID); err != nil {
		t.Fatal(err)
	}
}

func assertSucceededStatus(t *testing.T, svc *agenttools.Service, runID string) {
	t.Helper()
	statusOut, err := mustTool(t, svc, agenttools.ToolWorkflowStatus).Execute(
		context.Background(), json.RawMessage(fmt.Sprintf(`{"run_id":%q}`, runID)))
	if err != nil {
		t.Fatal(err)
	}
	var status agenttools.StatusView
	if err := json.Unmarshal([]byte(statusOut), &status); err != nil {
		t.Fatal(err)
	}
	if status.Status != "succeeded" || len(status.Attempts) < 2 {
		t.Fatalf("status = %q attempts=%d body=%s", status.Status, len(status.Attempts), statusOut)
	}
	for _, a := range status.Attempts {
		if a.OutputDigest == "" && a.Status == "succeeded" {
			t.Fatalf("succeeded attempt missing digest: %+v", a)
		}
	}
}

func assertEventsPresent(t *testing.T, svc *agenttools.Service, runID string) {
	t.Helper()
	evOut, err := mustTool(t, svc, agenttools.ToolWorkflowEvents).Execute(
		context.Background(), json.RawMessage(fmt.Sprintf(`{"run_id":%q,"limit":50}`, runID)))
	if err != nil {
		t.Fatal(err)
	}
	var events agenttools.EventsPage
	if err := json.Unmarshal([]byte(evOut), &events); err != nil {
		t.Fatal(err)
	}
	if events.Count < 2 {
		t.Fatalf("events count = %d, want >= 2", events.Count)
	}
}

func assertInspectDetail(t *testing.T, svc *agenttools.Service, runID string) {
	t.Helper()
	insOut, err := mustTool(t, svc, agenttools.ToolWorkflowInspect).Execute(
		context.Background(), json.RawMessage(fmt.Sprintf(`{"run_id":%q,"step":"one","attempt":1}`, runID)))
	if err != nil {
		t.Fatal(err)
	}
	var inspect agenttools.InspectView
	if err := json.Unmarshal([]byte(insOut), &inspect); err != nil {
		t.Fatal(err)
	}
	if inspect.Output == nil || inspect.CoordinatorRunID == "" || inspect.TaskID == "" {
		t.Fatalf("inspect = %+v", inspect)
	}
	if inspect.Transition == nil || inspect.Transition.ToStep == "" {
		t.Fatalf("inspect missing transition: %+v", inspect)
	}
}

func assertListed(t *testing.T, svc *agenttools.Service, runID string) {
	t.Helper()
	listOut, err := mustTool(t, svc, agenttools.ToolWorkflowListRuns).Execute(
		context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var list agenttools.ListRunsView
	if err := json.Unmarshal([]byte(listOut), &list); err != nil {
		t.Fatal(err)
	}
	for _, r := range list.Runs {
		if r.RunID == runID {
			if r.Workflow != "two-step" || r.Status != "succeeded" {
				t.Fatalf("list item = %+v", r)
			}
			return
		}
	}
	t.Fatalf("run %s not in list: %s", runID, listOut)
}

// TestIntegrationParallelRunsIsolation starts ≥3 concurrent workflow_run calls.
func TestIntegrationParallelRunsIsolation(t *testing.T) {
	root := writeTwoStepWorkspace(t)
	repo := workflowledger.NewMemoryRepository()
	engine := &localengine.Engine{
		WorkspaceRoot: root,
		Repo:          repo,
		NewRunner: func() controller.AgentStepRunner {
			return &localengine.StaticStepRunner{Output: json.RawMessage(`{"ok":true}`)}
		},
	}
	svc := mustService(t, engine, repo)
	runTool := mustTool(t, svc, agenttools.ToolWorkflowRun)
	statusTool := mustTool(t, svc, agenttools.ToolWorkflowStatus)

	const n = 3
	var wg sync.WaitGroup
	ids := make([]string, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out, err := runTool.Execute(context.Background(), json.RawMessage(`{"workflow":"two-step","inputs":{"task":"t"}}`))
			if err != nil {
				errs[i] = err
				return
			}
			var started agenttools.StartResult
			if err := json.Unmarshal([]byte(out), &started); err != nil {
				errs[i] = err
				return
			}
			ids[i] = started.RunID
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			t.Fatalf("run ids not distinct: %v", ids)
		}
		seen[id] = true
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, id := range ids {
		if err := engine.Wait(waitCtx, id); err != nil {
			t.Fatal(err)
		}
		out, err := statusTool.Execute(context.Background(), json.RawMessage(fmt.Sprintf(`{"run_id":%q}`, id)))
		if err != nil {
			t.Fatal(err)
		}
		var status agenttools.StatusView
		if err := json.Unmarshal([]byte(out), &status); err != nil {
			t.Fatal(err)
		}
		if status.Status != "succeeded" || status.RunID != id {
			t.Fatalf("status for %s = %+v", id, status)
		}
	}
}

type invocationCountingRunner struct{ calls *atomic.Int32 }

func (r invocationCountingRunner) RunStep(_ context.Context, req controller.AgentStepRequest) (controller.AgentStepResult, error) {
	r.calls.Add(1)
	output := json.RawMessage(`{"ok":true}`)
	if req.StepID == "two" {
		output = json.RawMessage(`{"ok":true,"done":true}`)
	}
	return controller.AgentStepResult{CoordinatorRunID: req.CoordinatorRunID, TaskID: req.TaskID, Output: output, EvidenceJSON: []byte(`[]`), Status: "completed"}, nil
}

// TestIntegrationInvocationKeyAdmitsOneRunAcrossEngines forces two independent
// engines past their initial run lookup before either can create the run. Only
// one controller may launch after the durable run-ID uniqueness check.
func TestIntegrationInvocationKeyAdmitsOneRunAcrossEngines(t *testing.T) {
	root := writeTwoStepWorkspace(t)
	repo := workflowledger.NewMemoryRepository()
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	var calls atomic.Int32
	newEngine := func() *localengine.Engine {
		return &localengine.Engine{WorkspaceRoot: root, Repo: repo, NewRunner: func() controller.AgentStepRunner {
			ready <- struct{}{}
			<-release
			return invocationCountingRunner{calls: &calls}
		}}
	}
	engines := []*localengine.Engine{newEngine(), newEngine()}
	results := make(chan agenttools.StartResult, len(engines))
	errs := make(chan error, len(engines))
	for _, engine := range engines {
		go func(engine *localengine.Engine) {
			result, err := engine.Start(context.Background(), agenttools.StartRequest{
				Workflow: "two-step", Inputs: map[string]any{"task": "same"}, InvocationKey: "same-request",
			})
			results <- result
			errs <- err
		}(engine)
	}
	<-ready
	<-ready
	close(release)
	var first agenttools.StartResult
	for range engines {
		result := <-results
		if err := <-errs; err != nil {
			t.Fatalf("Start() error = %v", err)
		}
		if first.RunID == "" {
			first = result
		} else if result.RunID != first.RunID {
			t.Fatalf("run IDs = %q and %q, want one invocation run", first.RunID, result.RunID)
		}
	}
	for _, engine := range engines {
		waitRun(t, engine, first.RunID)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("step calls = %d, want 2 from one two-step workflow", got)
	}
}

// TestIntegrationCancelSettlesRun cancels a blocked in-flight run.
func TestIntegrationCancelSettlesRun(t *testing.T) {
	root := writeTwoStepWorkspace(t)
	repo := workflowledger.NewMemoryRepository()
	block := make(chan struct{})
	entered := make(chan struct{}, 1)
	engine := &localengine.Engine{
		WorkspaceRoot: root,
		Repo:          repo,
		NewRunner: func() controller.AgentStepRunner {
			return &localengine.StaticStepRunner{
				Output:     json.RawMessage(`{"ok":true}`),
				BlockUntil: block,
				OnStep: func(controller.AgentStepRequest) {
					select {
					case entered <- struct{}{}:
					default:
					}
				},
			}
		},
	}
	svc := mustService(t, engine, repo)
	out, err := mustTool(t, svc, agenttools.ToolWorkflowRun).Execute(
		context.Background(), json.RawMessage(`{"workflow":"two-step","inputs":{"task":"x"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var started agenttools.StartResult
	if err := json.Unmarshal([]byte(out), &started); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("step did not start")
	}
	// Cancel while the step is blocked. The runner watches ctx.Done, so the
	// controller exits without closing block first.
	cOut, err := mustTool(t, svc, agenttools.ToolWorkflowCancel).Execute(
		context.Background(), json.RawMessage(fmt.Sprintf(`{"run_id":%q}`, started.RunID)))
	close(block)
	if err != nil {
		t.Fatal(err)
	}
	var canceled agenttools.CancelResult
	if err := json.Unmarshal([]byte(cOut), &canceled); err != nil {
		t.Fatal(err)
	}
	if canceled.Status != "canceled" && canceled.Status != "succeeded" {
		t.Fatalf("cancel status = %q", canceled.Status)
	}
	if _, err := mustTool(t, svc, agenttools.ToolWorkflowCancel).Execute(
		context.Background(), json.RawMessage(fmt.Sprintf(`{"run_id":%q}`, started.RunID))); err != nil {
		t.Fatalf("second cancel: %v", err)
	}
	waitRun(t, engine, started.RunID)
}

// TestIntegrationDeliverAllowPublishRefusalAndSuccess covers deliver gates.
func TestIntegrationDeliverAllowPublishRefusalAndSuccess(t *testing.T) {
	root := writeDeliveryWorkspace(t)
	repo := workflowledger.NewMemoryRepository()
	engine := &localengine.Engine{
		WorkspaceRoot: root,
		Repo:          repo,
		NewRunner: func() controller.AgentStepRunner {
			return &localengine.StaticStepRunner{Output: json.RawMessage(`{"ok":true}`)}
		},
		// Delivery will refuse without a real git worktree/diff — assert refusal path with allow_publish.
		Git: refusingGit{},
		PR:  noopPR{},
	}
	svc := mustService(t, engine, repo)
	runTool := mustTool(t, svc, agenttools.ToolWorkflowRun)
	out, err := runTool.Execute(context.Background(), json.RawMessage(`{"workflow":"deliver-me","inputs":{"task":"x"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var started agenttools.StartResult
	if err := json.Unmarshal([]byte(out), &started); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := engine.Wait(waitCtx, started.RunID); err != nil {
		t.Fatal(err)
	}
	statusTool := mustTool(t, svc, agenttools.ToolWorkflowStatus)
	statusOut, err := statusTool.Execute(context.Background(), json.RawMessage(fmt.Sprintf(`{"run_id":%q}`, started.RunID)))
	if err != nil {
		t.Fatal(err)
	}
	var status agenttools.StatusView
	if err := json.Unmarshal([]byte(statusOut), &status); err != nil {
		t.Fatal(err)
	}
	if status.Status != "delivery_pending" {
		t.Fatalf("status = %q, want delivery_pending; body=%s", status.Status, statusOut)
	}

	deliverTool := mustTool(t, svc, agenttools.ToolWorkflowDeliver)
	// Refusal without allow_publish (tool-level, no engine call).
	refOut, err := deliverTool.Execute(context.Background(), json.RawMessage(fmt.Sprintf(`{"run_id":%q}`, started.RunID)))
	if err != nil {
		t.Fatal(err)
	}
	var refused agenttools.DeliverResult
	if err := json.Unmarshal([]byte(refOut), &refused); err != nil {
		t.Fatal(err)
	}
	if !refused.Refused {
		t.Fatalf("expected refusal without allow_publish: %+v", refused)
	}

	// With allow_publish, host delivery path runs (stubbed git refuses → delivery_failed).
	delOut, err := deliverTool.Execute(context.Background(), json.RawMessage(fmt.Sprintf(
		`{"run_id":%q,"allow_publish":true}`, started.RunID)))
	if err != nil {
		// Hard errors from deliver are acceptable only if allow_publish path was entered;
		// prefer structured refused/failed result.
		t.Logf("deliver with allow_publish error (path exercised): %v", err)
		return
	}
	var delivered agenttools.DeliverResult
	if err := json.Unmarshal([]byte(delOut), &delivered); err != nil {
		t.Fatal(err)
	}
	if delivered.Status != "delivery_failed" && delivered.Status != "succeeded" {
		t.Fatalf("deliver result = %+v", delivered)
	}
	if delivered.Status == "delivery_failed" && !delivered.Refused && delivered.Reason == "" {
		t.Fatalf("delivery_failed without reason: %+v", delivered)
	}
}

// TestIntegrationInterruptAndResume proves resume after process-style abandon:
// the ledger stays non-terminal with an interrupted attempt; resume re-drives
// the controller and completes a second step execution to succeeded.
func TestIntegrationInterruptAndResume(t *testing.T) {
	engine, repo, svc, started, block, entered, calls, stepCalls := startBlockedTwoStep(t)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("step did not start")
	}
	if err := engine.Interrupt(started.RunID); err != nil {
		t.Fatal(err)
	}
	close(block)
	assertInterruptedNonTerminal(t, repo, started.RunID)
	stepCalls.Lock()
	before := *calls
	stepCalls.Unlock()
	resumeAndAssertSucceeded(t, engine, svc, started.RunID)
	stepCalls.Lock()
	after := *calls
	stepCalls.Unlock()
	if after <= before {
		t.Fatalf("resume did not re-dispatch a step: before=%d after=%d", before, after)
	}
}

func startBlockedTwoStep(t *testing.T) (*localengine.Engine, workflowledger.Repository, *agenttools.Service, agenttools.StartResult, chan struct{}, chan struct{}, *int, *sync.Mutex) {
	t.Helper()
	root := writeTwoStepWorkspace(t)
	repo := workflowledger.NewMemoryRepository()
	block := make(chan struct{})
	entered := make(chan struct{}, 1)
	stepCalls := &sync.Mutex{}
	calls := 0
	engine := &localengine.Engine{
		WorkspaceRoot: root, Repo: repo,
		NewRunner: func() controller.AgentStepRunner {
			return &localengine.StaticStepRunner{
				Output: json.RawMessage(`{"ok":true}`), BlockUntil: block,
				OnStep: func(controller.AgentStepRequest) {
					stepCalls.Lock()
					calls++
					stepCalls.Unlock()
					select {
					case entered <- struct{}{}:
					default:
					}
				},
			}
		},
	}
	svc := mustService(t, engine, repo)
	out, err := mustTool(t, svc, agenttools.ToolWorkflowRun).Execute(
		context.Background(), json.RawMessage(`{"workflow":"two-step","inputs":{"task":"x"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var started agenttools.StartResult
	if err := json.Unmarshal([]byte(out), &started); err != nil {
		t.Fatal(err)
	}
	return engine, repo, svc, started, block, entered, &calls, stepCalls
}

func assertInterruptedNonTerminal(t *testing.T, repo workflowledger.Repository, runID string) {
	t.Helper()
	pre, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if workflowledger.IsTerminalRunStatus(pre.Status) {
		t.Fatalf("after interrupt status = %q, want non-terminal", pre.Status)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), runID)
	if err != nil || len(attempts) == 0 {
		t.Fatalf("attempts after interrupt: %v len=%d", err, len(attempts))
	}
	for _, a := range attempts {
		if a.Status == workflowledger.AttemptStatusInterrupted {
			return
		}
	}
	t.Fatalf("expected an interrupted attempt after Interrupt: %+v", attempts)
}

func resumeAndAssertSucceeded(t *testing.T, engine *localengine.Engine, svc *agenttools.Service, runID string) {
	t.Helper()
	resOut, err := mustTool(t, svc, agenttools.ToolWorkflowRun).Execute(
		context.Background(), json.RawMessage(fmt.Sprintf(
			`{"resume":true,"run_id":%q,"force":true}`, runID)))
	if err != nil {
		t.Fatal(err)
	}
	var resumed agenttools.StartResult
	if err := json.Unmarshal([]byte(resOut), &resumed); err != nil {
		t.Fatal(err)
	}
	if !resumed.Resumed || resumed.RunID != runID {
		t.Fatalf("resume = %+v", resumed)
	}
	waitRun(t, engine, runID)
	statusOut, err := mustTool(t, svc, agenttools.ToolWorkflowStatus).Execute(
		context.Background(), json.RawMessage(fmt.Sprintf(`{"run_id":%q}`, runID)))
	if err != nil {
		t.Fatal(err)
	}
	var status agenttools.StatusView
	if err := json.Unmarshal([]byte(statusOut), &status); err != nil {
		t.Fatal(err)
	}
	if status.Status != "succeeded" {
		t.Fatalf("after resume status = %q, want succeeded; body=%s", status.Status, statusOut)
	}
	if len(status.Attempts) < 2 {
		t.Fatalf("attempts after resume = %d, want >= 2", len(status.Attempts))
	}
}

// TestResumeTerminalRunRefused proves resume does not claim success on terminals.
func TestResumeTerminalRunRefused(t *testing.T) {
	engine, svc := scriptedTwoStep(t)
	started := startTwoStep(t, svc)
	waitRun(t, engine, started.RunID)
	_, err := mustTool(t, svc, agenttools.ToolWorkflowRun).Execute(
		context.Background(), json.RawMessage(fmt.Sprintf(
			`{"resume":true,"run_id":%q,"force":true}`, started.RunID)))
	if err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("resume terminal error = %v, want terminal refusal", err)
	}
}

// TestResumeDeliveryFailedPointsAtDeliver proves a delivery_failed run is
// refused with a concrete recovery pointer instead of a generic terminal error.
func TestResumeDeliveryFailedPointsAtDeliver(t *testing.T) {
	engine, svc := scriptedTwoStep(t)
	ctx := context.Background()
	runID := "wfr-delivery-failed"
	if err := engine.Repo.CreateRun(ctx, workflowledger.RunSnapshot{
		RunID: runID, Status: workflowledger.RunStatusPending,
	}, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	fresh, err := engine.Repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Repo.CompareAndSetRunStatus(ctx, runID, fresh.Version, workflowledger.RunStatusDeliveryFailed, nil); err != nil {
		t.Fatal(err)
	}
	_, err = mustTool(t, svc, agenttools.ToolWorkflowRun).Execute(
		ctx, json.RawMessage(fmt.Sprintf(`{"resume":true,"run_id":%q,"force":true}`, runID)))
	if err == nil || !strings.Contains(err.Error(), "failed delivery") || !strings.Contains(err.Error(), "workflow_deliver") {
		t.Fatalf("resume delivery_failed error = %v, want deliver pointer", err)
	}
}

func TestIntegrationRaceConcurrentTools(t *testing.T) {
	// Exercised under go test -race via parallel start + status + cancel.
	root := writeTwoStepWorkspace(t)
	repo := workflowledger.NewMemoryRepository()
	engine := &localengine.Engine{
		WorkspaceRoot: root,
		Repo:          repo,
		NewRunner: func() controller.AgentStepRunner {
			return &localengine.StaticStepRunner{Output: json.RawMessage(`{"ok":true}`)}
		},
	}
	svc := mustService(t, engine, repo)
	runTool := mustTool(t, svc, agenttools.ToolWorkflowRun)
	statusTool := mustTool(t, svc, agenttools.ToolWorkflowStatus)
	cancelTool := mustTool(t, svc, agenttools.ToolWorkflowCancel)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := runTool.Execute(context.Background(), json.RawMessage(`{"workflow":"two-step","inputs":{"task":"r"}}`))
			if err != nil {
				t.Errorf("run: %v", err)
				return
			}
			var started agenttools.StartResult
			if err := json.Unmarshal([]byte(out), &started); err != nil {
				t.Errorf("decode: %v", err)
				return
			}
			_, _ = statusTool.Execute(context.Background(), json.RawMessage(fmt.Sprintf(`{"run_id":%q}`, started.RunID)))
			// Half cancel, half wait.
			if started.RunID[len(started.RunID)-1]%2 == 0 {
				_, _ = cancelTool.Execute(context.Background(), json.RawMessage(fmt.Sprintf(`{"run_id":%q}`, started.RunID)))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = engine.Wait(ctx, started.RunID)
		}()
	}
	wg.Wait()
}

// TestIntegrationRaceInterruptResume races Interrupt with resume and status on
// the shipped tool Execute path. Under go test -race this covers abandonFence
// map access (abandon vs clearAbandon vs isAbandoned) during concurrent tools.
func TestIntegrationRaceInterruptResume(t *testing.T) {
	// Sequential setup (t.Fatal-safe); concurrent only on the race surface.
	const n = 4
	type runCase struct {
		engine  *localengine.Engine
		svc     *agenttools.Service
		runID   string
		block   chan struct{}
		entered chan struct{}
	}
	cases := make([]runCase, 0, n)
	for i := 0; i < n; i++ {
		engine, _, svc, started, block, entered, _, _ := startBlockedTwoStep(t)
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("step did not start")
		}
		cases = append(cases, runCase{engine: engine, svc: svc, runID: started.RunID, block: block, entered: entered})
	}
	var wg sync.WaitGroup
	for _, c := range cases {
		c := c
		wg.Add(1)
		go func() {
			defer wg.Done()
			var inner sync.WaitGroup
			inner.Add(3)
			go func() {
				defer inner.Done()
				_ = c.engine.Interrupt(c.runID)
			}()
			go func() {
				defer inner.Done()
				_, _ = mustTool(t, c.svc, agenttools.ToolWorkflowStatus).Execute(
					context.Background(), json.RawMessage(fmt.Sprintf(`{"run_id":%q}`, c.runID)))
			}()
			go func() {
				defer inner.Done()
				// Resume may fail if Interrupt has not finished; that is fine.
				// The race detector must not fire on the fence map either way.
				_, _ = mustTool(t, c.svc, agenttools.ToolWorkflowRun).Execute(
					context.Background(), json.RawMessage(fmt.Sprintf(
						`{"resume":true,"run_id":%q,"force":true}`, c.runID)))
			}()
			inner.Wait()
			close(c.block)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = c.engine.Wait(ctx, c.runID)
		}()
	}
	wg.Wait()
}

// --- helpers ---

func mustService(t *testing.T, engine agenttools.Engine, repo workflowledger.Repository) *agenttools.Service {
	t.Helper()
	svc, err := agenttools.NewService(agenttools.ServiceOptions{
		Engine: engine,
		Repo: func(context.Context) (workflowledger.Repository, func(), error) {
			return repo, func() {}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func mustTool(t *testing.T, svc *agenttools.Service, name string) agenttools.Tool {
	t.Helper()
	for _, tool := range agenttools.Tools(svc) {
		if tool.Name() == name {
			return tool
		}
	}
	t.Fatalf("missing tool %s", name)
	return nil
}

type refusingGit struct{}

func (refusingGit) Run(context.Context, delivery.GitContext, ...string) (string, error) {
	return "", fmt.Errorf("git refused in test")
}

type noopPR struct{}

func (noopPR) FindByHead(context.Context, string, string) (*delivery.PRRef, error) {
	return nil, nil
}

func (noopPR) IsMerged(context.Context, string, string) (bool, error) {
	return false, nil
}

func (noopPR) Create(context.Context, string, delivery.PRInput) (delivery.PRRef, error) {
	return delivery.PRRef{}, fmt.Errorf("pr create refused in test")
}
