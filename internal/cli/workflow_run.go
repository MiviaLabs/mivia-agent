package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/tasks"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

var (
	workflowRunBuild        = buildWorkflowController
	workflowRunSetAdmission = func(b workflowControllerBuild) error {
		return b.Controller.SetAdmission(b.Admission)
	}
	workflowBuildLoadSkills = loadChatSkills
	workflowBuildLoadAgents = loadAgentDefinitions
	workflowBuildRegistry   = workflowDefaultRegistry
	workflowBuildWorkspace  = selectWorkflowWorkspace
	workflowBuildProvider   = provider.New
	workflowBuildDispatcher = NewSessionDispatcher
	workflowBuildController = controller.NewLinearController
)

func runWorkflow(args []string) error {
	// fd-2 progress writes must degrade, not kill, the run: a broken stderr
	// pipe (mivia workflow run 2> >(head -1)) would otherwise raise SIGPIPE
	// and terminate the process before the run settles.
	signal.Ignore(syscall.SIGPIPE)
	return runWorkflowWithIO(args, os.Stdout, os.Stderr)
}

func runWorkflowWithIO(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("workflow: expected run, runs, resume, deliver, status, events, approve, reject, cancel, cleanup, delete, or gc")
	}
	var workspaceRoot, configPath string
	var err error
	workspaceRoot, args, _, err = flagValue(args, "--workspace")
	if err != nil {
		return err
	}
	configPath, args, _, err = flagValue(args, "--config")
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return fmt.Errorf("workflow: expected run, runs, resume, deliver, status, events, approve, reject, cancel, cleanup, delete, or gc")
	}
	// --force is a flag of deliver/resume/delete only. Strip it for those;
	// every other subcommand must reject it loudly instead of silently
	// ignoring it (FIX: silent flag - `workflow run --force x`,
	// `workflow cancel --force <id>`, ... used to proceed without the flag).
	sub := args[0]
	force := false
	switch sub {
	case "deliver", "resume", "delete":
		filtered := args[:0]
		for _, arg := range args {
			if arg == "--force" {
				force = true
				continue
			}
			filtered = append(filtered, arg)
		}
		args = filtered
	default:
		for _, arg := range args[1:] {
			if arg == "--force" {
				return fmt.Errorf("workflow %s: --force is not supported by this subcommand (it is a flag of deliver, resume, and delete only)", sub)
			}
		}
	}
	switch args[0] {
	case "run":
		return runWorkflowCommandRun(args[1:], workspaceRoot, configPath, stdout, stderr)
	case "runs":
		return runWorkflowCommandRuns(args[1:], workspaceRoot, configPath, stdout, stderr)
	case "deliver":
		return runWorkflowCommandDeliver(args[1:], workspaceRoot, configPath, force, stdout, stderr)
	case "resume":
		return runWorkflowCommandResume(args[1:], workspaceRoot, configPath, force, stdout, stderr)
	case "status":
		return runWorkflowCommandStatus(args[1:], workspaceRoot, configPath, stdout, stderr)
	case "events":
		return runWorkflowCommandEvents(args[1:], workspaceRoot, configPath, stdout, stderr)
	case "approve":
		return runWorkflowCommandApprove(args[1:], workspaceRoot, configPath, stdout, stderr)
	case "reject":
		return runWorkflowCommandReject(args[1:], workspaceRoot, configPath, stdout, stderr)
	case "cancel":
		return runWorkflowCommandCancel(args[1:], workspaceRoot, configPath, stdout, stderr)
	case "cleanup":
		return runWorkflowCommandCleanup(args[1:], workspaceRoot, configPath, stdout, stderr)
	case "delete":
		return runWorkflowCommandDelete(args[1:], workspaceRoot, configPath, force, stdout, stderr)
	case "gc":
		return runWorkflowCommandGC(args[1:], workspaceRoot, configPath, stdout, stderr)
	default:
		return fmt.Errorf("workflow: unknown subcommand %q", args[0])
	}
}

func executeWorkflowRun(name, root, configPath string, rawInputs []string, allowPublish bool, stdout, stderr io.Writer) error {
	prepared, err := prepareWorkflowRun(name, root, configPath, rawInputs)
	if err != nil {
		return err
	}
	logMCPWarnings(stderr, prepared.res)
	defer prepared.closeFn()
	runID := newCLIWorkflowRunID()
	releaseExecution, err := beginWorkflowRunExecution(prepared.root, contextStorePath(prepared.root, prepared.res.Subagents), runID)
	if err != nil {
		return err
	}
	defer releaseExecution()
	built, err := workflowRunBuild(prepared.root, prepared.res, prepared.store, prepared.repo, prepared.compiled, prepared.refBase, prepared.inputs, prepared.inputSnapshot, prepared.raw, runID, nil, nil, nil, nil, nil)
	if err != nil {
		return err
	}
	defer built.Dispatcher.Close()
	admitted := false
	defer func() {
		if !admitted {
			built.Cleanup()
		}
	}()
	if err := workflowRunSetAdmission(built); err != nil {
		return err
	}
	wireCLIWorkflowProgress(&built, stderr)
	if err := built.Controller.Start(context.Background()); err != nil {
		return err
	}
	admitted = true
	snap, err := built.Controller.Run(context.Background())
	fmt.Fprintf(stdout, "run_id=%s status=%s\n", built.Controller.RunID, snap.Status)
	if err != nil {
		// A genuine (non-deadline) fault that stops the controller must settle
		// the run: Controller.Run self-settles deadline errors, cancel owns
		// cancelled runs, but a raw storage/claim fault would otherwise leave
		// the run row `running` with no cause (DC-9).
		settleCLIRunFailure(prepared.repo, built.Controller.RunID, err)
		return err
	}
	// Stacking is part of the run: a stacking-enabled workflow whose plan run
	// settles with a multi-chunk plan keeps driving from THIS invocation - the
	// per-chunk runs, their stacked PRs, the merge waits, and the final
	// integration run - until the whole stack is complete. No separate drive
	// command is needed; the run itself owns the stack.
	//
	// The drive runs BEFORE the delivery branch below: a multi-chunk plan run
	// settles at delivery_pending (its success terminal is delivery-policy
	// active), and returning from the delivery branch first would publish the
	// plan PR and leave the chunk stack undriven (deliver-before-drive
	// ordering bug). The drive is a no-op for non-stacking workflows,
	// non-multi plans, and workflows without an active delivery policy.
	// CLI foreground paths are unbounded by design: the drive's ctx is the
	// session attempt bound's stop signal, and this invocation owns the run
	// until the stack completes (or is interrupted by the process).
	drove, err := maybeDriveSettledStack(context.Background(), prepared, built.Controller.RunID, allowPublish, stdout, stderr)
	if err != nil {
		if errors.Is(err, errStackAwaitsGrant) {
			// A durable pause, not a failure: the drive already printed the
			// grant guidance and the run stays delivery_pending, resumable.
			// drove is meaningless here - fall through to it would settle
			// the plan run succeeded with the stack still incomplete (an
			// adversarial audit found exactly this).
			return nil
		}
		if settled, settleErr := settleFailedStackPlanRunIfNeededFn(context.Background(), prepared, built.Controller.RunID, err.Error()); settleErr != nil {
			return fmt.Errorf("workflow run: settle failed plan run: %w", settleErr)
		} else if settled {
			return errFailedStackPlanRun(built.Controller.RunID, err.Error())
		}
		return err
	}
	if snap.Status == workflowledger.RunStatusDeliveryPending {
		return finishExecutedWorkflowRunDelivery(context.Background(), prepared, built.Controller.RunID, name, configPath, drove, allowPublish, releaseExecution, stdout, stderr)
	}
	return nil
}

// finishExecutedWorkflowRunDelivery settles a just-run plan-mode workflow at
// its delivery_pending success terminal. A multi-chunk stack that just drove
// is published only when the workflow opts in (delivery.deliver_plan_run=true);
// the default keeps the plan run unpublished: the chunk PRs carry the work and
// the plan and its artifacts are recorded in the ledger, and the run settles
// succeeded (the same terminal a delivered run reaches) instead of parking at
// delivery_pending forever. The delivery mode comes from the compiled
// workflow (nil-safe), mirroring executeWorkflowResume.
func finishExecutedWorkflowRunDelivery(ctx context.Context, prepared *preparedWorkflowRun, runID, name, configPath string, drove, allowPublish bool, release func(), stdout, stderr io.Writer) error {
	if drove && !compiledDeliverPlanRun(prepared.compiled) {
		if err := settlePlanRunSkippedDelivery(ctx, prepared.repo, runID); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "run_id=%s status=%s plan PR not created (delivery.deliver_plan_run=false); plan and artifacts recorded in the ledger\n", runID, workflowledger.RunStatusSucceeded)
		return nil
	}
	mode := ""
	if prepared.compiled.Delivery != nil {
		mode = prepared.compiled.Delivery.Mode
	}
	return finishWorkflowRunDelivery(ctx, prepared.root, configPath, prepared.res, prepared.store, prepared.repo, runID, name, mode, allowPublish, release, stdout, stderr)
}

// maybeDriveSettledStack continues a just-settled plan-mode run into the
// stacking driver when the run's decompose step produced a multi-chunk plan
// (stack_mode=multi). It reports whether it drove a stack (drove=true only
// when a multi-chunk drive ran to completion) and is a no-op - (false, nil) -
// for single/no_bug plans, runs without a succeeded decompose output, and
// workflows without an active stacking delivery policy.
//
// The drive reconstructs the FULL chunk list across every already-admitted
// decompose wave (wave 0 from the plan run plus any continuation waves), not
// just the plan run's own first wave. A process that crashed after admitting
// wave N≥1 would otherwise re-enter with only wave-0 chunks, leaving the
// already-seeded wave-N tasks outside the topological order and wedging the
// stack (F5).
func maybeDriveSettledStack(ctx context.Context, prepared *preparedWorkflowRun, planRunID string, allowPublish bool, stdout, stderr io.Writer) (bool, error) {
	if prepared.compiled.Stacking == nil || !prepared.compiled.Stacking.Enabled {
		return false, nil
	}
	if !prepared.compiled.DeliveryActive() {
		return false, nil
	}
	planOutput, err := loadStackPlanOutput(prepared.repo, planRunID)
	if err != nil {
		return false, nil // no succeeded decompose output: nothing to stack
	}
	mode, _, _, _, err := parseStackPlanOutput(planOutput)
	if err != nil {
		return false, nil
	}
	if mode != "multi" {
		return false, nil
	}
	planInputs, err := stackPlanInputs(prepared.repo, planRunID)
	if err != nil {
		return false, fmt.Errorf("stack plan inputs: %w", err)
	}
	chunks, hasMore, remainingScope, err := loadAllStackChunksForDrive(prepared, planRunID, planOutput, planInputs, stdout, stderr)
	if err != nil {
		return false, fmt.Errorf("stack drive: %w", err)
	}
	if len(chunks) == 0 {
		return false, nil
	}
	fmt.Fprintf(stdout, "stack %s: multi-chunk plan (%d chunks); driving the stack to completion\n", planRunID, len(chunks))
	ledger := tasks.NewStore(prepared.store)
	if err := seedStackLedger(ledger, planRunID, chunks); err != nil {
		return false, fmt.Errorf("stack seed: %w", err)
	}
	return true, workflowStackDriveToCompletion(ctx, prepared, ledger, planRunID, chunks, hasMore, remainingScope, planInputs, allowPublish, stdout, stderr)
}

// compiledDeliverPlanRun reports whether the compiled workflow's delivery
// policy publishes the plan-mode run's own PR (delivery.deliver_plan_run).
// The default is false: a stacking plan run's diff is not published; the plan
// and its artifacts stay recorded in the ledger.
func compiledDeliverPlanRun(compiled *compiler.CompiledWorkflow) bool {
	return compiled != nil && compiled.Delivery != nil && compiled.Delivery.DeliverPlanRun
}

// settlePlanRunSkippedDelivery settles a plan run whose own publication is
// disabled (delivery.deliver_plan_run=false) after its stack drove to
// completion. The run's success terminal parked it at delivery_pending; the
// stack is done and nothing is published for the run itself, so it CASes to
// succeeded - the same terminal a delivered run reaches - rather than waiting
// for a delivery that will never come. It is a no-op when the run no longer
// waits for delivery (for example a concurrent manual deliver already settled
// it).
func settlePlanRunSkippedDelivery(ctx context.Context, repo workflowledger.Repository, runID string) error {
	fresh, err := repo.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if fresh.Status != workflowledger.RunStatusDeliveryPending {
		return nil
	}
	now := time.Now()
	return repo.CompareAndSetRunStatus(ctx, runID, fresh.Version, workflowledger.RunStatusSucceeded, &now)
}

// workflowStackDriveToCompletion is the stack driver invoked for a settled
// multi-chunk plan run. It is a package variable so tests can stub the drive
// and pin the drive-before-delivery ordering without running chunk agents.
var workflowStackDriveToCompletion = driveStackToCompletion

// preparedWorkflowRun is the immutable input of one workflow run invocation:
// the opened workspace, store, and compiled definition.
type preparedWorkflowRun struct {
	root          string
	res           *config.Resolved
	store         *storage.SQLite
	repo          workflowledger.Repository
	closeFn       func()
	compiled      *compiler.CompiledWorkflow
	inputs        map[string]any
	inputSnapshot map[string]string
	refBase       string
	raw           []byte
}

// prepareWorkflowRun opens the workspace and store and compiles the named
// workflow with validated inputs, before any execution begins.
func prepareWorkflowRun(name, root, configPath string, rawInputs []string) (*preparedWorkflowRun, error) {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	work, err := workspace.Open(root)
	if err != nil {
		return nil, err
	}
	configPath = workflowConfigPath(work.Abs, configPath)
	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, WorkspaceRoot: work.Abs, AllowMissingConfig: true})
	if err != nil {
		return nil, err
	}
	applyPrivacyPolicy(res)
	applyWorkflowStoreRoot(res, work.Abs)
	store, repo, closeFn, err := openWorkflowStore(work.Abs, res.Subagents)
	if err != nil {
		return nil, err
	}
	workflows, err := definition.DiscoverWorkflows(work.Abs)
	if err != nil {
		closeFn()
		return nil, err
	}
	var found *definition.DiscoveredWorkflow
	for i := range workflows {
		if workflows[i].Name == name {
			found = &workflows[i]
			break
		}
	}
	if found == nil {
		closeFn()
		return nil, fmt.Errorf("workflow %q was not found", name)
	}
	wf, _, err := definition.ParseWorkflowTOML(found.Raw, found.Name+".toml")
	if err != nil {
		closeFn()
		return nil, err
	}
	compiled, err := compiler.Compile(&wf)
	if err != nil {
		closeFn()
		return nil, err
	}
	// A stacking workflow accepts the engine-reserved inputs (stack_mode,
	// chunk, ...) at admission too, so the operator override the controller
	// supports (e.g. --input stack_mode=single) validates against the same
	// input contract as resume. A no-op for non-stacking workflows.
	compiler.MergeStackingInputs(compiled)
	inputs, inputSnapshot, err := parseWorkflowInputs(rawInputs, compiled.Inputs)
	if err != nil {
		closeFn()
		return nil, err
	}
	// Fail fast before any agent runs: a fresh run whose inputs instruct a
	// write to a host write-blocklisted path can never satisfy itself (the
	// write tools refuse), so it would spin implement -> review -> blocked
	// implement until a misattributed failure. Refuse admission instead and
	// route the change through the root session or a host-owned process.
	if err := workflowBlockedInputAdmission(effectiveWorkflowWriteDenylist(res), compiled.Name, inputs); err != nil {
		closeFn()
		return nil, err
	}
	return &preparedWorkflowRun{
		root: work.Abs, res: res, store: store, repo: repo, closeFn: closeFn,
		compiled: compiled, inputs: inputs, inputSnapshot: inputSnapshot,
		refBase: filepath.Dir(found.Path), raw: found.Raw,
	}, nil
}

// finishWorkflowRunDelivery completes a run that settled at delivery_pending:
// with --allow-publish it performs delivery and prints the settled status;
// without it prints the non-publication explanation. This is the shared
// settle point for both `workflow run` and `workflow resume`'s foreground
// paths, both of which own the run until it reaches a terminal status ("CLI
// foreground paths are unbounded by design", executeWorkflowRun).
func finishWorkflowRunDelivery(ctx context.Context, root, configPath string, res *config.Resolved, store *storage.SQLite, repo workflowledger.Repository, runID, workflowName, mode string, allowPublish bool, release func(), stdout, stderr io.Writer) error {
	if allowPublish {
		if err := deliverRunWithStore(ctx, root, res, store, repo, runID, allowPublish, false, stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "workflow delivery failed: %v\n", err)
			return err
		}
		settled, err := repo.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "run_id=%s status=%s\n", runID, settled.Status)
		if settled.Status == workflowledger.RunStatusRunning {
			// A repairable delivery rejection routed the run back to its
			// repair step (delivery.ReopenForRepair). Drive it forward
			// instead of stopping and telling a human to run `workflow
			// resume` by hand - a run whose foreground process already
			// exited here used to sit parked (ledger status "running", no
			// live process) until someone noticed and ran the printed
			// recovery command. reenterRepairedRun releases the execution
			// lock first and re-enters through executeWorkflowResume, which
			// reacquires it: the same release-then-resume pattern the
			// session recovery sweep already uses for this exact scenario
			// (reconcileParkedDelivery). ReopenForRepair's own
			// MaxDeliveryRepairs budget bounds the number of re-entries.
			return reenterRepairedRun(runID, root, configPath, allowPublish, release, stdout, stderr)
		}
		return nil
	}
	fmt.Fprintf(stdout, "workflow %s reached its success terminal; delivery mode=%s requires --allow-publish\n", workflowName, mode)
	fmt.Fprintf(stdout, "deliver with: mivia workflow deliver %s --allow-publish\n", runID)
	return nil
}

// reenterRepairedRun re-drives a run that ReopenForRepair (see
// internal/workflows/delivery/repair.go) just reopened to RunStatusRunning
// after a repairable delivery rejection. release must release the caller's
// already-held workflow execution lock; the lock is not reentrant
// (acquireWorkflowExecutionLock, workflow_resume_lock.go), and
// executeWorkflowResume reacquires it itself. Recursing while still holding
// it would deadlock (a second acquire from the same process fails "lock is
// busy"), so release runs first, exactly mirroring reconcileParkedDelivery's
// proven release-then-resume handling of this identical scenario
// (workflow_tool_engine_reconcile.go).
func reenterRepairedRun(runID, root, configPath string, allowPublish bool, release func(), stdout, stderr io.Writer) error {
	release()
	return executeWorkflowResume(runID, root, configPath, false, allowPublish, false, false, stdout, stderr)
}

// beginWorkflowRunExecution acquires the run's execution lock and wraps the
// release in sync.OnceFunc: a repairable delivery rejection releases it early
// (see reenterRepairedRun), so the caller's own deferred release must become
// a safe no-op instead of double-releasing.
func beginWorkflowRunExecution(root, storePath, runID string) (func(), error) {
	finish, err := beginWorkflowExecution(root, storePath, runID)
	if err != nil {
		return nil, err
	}
	return sync.OnceFunc(finish), nil
}

func openWorkflowStore(root string, cfg config.SubagentConfig) (*storage.SQLite, workflowledger.Repository, func(), error) {
	store, err := openContextStore(root, cfg)
	if err != nil {
		return nil, nil, func() {}, err
	}
	return store, workflowledger.NewStorageRepository(store), func() { _ = store.Close() }, nil
}

func workflowConfigPath(root, explicit string) string {
	if strings.TrimSpace(explicit) != "" {
		return explicit
	}
	candidate := workspace.NamespacePath(root, "mivia.toml")
	info, err := os.Stat(candidate)
	if err == nil && info.Mode().IsRegular() {
		return candidate
	}
	return ""
}

// applyWorkflowStoreRoot pins workflow execution state to the workspace,
// regardless of the chat/session store's default. Workflow run IDs and
// locks are not namespaced across projects the way chat sessions are (see
// contextWorkspaceID), so letting them fall back to the shared global store
// would let unrelated workflow runs in different repos collide.
func applyWorkflowStoreRoot(res *config.Resolved, root string) {
	if res != nil && !res.StorePathSet {
		res.Subagents.StorePath = workspace.ContextStorePath(root)
	}
}
