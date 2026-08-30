package cliworkflow

// testmain_test.go wires the cli-backed seams with defaults that do not need
// internal/cli: the stack read helpers are thin wrappers over the workflows
// delivery and ledger packages, and the flag/slice helpers are pure logic
// mirrored from cli. Tests that need the real cli behavior (hooks, dispatch,
// privacy) stub the seams explicitly.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	goruntime "runtime"
	"strconv"
	"strings"
	"testing"

	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/gittest"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func TestMain(m *testing.M) {
	gittest.DisableDetachedMaintenance()
	wireTestSeams()
	cliagents.WireWorkflowToolOptionsVar = WireWorkflowToolOptions
	os.Exit(m.Run())
}

// wireTestSeams installs every cli-backed seam default that does not need
// internal/cli.
func wireTestSeams() {
	wireDeliverySeams()
	wireStackReadSeams()
	wireStackGateSeams()
	wireSessionSeams()
	InitCLIDefaults()
}

// wireDeliverySeams wires the store, flag, and policy seams.
func wireDeliverySeams() {
	ContextStorePath = func(root string, cfg config.SubagentConfig) string {
		if cfg.StorePath != "" {
			return config.ExpandPath(cfg.StorePath)
		}
		return workspace.GlobalContextStorePath(root)
	}
	OpenContextStoreFunc = func(root string, cfg config.SubagentConfig) (*storage.SQLite, error) {
		p := ContextStorePath(root, cfg)
		harden := cliagents.SameFilePath(goruntime.GOOS, p, config.TempStorePath(root, "orchestration"))
		return storage.OpenSQLiteWithOptions(p, storage.Options{Harden: harden})
	}
	ApplyPrivacyPolicyFunc = func(res *config.Resolved) {}
	LogMCPWarningsFunc = func(w io.Writer, res *config.Resolved) {}
	FlagValueFunc = flagValueLocal
	FlagVarFunc = flagVarLocal
	SliceErrorsFunc = func(context string, errs []string) error {
		if len(errs) == 0 {
			return nil
		}
		return fmt.Errorf("%s: %s", context, strings.Join(errs, "; "))
	}
}

// wireStackReadSeams wires the ledger read helpers over delivery's logic.
func wireStackReadSeams() {
	ParseStackPlanOutputFunc = delivery.ParseStackPlanOutput
	SeedStackLedgerFunc = func(l *workflowledger.Store, stackID string, chunks []delivery.ChunkPlan) error {
		return delivery.SeedStackLedger(context.Background(), l, stackID, chunks)
	}
	StackPlanInputsFunc = func(repo workflowledger.Repository, stackID string) (map[string]string, error) {
		return delivery.PlanInputs(context.Background(), repo, stackID)
	}
	StackTaskMapFunc = func(l *workflowledger.Store, stackID string) (map[string]workflowledger.Task, error) {
		return delivery.TaskMap(context.Background(), l, stackID)
	}
	StackMergedSetFunc = delivery.MergedSet
	AllChunksMergedFunc = delivery.AllChunksMerged
	LoadStackPlanOutputFunc = loadStackPlanOutputLocal
	LoadAllStackChunksFunc = loadAllStackChunksLocal
	StackRunRefFunc = stackRunRefLocal
	StackRunPushedFunc = stackRunPushedLocal
	StackRunHeadCommitFunc = stackRunHeadCommitLocal
	StackHeadBranchFunc = func(run workflowledger.RunSnapshot) string {
		if run.WorktreeName == "" {
			return ""
		}
		return "wf/" + run.WorktreeName
	}
	GitMergeCheckFunc = func(ctx context.Context, git delivery.GitRunner, pr delivery.PRClient, gc delivery.GitContext, headBranch, baseBranch, headCommit, repoSlug string, wasPushed bool) (bool, error) {
		return false, nil
	}
	StackDecomposedChunksFunc = delivery.DecomposedChunks
	StackingDriveAllowPublishFunc = func(compiled *definition.CompiledWorkflow) bool {
		return compiled != nil && compiled.Stacking != nil && compiled.Stacking.MergePolicy == "auto"
	}
}

// wireStackGateSeams wires the plan-run gate and drive seams.
func wireStackGateSeams() {
	ClassifyStackPlanRunDeliveryFunc = ClassifyStackPlanRunDeliveryImpl
	StackPlanRunFailureReasonFunc = StackPlanRunFailureReasonImpl
	ErrFailedStackPlanRunFunc = errFailedStackPlanRunLocal
	ErrUndrivenStackPlanRunFunc = errUndrivenStackPlanRunLocal
	SettleStackPlanRunIfCompleteFn = settleStackPlanRunIfCompleteLocal
	LoadAllStackChunksForDriveFunc = func(prepared *PreparedWorkflowRun, stackID string, planOutput []byte, planInputs map[string]string, stdout, stderr io.Writer) ([]delivery.ChunkPlan, bool, bool, string, error) {
		chunks, hasMore, remaining, err := loadAllStackChunksLocal(prepared.Repo, stackID)
		return chunks, hasMore, false, remaining, err
	}
	StackRunPublishWithheldFunc = stackRunPublishWithheldLocal
}

// wireSessionSeams wires the session, dispatcher, and messaging seams.
func wireSessionSeams() {
	LoadChatSkillsFunc = func(wsRoot string) (*skills.Registry, error) {
		globalPreview, err := config.LoadAgentsGlobal(wsRoot)
		if err != nil {
			return nil, err
		}
		reg, _, err := cliagents.LoadSessionSkills(wsRoot, globalPreview.LoadWorkspaceConfig)
		return reg, err
	}
	InstallHookSessionFunc = func(workspaceRoot string, staleBypass, quiet bool) (func(), error) { return func() {}, nil }
	InitCoordinatorFunc = func(d *runtime.Dispatcher, cfg config.SubagentConfig, repos ...ledger.LedgerRepository) coordinator.Coordinator {
		return coordinator.New(repos[0], subagents.New(d, subagents.Policy{Workers: 4}))
	}
	WorkflowBuildDispatcher = func(opts cliagents.SessionDispatcherOpts) (*runtime.Dispatcher, error) {
		d := runtime.New(runtime.Policy{})
		if opts.AgentRegistry != nil {
			for _, agent := range opts.AgentRegistry.List() {
				_ = d.Register(runtime.Subagent, agent.Name, providerRunHandler{completer: opts.Completer, model: opts.Model})
			}
		}
		return d, nil
	}
	InjectSkillResourceToolFunc = func(registry *tools.Registry, activation *skills.SkillActivation) (*tools.Registry, error) {
		clone := registry.Clone()
		if _, exists := clone.Get(tools.SkillResourceToolName); exists {
			return nil, fmt.Errorf("skill resource capability conflict")
		}
		clone.Register(tools.NewSkillResourceTool(
			func(ctx context.Context, id string) (string, string, error) {
				content, err := activation.Read(ctx, id)
				if err != nil {
					return "", "", err
				}
				return content.Text, "skill resource loaded: " + content.ID, nil
			},
			activation.ToolKey(),
			activation.ToolResultBudget(),
		))
		return clone, nil
	}
	InjectBaselineMessagingFunc = func(full, scoped *tools.Registry, cfg config.SubagentConfig, disallowed map[string]struct{}) {}
	MessagingDisallowedFunc = func(names []string) map[string]struct{} {
		out := map[string]struct{}{}
		for _, name := range names {
			out[name] = struct{}{}
		}
		return out
	}
	SessionAutoDeliveryRepairLoopFunc = sessionAutoDeliveryRepairLoopLocal
}

func loadAllStackChunksLocal(repo workflowledger.Repository, stackID string) (chunks []delivery.ChunkPlan, hasMore bool, remainingScope string, err error) {
	planOutput, err := LoadStackPlanOutputFunc(repo, stackID)
	if err != nil {
		return nil, false, "", err
	}
	mode, waveChunks, waveHasMore, waveRemaining, err := ParseStackPlanOutputFunc(planOutput)
	if err != nil {
		return nil, false, "", err
	}
	if mode != "multi" {
		return waveChunks, false, "", nil
	}
	chunks = append(chunks, waveChunks...)
	hasMore, remainingScope = waveHasMore, waveRemaining
	lastWave, err := latestDecomposeContinueWaveLocal(repo, stackID)
	if err != nil {
		return nil, false, "", err
	}
	for wave := 1; wave <= lastWave; wave++ {
		run, found, err := stackDecomposeContinueRunRefLocal(repo, stackID, wave)
		if err != nil {
			return nil, false, "", err
		}
		if !found {
			return nil, false, "", fmt.Errorf("stack %s: decompose continuation wave %d has an invocation key but no run", stackID, wave)
		}
		raw, err := LoadStackPlanOutputFunc(repo, run.RunID)
		if err != nil {
			return nil, false, "", fmt.Errorf("stack %s: decompose continuation wave %d: %w", stackID, wave, err)
		}
		_, waveChunks, waveHasMore, waveRemaining, err := ParseStackPlanOutputFunc(raw)
		if err != nil {
			return nil, false, "", fmt.Errorf("stack %s: decompose continuation wave %d: %w", stackID, wave, err)
		}
		chunks = append(chunks, waveChunks...)
		hasMore, remainingScope = waveHasMore, waveRemaining
	}
	return chunks, hasMore, remainingScope, nil
}

// latestDecomposeContinueWaveLocal mirrors cli.latestDecomposeContinueWave.
func latestDecomposeContinueWaveLocal(repo workflowledger.Repository, stackID string) (int, error) {
	prefix := stackID + ":decompose:"
	runs, err := repo.ListRuns(context.Background())
	if err != nil {
		return 0, err
	}
	best := 0
	for _, r := range runs {
		if !strings.HasPrefix(r.InvocationKey, prefix) {
			continue
		}
		n, convErr := strconv.Atoi(strings.TrimPrefix(r.InvocationKey, prefix))
		if convErr != nil {
			continue
		}
		if n > best {
			best = n
		}
	}
	return best, nil
}

// stackDecomposeContinueRunRefLocal mirrors
// cli.stackDecomposeContinueRunRef.
func stackDecomposeContinueRunRefLocal(repo workflowledger.Repository, stackID string, wave int) (workflowledger.RunSnapshot, bool, error) {
	key := fmt.Sprintf("%s:decompose:%d", stackID, wave)
	runs, err := repo.ListRuns(context.Background())
	if err != nil {
		return workflowledger.RunSnapshot{}, false, err
	}
	var best workflowledger.RunSnapshot
	found := false
	for _, r := range runs {
		if r.InvocationKey != key {
			continue
		}
		if !found || r.StartedAt.After(best.StartedAt) {
			best = r
			found = true
		}
	}
	return best, found, nil
}

// loadStackPlanOutputLocal mirrors cli.loadStackPlanOutput (stack_state.go):
// the newest succeeded decompose attempt's output content.
func loadStackPlanOutputLocal(repo workflowledger.Repository, stackID string) ([]byte, error) {
	return delivery.LoadStackPlanOutput(context.Background(), repo, stackID)
}

// stackRunPushedLocal mirrors cli.stackRunPushed (stack_state.go).
func stackRunPushedLocal(repo workflowledger.Repository, run workflowledger.RunSnapshot) bool {
	records, err := repo.ListDeliveries(context.Background(), run.RunID)
	if err != nil {
		return false
	}
	for _, rec := range records {
		if rec.CommitSHA == "" {
			continue
		}
		switch rec.Status {
		case "pushed", "succeeded":
			return true
		}
	}
	return false
}

// stackRunHeadCommitLocal mirrors cli.stackRunHeadCommit (stack_state.go).
func stackRunHeadCommitLocal(repo workflowledger.Repository, run workflowledger.RunSnapshot) string {
	records, err := repo.ListDeliveries(context.Background(), run.RunID)
	if err != nil {
		return ""
	}
	for _, rec := range records {
		if rec.CommitSHA == "" {
			continue
		}
		switch rec.Status {
		case "pushed", "succeeded":
			return rec.CommitSHA
		}
	}
	return ""
}

// settleStackPlanRunIfCompleteLocal mirrors cli.settleStackPlanRunIfComplete
// (stack_drive.go) over the local seams.
func settleStackPlanRunIfCompleteLocal(ctx context.Context, prepared *PreparedWorkflowRun, stackID string, stdout io.Writer) error {
	switch gate := ClassifyStackPlanRunDeliveryFn(ctx, prepared.Root, prepared.Store, prepared.Repo, stackID, true); gate {
	case stackPlanRunNotApplicable, stackPlanRunIncomplete:
		return nil
	case stackPlanRunFailed:
		return RefuseFailedStackPlanRunDelivery(ctx, prepared.Root, prepared.Store, prepared.Repo, stackID)
	case stackPlanRunComplete:
		if SkipParkedPlanRunPublication(ctx, prepared.Store, prepared.Repo, stackID) {
			if err := SettlePlanRunSkippedDelivery(ctx, prepared.Repo, stackID); err != nil {
				return fmt.Errorf("stack drive: settle plan run: %w", err)
			}
			fmt.Fprintf(stdout, "stack %s: plan run settled (plan PR not created; delivery.deliver_plan_run=false)\n", stackID)
			return nil
		}
		fmt.Fprintf(stdout, "stack %s: plan run ready for delivery: mivia workflow deliver %s --allow-publish\n", stackID, stackID)
	default:
		return fmt.Errorf("stack drive: stack %s has an unknown plan run classification (%d)", stackID, int(gate))
	}
	return nil
}

// flagValueLocal mirrors cli.flagValue (root.go): --name VALUE and
// --name=VALUE forms, refusing a missing or dash-prefixed value.
func flagValueLocal(args []string, names ...string) (string, []string, bool, error) {
	out := make([]string, 0, len(args))
	var val string
	found := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		matched := false
		for _, n := range names {
			if a == n {
				if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
					return "", nil, found, fmt.Errorf("%s requires a value", n)
				}
				val = args[i+1]
				found = true
				i++
				matched = true
				break
			}
			if strings.HasPrefix(a, n+"=") {
				val = strings.TrimPrefix(a, n+"=")
				found = true
				matched = true
				break
			}
		}
		if !matched {
			out = append(out, a)
		}
	}
	return val, out, found, nil
}

// flagVarLocal mirrors cli.flagVar (root.go): repeatable string flags.
func flagVarLocal(args []string, names ...string) ([]string, []string, bool, error) {
	var vals []string
	rest := make([]string, 0, len(args))
	found := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		matched := false
		for _, n := range names {
			if a == n {
				if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
					return nil, nil, found, fmt.Errorf("%s requires a value", n)
				}
				vals = append(vals, args[i+1])
				found = true
				i++
				matched = true
				break
			}
			if strings.HasPrefix(a, n+"=") {
				vals = append(vals, strings.TrimPrefix(a, n+"="))
				found = true
				matched = true
				break
			}
		}
		if !matched {
			rest = append(rest, a)
		}
	}
	return vals, rest, found, nil
}

// providerRunHandler routes each subagent request through the configured
// provider completer, so a child test process performs the real HTTP
// round-trip the kill-recovery fixture observes.
type providerRunHandler struct {
	completer provider.Completer
	model     string
}

// Invoke calls the completer and wraps its content in the step schema shape.
func (h providerRunHandler) Invoke(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
	if h.completer == nil {
		return json.RawMessage(`{"ok":true}`), nil
	}
	if _, err := h.completer.Chat(ctx, provider.Request{Model: h.model, Messages: []provider.Message{{Role: "user", Content: string(req.Input)}}}); err != nil {
		return nil, err
	}
	return json.RawMessage(`{"ok":true}`), nil
}

// errFailedStackPlanRunLocal mirrors cli.errFailedStackPlanRun.
func errFailedStackPlanRunLocal(runID, reason string) error {
	return fmt.Errorf("workflow run %q is the plan run of a stack that cannot complete: %s - use `mivia stack drive <workflow> --stack %s` to inspect, or delete the run if the failure is unrecoverable", runID, reason, runID)
}

// errUndrivenStackPlanRunLocal mirrors cli.errUndrivenStackPlanRun.
func errUndrivenStackPlanRunLocal(runID string) error {
	return fmt.Errorf("workflow run %q is the plan run of a stack that has not fully driven yet: finish it with `mivia stack drive <workflow> --stack %s`, then settle the plan run with `mivia workflow deliver %s` - delivering it now would abandon the undriven stack while reporting the plan run succeeded", runID, runID, runID)
}

// sessionAutoDeliveryRepairLoopLocal mirrors cli.sessionAutoDeliveryRepairLoop
// (session_delivery_repair.go) over cliworkflow-owned functions.
func sessionAutoDeliveryRepairLoopLocal(runCtx context.Context, repo workflowledger.Repository, root string, res *config.Resolved, store *storage.SQLite, runID string, advance func(context.Context) (workflowledger.RunSnapshot, error), driveStack func(context.Context) (bool, error), deliverPlanRun bool) {
	snap, err := advance(runCtx)
	if err != nil {
		SettleSessionRunFailure(repo, runID, err)
		return
	}
	for {
		if snap.Status != workflowledger.RunStatusDeliveryPending {
			return
		}
		if driveStack != nil {
			driveCtx, cancelDrive := context.WithTimeout(runCtx, WorkflowAutoDeliveryAttemptTimeout)
			drove, err := driveStack(driveCtx)
			cancelDrive()
			if err != nil {
				log.Printf("workflow: run %s stack drive before delivery: %v", runID, err)
				return
			}
			if drove && !deliverPlanRun {
				if err := SettlePlanRunSkippedDelivery(context.Background(), repo, runID); err != nil {
					log.Printf("workflow: run %s settle skipped plan run: %v", runID, err)
				}
				return
			}
		}
		if StackRunPublishWithheldFunc(runCtx, repo, runID, false) {
			return
		}
		deliverCtx, cancelDeliver := context.WithTimeout(runCtx, WorkflowDeliveryTimeout)
		deliverErr := DeliverRunWithStore(deliverCtx, root, res, store, repo, runID, true, false, io.Discard, io.Discard)
		cancelDeliver()
		if deliverErr != nil && !DeliveryFaultTransient(deliverErr) {
			RecordAutoDeliveryFailure(context.Background(), repo, runID, deliverErr)
		}
		fresh, getErr := repo.GetRun(context.Background(), runID)
		if getErr != nil {
			log.Printf("workflow: run %s auto-delivery repair: re-read after delivery failed: %v", runID, getErr)
			return
		}
		if workflowledger.IsTerminalRunStatus(fresh.Status) || fresh.Status == workflowledger.RunStatusDeliveryPending {
			return
		}
		if !(fresh.Status == workflowledger.RunStatusRunning && fresh.ActiveStepID != "" && !workflowledger.IsTerminalStepID(fresh.ActiveStepID)) {
			log.Printf("workflow: run %s auto-delivery repair: unexpected status %q; stopping loop", runID, fresh.Status)
			return
		}
		snap, err = advance(runCtx)
		if err != nil {
			SettleSessionRunFailure(repo, runID, err)
			return
		}
	}
}

// stackRunRefLocal mirrors cli.stackRunRef (stack_reconcile.go): the newest
// run admitted under the stable chunk admission key.
func stackRunRefLocal(repo workflowledger.Repository, stackID, chunkID string) (workflowledger.RunSnapshot, bool, error) {
	key, err := delivery.AdmissionKey(stackID, chunkID)
	if err != nil {
		return workflowledger.RunSnapshot{}, false, err
	}
	runs, err := repo.ListRuns(context.Background())
	if err != nil {
		return workflowledger.RunSnapshot{}, false, err
	}
	var best workflowledger.RunSnapshot
	found := false
	for _, r := range runs {
		if r.InvocationKey != key {
			continue
		}
		if !found || r.StartedAt.After(best.StartedAt) {
			best = r
			found = true
		}
	}
	return best, found, nil
}

// stackRunPublishWithheldLocal mirrors cli.stackRunPublishWithheld
// (stack_publish_gate.go): a stack run under a non-auto merge policy stays
// parked for the human publish grant.
func stackRunPublishWithheldLocal(ctx context.Context, repo workflowledger.Repository, runID string, quiet bool) bool {
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		return false
	}
	raw, err := repo.GetRunSnapshot(ctx, runID)
	if err != nil {
		return false
	}
	snapshot, compiled, _, err := ValidateWorkflowResumeSnapshot(run, raw)
	if err != nil || compiled == nil || compiled.Stacking == nil {
		return false
	}
	mode := snapshot.Inputs["stack_mode"]
	isStackRun := false
	switch mode {
	case "chunk":
		isStackRun = snapshot.Inputs["chunk"] != "" && snapshot.Inputs["stack_part"] != ""
	case "single", "decompose_continue":
		isStackRun = true
	}
	if !isStackRun {
		return false
	}
	i := strings.Index(run.InvocationKey, ":")
	if i <= 0 {
		return false
	}
	planRun, err := repo.GetRun(ctx, run.InvocationKey[:i])
	if err != nil {
		if !quiet {
			log.Printf("workflow: session recovery: %s is a stack chunk/integration run, but its stack plan run is missing or unresolvable; leaving parked", runID)
		}
		return true
	}
	planRaw, err := repo.GetRunSnapshot(ctx, planRun.RunID)
	if err != nil {
		if !quiet {
			log.Printf("workflow: session recovery: %s is a stack chunk/integration run, but its stack plan run is missing or unresolvable; leaving parked", runID)
		}
		return true
	}
	_, planCompiled, _, err := ValidateWorkflowResumeSnapshot(planRun, planRaw)
	if err != nil || planCompiled == nil || planCompiled.Stacking == nil || planCompiled.Stacking.MergePolicy != "auto" {
		if !quiet {
			log.Printf("workflow: session recovery: %s is a stack chunk/integration run awaiting a human publish grant (merge_policy != auto); leaving parked", runID)
		}
		return true
	}
	return false
}
