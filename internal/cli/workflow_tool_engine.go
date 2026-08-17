package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// sessionCancelWait is how long Cancel waits for an in-process controller to
// drop its claim after context cancel before settling the ledger.
const sessionCancelWait = 3 * time.Second

// workflowResolutionLockWait bounds the execution-lock wait for cancel after
// stopActive: a settling controller can hold the flock past the cancel wait
// bound, and a non-blocking acquire would surface as an opaque lock error
// while the run keeps running.
//
// It is a var, not a const, ONLY so tests that assert the held-lock refusal
// can shorten the wait (shortenWorkflowResolutionLockWait). Those tests pin
// WHICH error a held lock produces, not how long the surface waits, so paying
// the real five seconds per surface bought nothing and made internal/cli the
// critical path of the whole test suite. Production never assigns it.
var workflowResolutionLockWait = 5 * time.Second

// sessionWorkflowEngine is the production Engine for chat-session workflow tools.
// New runs use the full CLI admission path (providers, worktrees, coordinator)
// and return the run ID without waiting for terminal state.
// Cancel and Deliver use the same CLI ledger/worktree paths as the operator
// commands — not localengine delivery against the caller workspace root.
type sessionWorkflowEngine struct {
	root       string
	configPath string

	mu sync.Mutex
	// bus supplies the session event bus lazily. The provider is read at
	// controller attach time, not at wiring time: chat wires the engine before
	// runTUI creates the bus, so a provider keeps that ordering irrelevant.
	bus    func() *events.Bus
	active map[string]*sessionActiveRun
}

// newSessionWorkflowEngine builds the chat-session workflow engine.
func newSessionWorkflowEngine(root, configPath string) *sessionWorkflowEngine {
	return &sessionWorkflowEngine{
		root:       root,
		configPath: configPath,
		active:     make(map[string]*sessionActiveRun),
	}
}

// SetEventBusProvider attaches the session event bus provider to the engine.
// When a controller runs, its progress events are published onto the bus the
// provider returns so the TUI and metrics can observe workflow lifecycle. The
// provider is read at attach time, which lets a bus created after wiring (the
// production order: wireWorkflowToolOptions runs before runTUI builds the bus)
// still receive progress. A nil provider disables progress publishing.
func (e *sessionWorkflowEngine) SetEventBusProvider(provider func() *events.Bus) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.bus = provider
	e.mu.Unlock()
}

// SetEventBus attaches a fixed session event bus for callers that hold the bus
// at wiring time (mainly tests). It wraps SetEventBusProvider.
func (e *sessionWorkflowEngine) SetEventBus(bus *events.Bus) {
	e.SetEventBusProvider(func() *events.Bus { return bus })
}

// attachWorkflowProgressBus publishes controller progress onto the engine
// event bus. It must run before the controller starts. A nil sink or a nil
// controller leaves the sink unset.
func (e *sessionWorkflowEngine) attachWorkflowProgressBus(ctrl *controller.LinearController) {
	if e == nil || ctrl == nil {
		return
	}
	sink := e.workflowProgressSink()
	if sink == nil {
		return
	}
	// The sink is best-effort. A controller that already started refuses it.
	_ = ctrl.SetProgressSink(sink)
}

// Start implements agenttools.Engine.
// New runs and resumes use the full CLI admission path only. There is no
// silent fallback to a scripted local runner: missing provider config fails.
func (e *sessionWorkflowEngine) Start(ctx context.Context, req agenttools.StartRequest) (agenttools.StartResult, error) {
	if e == nil {
		return agenttools.StartResult{}, fmt.Errorf("workflow engine is nil")
	}
	if req.Resume {
		return e.resumeCLI(ctx, req)
	}
	return e.startCLI(ctx, req)
}

func (e *sessionWorkflowEngine) startCLI(ctx context.Context, req agenttools.StartRequest) (agenttools.StartResult, error) {
	rawInputs, err := inputsToRawFlags(req.Inputs)
	if err != nil {
		return agenttools.StartResult{}, err
	}
	prepared, err := prepareWorkflowRun(req.Workflow, e.root, e.configPath, rawInputs)
	if err != nil {
		return agenttools.StartResult{}, err
	}
	runID := newCLIWorkflowRunID()
	if strings.TrimSpace(req.InvocationKey) != "" {
		keyedID, existing, err := e.keyedRunID(ctx, prepared, req)
		if err != nil {
			return agenttools.StartResult{}, err
		}
		if existing != nil {
			return *existing, nil
		}
		// A fresh keyed start binds the keyed runID, never a random one:
		// the invocation key must stay the stable handle for this run.
		runID = keyedID
	}
	return e.buildAndStart(ctx, prepared, req, runID)
}

// keyedRunID resolves the run a request under an invocation_key maps to. A
// key is a stable handle on ONE run of ONE workflow (the stack_command.go
// admission guard keeps the same invariant): a key bound to workflow A must
// never return A's terminal result for workflow B, and a keyed retry with
// different inputs must be refused instead of silently resuming the old
// request (FIX: wrong-run continuation). The helper owns prepared on every
// path except the fresh-start one - it closes the admission store before
// resuming or returning an existing result, and returns the keyed runID for
// a fresh start with ownership intact.
func (e *sessionWorkflowEngine) keyedRunID(ctx context.Context, prepared *preparedWorkflowRun, req agenttools.StartRequest) (string, *agenttools.StartResult, error) {
	runID := agenttools.InvocationRunID(strings.TrimSpace(req.InvocationKey))
	existing, getErr := prepared.repo.GetRun(ctx, runID)
	if getErr != nil {
		if errors.Is(getErr, workflowledger.ErrNotFound) {
			return runID, nil, nil
		}
		return "", nil, getErr
	}
	if existing.WorkflowName != "" && req.Workflow != "" && existing.WorkflowName != req.Workflow {
		prepared.closeFn()
		return "", nil, fmt.Errorf("invocation_key %q is already bound to workflow %s (run %s); use a fresh key or resume %s explicitly", req.InvocationKey, existing.WorkflowName, runID, runID)
	}
	if !workflowledger.IsTerminalRunStatus(existing.Status) && existing.Status != workflowledger.RunStatusDeliveryPending {
		e.mu.Lock()
		_, active := e.active[runID]
		e.mu.Unlock()
		if !active {
			// Silent-resume branch: the snapshot owns the request. A retry
			// under the same key with different inputs must be refused while
			// the admission store is still open.
			if len(req.Inputs) > 0 {
				match, matchErr := invocationInputsMatchRun(prepared.repo, runID, req.Inputs)
				if matchErr != nil {
					prepared.closeFn()
					return "", nil, matchErr
				}
				if !match {
					prepared.closeFn()
					return "", nil, fmt.Errorf("invocation_key %q is bound to run %s with different inputs; use a fresh key or resume %s explicitly", req.InvocationKey, runID, runID)
				}
			}
			prepared.closeFn()
			resumed, resumeErr := e.resumeCLI(ctx, agenttools.StartRequest{Resume: true, RunID: runID, Force: req.Force, AllowPublish: req.AllowPublish})
			return "", &resumed, resumeErr
		}
	}
	prepared.closeFn()
	return runID, &agenttools.StartResult{RunID: runID, Status: string(existing.Status), Workflow: existing.WorkflowName}, nil
}

// buildAndStart performs the CLI admission build for runID and starts the
// controller, settling resources on every failure path. On success the run
// launches and prepared/built ownership transfers to the active run.
func (e *sessionWorkflowEngine) buildAndStart(ctx context.Context, prepared *preparedWorkflowRun, req agenttools.StartRequest, runID string) (agenttools.StartResult, error) {
	finishExecution, err := beginWorkflowExecution(prepared.root, contextStorePath(prepared.root, prepared.res.Subagents), runID)
	if err != nil {
		prepared.closeFn()
		return agenttools.StartResult{}, err
	}
	built, err := workflowRunBuild(prepared.root, prepared.res, prepared.store, prepared.repo, prepared.compiled, prepared.refBase, prepared.inputs, prepared.inputSnapshot, prepared.raw, runID, nil, nil, nil, nil, nil)
	if err != nil {
		finishExecution()
		prepared.closeFn()
		return agenttools.StartResult{}, err
	}
	built.Admission.InvocationKey = strings.TrimSpace(req.InvocationKey)
	if err := workflowRunSetAdmission(built); err != nil {
		built.Cleanup()
		built.Dispatcher.Close()
		finishExecution()
		prepared.closeFn()
		return agenttools.StartResult{}, err
	}
	e.attachWorkflowProgressBus(built.Controller)
	created, err := built.Controller.StartNew(ctx)
	if err != nil {
		built.Cleanup()
		built.Dispatcher.Close()
		finishExecution()
		prepared.closeFn()
		return agenttools.StartResult{}, err
	}
	if !created {
		existing, getErr := prepared.repo.GetRun(ctx, runID)
		built.Cleanup()
		built.Dispatcher.Close()
		finishExecution()
		prepared.closeFn()
		if getErr != nil {
			return agenttools.StartResult{}, getErr
		}
		return agenttools.StartResult{RunID: runID, Status: string(existing.Status), Workflow: existing.WorkflowName}, nil
	}
	return e.launchStartedWorkflow(ctx, prepared, built, runID, req.Workflow, finishExecution)
}

// invocationInputsMatchRun reports whether the requested inputs equal the
// bound run's admitted snapshot inputs (compared by input digest, the same
// digest admission recorded). The resume branch must not silently drop a
// different request, so a keyed retry with changed inputs is refused instead
// of resuming the old request (FIX: wrong-run continuation).
func invocationInputsMatchRun(repo workflowledger.Repository, runID string, inputs map[string]any) (bool, error) {
	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		return false, err
	}
	strInputs := make(map[string]string, len(inputs))
	for k, v := range inputs {
		strInputs[k] = fmt.Sprint(v)
	}
	return run.InputDigest == workflowledger.InputDigest(strInputs), nil
}

func (e *sessionWorkflowEngine) launchStartedWorkflow(ctx context.Context, prepared *preparedWorkflowRun, built workflowControllerBuild, runID, workflow string, finishExecution func()) (agenttools.StartResult, error) {
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	e.mu.Lock()
	if e.active == nil {
		e.active = make(map[string]*sessionActiveRun)
	}
	runner, _ := built.Controller.Runner.(*controller.CoordinatorRunner)
	// The run goroutine owns the execution flock for the run's whole lifetime.
	// Capture the release function here so the goroutine can release the flock
	// BEFORE closing done: callers waiting on done must be able to assume the
	// lock is free once done closes.
	releaseExecution := finishExecution
	e.active[runID] = &sessionActiveRun{cancel: cancel, done: done, runner: runner, closeFn: func() { built.Dispatcher.Close(); prepared.closeFn() }}
	e.mu.Unlock()
	go func() {
		sessionAutoDeliveryRepairLoop(runCtx, prepared.repo, prepared.root, prepared.res, prepared.store, runID, func(ctx context.Context) (workflowledger.RunSnapshot, error) {
			return controller.RunWithCancelReconciliationRetry(ctx, built.Controller.Run)
		}, func(ctx context.Context) (bool, error) {
			// A stacking plan run that settles delivery_pending with a multi-chunk
			// plan drives its stack before the plan run is delivered; the hook
			// mirrors the CLI run entry point's drive-before-delivery ordering and
			// reports whether it drove a stack. The hook ctx is the bounded
			// session attempt ctx (workflowAutoDeliveryAttemptTimeout), so a
			// stuck drive can be stopped instead of holding the execution flock.
			// Publish authority derives from the merge policy (stackingDrive
			// AllowPublish): merge_policy=approve - the default - marks chunks
			// reviewed and pauses for the per-chunk deliver grant instead of
			// auto-publishing, exactly like the CLI foreground path without
			// --allow-publish (live finding: hardcoding true made the approve
			// checkpoint dead in the session path).
			return maybeDriveSettledStack(ctx, prepared, runID, stackingDriveAllowPublish(prepared.compiled), io.Discard, io.Discard)
		}, compiledDeliverPlanRun(prepared.compiled))
		// Delivery completion settles outside the controller (which parked at
		// delivery_pending and emitted no run_finished), so publish the terminal
		// event here once delivery actually succeeded.
		e.publishDeliveredRunFinished(context.Background(), prepared.repo, runID)
		// A plan run whose own publication is disabled settles succeeded with no
		// delivery record; publish its terminal event from the stack-ledger
		// marker.
		e.publishSkippedPlanRunFinished(context.Background(), prepared.store, prepared.repo, runID)
		// Release the per-run execution flock BEFORE closing done. The run loop
		// has exited, so no further controller work can race with a waiter that
		// observes done closed. Releasing here lets Deliver wait on done and then
		// safely contend for the flock, instead of racing the goroutine's own
		// cleanup below.
		releaseExecution()
		// done closes as soon as the run loop itself has exited and the flock is
		// released, before resource teardown below: stopActive's callers must be
		// able to observe "the loop stopped" without that implying "resources are
		// already closed" - otherwise a concurrent Cancel waiting on done would
		// always find runner's backing store already closed by the time it can
		// reuse it.
		close(done)
		e.mu.Lock()
		active := e.active[runID]
		delete(e.active, runID)
		e.mu.Unlock()
		if active != nil {
			active.closeGuarded()
		}
	}()
	run, err := prepared.repo.GetRun(ctx, runID)
	if err != nil {
		return agenttools.StartResult{}, err
	}
	return agenttools.StartResult{RunID: runID, Status: string(run.Status), Workflow: workflow}, nil
}

// stopActive cancels an in-process controller for runID and waits until it
// exits (or the wait bound fires). It must run before claim clear / CancelRun
// so the dying controller is not racing ledger settlement.
func (e *sessionWorkflowEngine) stopActive(ctx context.Context, runID string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	active, ok := e.active[runID]
	e.mu.Unlock()
	if !ok || active == nil {
		return
	}
	active.cancel()
	select {
	case <-active.done:
	case <-ctx.Done():
	case <-time.After(sessionCancelWait):
	}
}

// claimForCancel claims runID for holder, taking over the existing claim
// only when its lease has actually expired. A fresh foreign claim is refused
// outright: a live delivery claim (held by this or another host mid-publish)
// must never be force-cleared by cancel.
func claimForCancel(ctx context.Context, repo workflowledger.Repository, runID, holder string) error {
	if err := repo.ClaimRun(ctx, runID, holder); err != nil {
		if !errors.Is(err, workflowledger.ErrClaimHeld) {
			return err
		}
		takeoverErr := repo.TakeoverExpiredRunClaim(ctx, runID, holder, workflowledger.DefaultClaimLease)
		if errors.Is(takeoverErr, workflowledger.ErrClaimNotHeld) {
			return repo.ClaimRun(ctx, runID, holder)
		}
		if takeoverErr != nil {
			return fmt.Errorf("workflow run %q is claimed by another executor; cancel refused", runID)
		}
	}
	return nil
}

func inputsToRawFlags(inputs map[string]any) ([]string, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(inputs))
	for k, v := range inputs {
		switch x := v.(type) {
		case string:
			out = append(out, k+"="+x)
		default:
			raw, err := json.Marshal(x)
			if err != nil {
				return nil, fmt.Errorf("workflow input %q: encode JSON: %w", k, err)
			}
			out = append(out, k+"="+string(raw))
		}
	}
	return out, nil
}
