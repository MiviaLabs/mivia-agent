package workflow

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
	"strconv"
	"strings"
	"testing"

	cliagents "github.com/MiviaLabs/mivia-agent/internal/cli/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/gittest"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/testenv"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func TestMain(m *testing.M) {
	gittest.DisableDetachedMaintenance()
	// Isolate the home directory before anything resolves configuration.
	// config.Load merges the USER-level [mcp] table (LoadTrustedMCPConfig
	// reads config.UserConfigPath) into every resolved config, even behind an
	// explicit ConfigPath - so on a developer machine with MCP servers
	// configured, every fixture here got res.MCP.Enabled = true while its
	// ledger rows pinned no digest, and validateWorkflowMCPConfigDigest
	// correctly refused 19 resumes. The suite also writes through
	// workspace.GlobalContextStorePath. See internal/testenv, and
	// home_isolation_test.go for the assertion that keeps this call here.
	restoreHome, err := testenv.IsolateHome()
	if err != nil {
		// Continuing unprotected would both write into the real home and
		// let ambient configuration decide test outcomes.
		fmt.Fprintf(os.Stderr, "testenv: %v\n", err)
		os.Exit(1)
	}
	wireTestSeams()
	cliagents.WireWorkflowToolOptionsVar = WireWorkflowToolOptions
	// os.Exit skips deferred calls, so restore explicitly before exiting.
	code := m.Run()
	restoreHome()
	os.Exit(code)
}

// wireTestSeams installs every cli-backed seam default that does not need
// internal/cli.
func wireTestSeams() {
	wireStackReadSeams()
	wireStackGateSeams()
	wireSessionSeams()
	InitCLIDefaults()
}

// wireStackReadSeams no longer overrides anything: the stack helper seams
// are initialized to the package's own (moved) production implementations,
// which is exactly what the tests must exercise.
func wireStackReadSeams() {}

// wireStackGateSeams no longer overrides anything (see wireStackReadSeams).
func wireStackGateSeams() {}

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
	InitCoordinatorFunc = func(d *runtime.Dispatcher, cfg config.SubagentConfig, repos ...ledger.LedgerRepository) *coordinator.Coordinator {
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
	InjectBaselineMessagingFunc = func(full, scoped *tools.Registry, cfg config.SubagentConfig, disallowed map[string]struct{}) {}
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
	case StackPlanRunNotApplicable, StackPlanRunIncomplete:
		return nil
	case StackPlanRunFailed:
		return RefuseFailedStackPlanRunDelivery(ctx, prepared.Root, prepared.Store, prepared.Repo, stackID)
	case StackPlanRunComplete:
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
