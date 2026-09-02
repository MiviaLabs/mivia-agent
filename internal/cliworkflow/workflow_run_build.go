package cliworkflow

// Workflow run construction and delivery admission. Split out of
// workflow_run.go so the entrypoint/dispatch file stays under the
// go-structure soft line cap; these helpers build the controller, wire its
// dispatcher and verifiers, and decide whether a fresh delivery-required run
// may publish.

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/secretpath"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	workflowspace "github.com/MiviaLabs/mivia-agent/internal/workflows/localengine"
)

type WorkflowControllerBuild struct {
	Controller *controller.LinearController
	Dispatcher interface{ Close() }
	Admission  controller.Admission
	Cleanup    func()
}

// buildWorkflowController builds one workflow run's controller. ownerSessionID
// is the chat session that owns the run's child runs: empty on the operator CLI
// paths (no session exists there), the admitting session's ID on the
// workflow_run tool path. It threads down as an explicit parameter from the
// admission entrypoint - the only frame where the session caller exists - and
// no wiring layer below re-derives it from ctx.
func buildWorkflowController(root string, res *config.Resolved, store *storage.SQLite, repo workflowledger.Repository, wf *definition.CompiledWorkflow, refBase string, inputs map[string]any, inputSnapshot map[string]string, definitionTOML []byte, runID string, prior *workflowledger.Snapshot, priorRaw []byte, recorded *workflowledger.RunSnapshot, remaining map[string]bool, preloadedSkills *skills.Registry, ownerSessionID string, sessionRepo ledger.LedgerRepository) (WorkflowControllerBuild, error) {
	// A stacking run EXECUTES the synthesized graph: decompose and
	// chunk_plan_validate are real steps whose agents must be resolved, whose
	// templates and schemas must be pinned, and whose routing digests must be
	// recorded before anything is admitted. Synthesize here - where the agent
	// registry is available - so the runtime build treats them like any
	// declared step. Synthesizing inside the controller instead would admit
	// them with a null routing snapshot and fail every dispatch attempt.
	synth, err := workflowSynthesizeRunGraph(wf)
	if err != nil {
		return WorkflowControllerBuild{}, err
	}
	wf = synth
	setup, err := prepareWorkflowBuild(root, res, wf, runID, recorded, delivery.EffectiveBase(wf, inputSnapshot), preloadedSkills)
	if err != nil {
		return WorkflowControllerBuild{}, err
	}
	dispatcher, opts, legacy, err := newWorkflowDispatcher(res, store, setup)
	if err != nil {
		setup.cleanup()
		return WorkflowControllerBuild{}, err
	}
	runtime, baseline, err := prepareWorkflowBuildRuntime(root, refBase, res, wf, inputSnapshot, definitionTOML, prior, priorRaw, setup, opts, remaining)
	if err != nil {
		dispatcher.Close()
		setup.cleanup()
		return WorkflowControllerBuild{}, err
	}
	ctrl, err := newWorkflowController(repo, dispatcher, legacy, res, wf, inputs, runID, runtime, setup.identity, baseline, ownerSessionID, sessionRepo)
	if err != nil {
		dispatcher.Close()
		setup.cleanup()
		return WorkflowControllerBuild{}, err
	}
	return WorkflowControllerBuild{Controller: ctrl, Dispatcher: dispatcher, Admission: workflowAdmission(setup, inputSnapshot, recorded), Cleanup: setup.cleanup}, nil
}

// workflowSynthesizeRunGraph applies the stacking synthesis an admitted run
// executes on. A stacking run graph carries engine-synthesized steps
// (decompose, chunk_plan_validate) that no author declared; they must be part
// of the workflow BEFORE the runtime build so their agents are resolved from
// the registry, their templates and schemas are pinned into the snapshot, and
// their routing digests are recorded - exactly like any declared step. A step
// admitted without a routing digest could never dispatch (the agent handler
// refuses null routing snapshots) and could never resume, so synthesis belongs
// here, next to the registry, and never inside the controller.
func workflowSynthesizeRunGraph(wf *definition.CompiledWorkflow) (*definition.CompiledWorkflow, error) {
	if wf == nil || wf.Stacking == nil {
		return wf, nil
	}
	synth, err := definition.SynthesizeStacking(wf)
	if err != nil {
		return nil, fmt.Errorf("synthesize stacking run graph: %w", err)
	}
	return synth, nil
}

type workflowBuildSetup struct {
	skills    *skills.Registry
	loaded    cliagents.AgentLoadResult
	authority *tools.Registry
	identity  workflowspace.Identity
	cleanup   func()
	remoteURL string
	// originBaseCommit is the delivery target's fetched origin tip at
	// admission (set only on a fresh delivery-active admission). Empty
	// means workflowAdmission falls back to identity.OriginBaseCommit (the
	// source branch's tracking ref, or a resumed run's recorded value).
	originBaseCommit string
}

func prepareWorkflowBuild(root string, res *config.Resolved, wf *definition.CompiledWorkflow, runID string, recorded *workflowledger.RunSnapshot, effectiveBase string, preloadedSkills *skills.Registry) (workflowBuildSetup, error) {
	// A resume passes the registry it already loaded for acceptance so the
	// whole invocation observes one load (R5); every other caller passes nil
	// and loads here.
	skillReg := preloadedSkills
	if skillReg == nil {
		loadedSkills, err := WorkflowBuildLoadSkills(root)
		if err != nil {
			return workflowBuildSetup{}, err
		}
		skillReg = loadedSkills
	}
	loaded, err := workflowBuildLoadAgents(root, "", skillReg)
	if err != nil {
		return workflowBuildSetup{}, err
	}
	if err := SliceErrorsFunc("workflow", definition.ValidateAgentSkillReferences(wf, loaded.Registry, skillReg)); err != nil {
		return workflowBuildSetup{}, err
	}
	authority, err := workflowBuildRegistry(root, res)
	if err != nil {
		return workflowBuildSetup{}, err
	}
	writeCapable, err := workflowWriteAuthority(wf, loaded.Registry, authority, loaded.Global.MandatoryToolDenylistAdditions)
	if err != nil {
		return workflowBuildSetup{}, err
	}
	identity, cleanup, err := workflowBuildWorkspace(context.Background(), root, runID, writeCapable, recorded)
	if err != nil {
		return workflowBuildSetup{}, err
	}
	authority, err = workflowBuildRegistry(identity.Root, res)
	if err != nil {
		cleanup()
		return workflowBuildSetup{}, err
	}
	closeMCP, err := cliagents.AddMCPTools(authority, res, workflowMCPServers(wf, loaded.Registry))
	if err != nil {
		cleanup()
		return workflowBuildSetup{}, err
	}
	previousCleanup := cleanup
	cleanup = func() {
		closeMCP()
		previousCleanup()
	}
	remoteURL, originBaseCommit, err := workflowBuildRemoteURL(wf, identity, writeCapable, recorded, effectiveBase)
	if err != nil {
		cleanup()
		return workflowBuildSetup{}, err
	}
	return workflowBuildSetup{skills: skillReg, loaded: loaded, authority: authority, identity: identity, cleanup: cleanup, remoteURL: remoteURL, originBaseCommit: originBaseCommit}, nil
}

func workflowBuildRemoteURL(wf *definition.CompiledWorkflow, identity workflowspace.Identity, writeCapable bool, recorded *workflowledger.RunSnapshot, effectiveBase string) (originURL, originBaseCommit string, err error) {
	if recorded != nil || !deliveryRequiresPublication(wf) {
		return "", "", nil
	}
	return workflowDeliveryAdmission(wf, identity, writeCapable, effectiveBase)
}

func newWorkflowDispatcher(res *config.Resolved, store *storage.SQLite, setup workflowBuildSetup) (*runtime.Dispatcher, cliagents.SessionDispatcherOpts, ledger.LedgerRepository, error) {
	comp, err := workflowBuildProvider(res)
	if err != nil {
		return nil, cliagents.SessionDispatcherOpts{}, nil, err
	}
	legacy := ledger.NewStorageLedgerRepository(store)
	// A workflow runs headless, so there is no operator to prompt and no gate
	// to attach - but an explicit "deny" is an instruction, not a prompt, and
	// a runner that ignores it is a hole. The configured policy travels; with
	// no gate, DecideApproval denies anything that would need one.
	approval := func() sdkadapter.ApprovalDeps {
		return sdkadapter.ApprovalDeps{Policy: res.Approvals.ApprovalPolicy()}
	}
	opts := cliagents.SessionDispatcherOpts{Approval: approval, ToolDenylist: setup.loaded.Global.MandatoryToolDenylistAdditions, Registry: setup.authority, AuthorityRegistry: setup.authority, Completer: comp, Model: res.Model, ProviderName: res.ProviderName, AllowWorkspaceAgentProviders: setup.loaded.Global.AllowWorkspaceAgentProviders, ModelCatalog: res.ModelCatalog(), CompleterFactory: cliagents.NewProviderCompleterFactory(res), Config: res.Subagents, MCP: res.MCP, Repo: legacy, SharedSQLite: store, SkillReg: setup.skills, WorkflowSkillSnapshots: make(map[string]workflowledger.RefSnapshot), AgentRegistry: setup.loaded.Registry, WorkspaceRoot: setup.identity.Root, ToolRunTimeout: config.SaturatingSeconds(res.Tools.ToolRunTimeoutSec)}
	dispatcher, err := WorkflowBuildDispatcher(opts)
	if err != nil {
		return nil, cliagents.SessionDispatcherOpts{}, nil, err
	}
	return dispatcher, opts, legacy, nil
}

func prepareWorkflowBuildRuntime(root, refBase string, res *config.Resolved, wf *definition.CompiledWorkflow, inputs map[string]string, definition []byte, prior *workflowledger.Snapshot, priorRaw []byte, setup workflowBuildSetup, opts cliagents.SessionDispatcherOpts, remaining map[string]bool) (preparedWorkflowRuntime, *definition.GoModuleBaseline, error) {
	runtime, err := prepareWorkflowRuntime(root, refBase, wf, setup.loaded.Registry, prior, priorRaw, definition, inputs, opts)
	if err != nil {
		return preparedWorkflowRuntime{}, nil, err
	}
	if err := VerifyWorkflowSkillSnapshotScoped(wf, setup.skills, prior, remaining); err != nil {
		return preparedWorkflowRuntime{}, nil, err
	}
	if err := verifyWorkflowVerifierSnapshot(wf, res.Verifiers, prior); err != nil {
		return preparedWorkflowRuntime{}, nil, err
	}
	if prior == nil {
		// Fail a fresh run at admission, before any agent step burns tokens,
		// when a gate references an undeclared profile.
		if err := validateWorkflowVerifierReferences(wf, res.Verifiers); err != nil {
			return preparedWorkflowRuntime{}, nil, err
		}
		runtime.Snapshot, err = PinWorkflowSkills(runtime.Snapshot, wf, setup.skills)
		if err != nil {
			return preparedWorkflowRuntime{}, nil, err
		}
		runtime.Snapshot, err = pinWorkflowVerifierDefinitions(runtime.Snapshot, wf, res.Verifiers)
		if err != nil {
			return preparedWorkflowRuntime{}, nil, err
		}
	}
	// Install the skill pins the dispatcher hydrates and executes at dispatch
	// time (activateSkill). On a resume the controller snapshot carries the
	// STORED admission bytes verbatim (priorRaw) for StartNew byte comparison,
	// but the DISPATCHER must see the pins that verification accepted: when
	// --accept-skill-change rewrote the in-memory prior, that rewrite is what
	// the build-time guard verified, so dispatch must hydrate from the same
	// accepted pins or the resumed step dies at dispatch on a stale pin. A
	// fresh run has no prior, so runtime.Snapshot already carries the
	// just-pinned skills and is used as-is.
	if prior != nil {
		if err := InstallWorkflowSkillSnapshotsFromSnapshot(opts.WorkflowSkillSnapshots, prior); err != nil {
			return preparedWorkflowRuntime{}, nil, err
		}
	} else if err := InstallWorkflowSkillSnapshots(opts.WorkflowSkillSnapshots, runtime.Snapshot); err != nil {
		return preparedWorkflowRuntime{}, nil, err
	}
	needBaseline, err := workflowNeedsGoBaseline(wf, res.Verifiers, prior)
	if err != nil {
		return preparedWorkflowRuntime{}, nil, err
	}
	baseline, err := workflowModuleBaseline(needBaseline, setup.identity.Root, prior)
	if err != nil {
		return preparedWorkflowRuntime{}, nil, err
	}
	if baseline != nil && prior == nil {
		runtime.Snapshot, err = pinWorkflowModuleBaseline(runtime.Snapshot, baseline)
		if err != nil {
			return preparedWorkflowRuntime{}, nil, err
		}
	}
	return runtime, baseline, nil
}

func newWorkflowController(repo workflowledger.Repository, dispatcher *runtime.Dispatcher, legacy ledger.LedgerRepository, res *config.Resolved, wf *definition.CompiledWorkflow, inputs map[string]any, runID string, runtime preparedWorkflowRuntime, identity workflowspace.Identity, baseline *definition.GoModuleBaseline, ownerSessionID string, sessionRepo ledger.LedgerRepository) (*controller.LinearController, error) {
	applyHarnessSandboxSetting(res.Harness)
	coord := InitCoordinatorFunc(dispatcher, res.Subagents, legacy)
	runner := controller.NewCoordinatorRunner(coord)
	// Register every child run this controller ensures under the owning
	// session, so inspect_agents/join_run/cancel_run resolve workflow children
	// the same way they resolve dispatch_tasks runs.
	runner.RegisterChildRun = workflowChildRunRegistrar(dispatcher, coord, res.Subagents, ownerSessionID, sessionRepo)
	ctrl, err := workflowBuildController(repo, runner, wf, runtime.Steps, inputs, runID, runtime.Snapshot)
	if err != nil {
		return nil, err
	}
	// Wire the runner's step-heartbeat emitter into the controller's progress
	// sink so a live agent-step join stays observable (a step_heartbeat per
	// watchdog tick). EmitProgress is nil-safe when no sink is attached.
	runner.SetProgressEmitter(func(e controller.ProgressEvent) { ctrl.EmitProgress(e) })
	policy, err := secretpath.New(res.Tools.SecretPathPatterns, res.Tools.SecretPathExceptions)
	if err != nil {
		return nil, err
	}
	catalogue, err := workflowVerifierCatalogue(res.Verifiers, policy)
	if err != nil {
		return nil, err
	}
	if err := ctrl.SetVerifiers(catalogue); err != nil {
		return nil, err
	}
	if err := ctrl.SetSecretPolicy(policy); err != nil {
		return nil, err
	}
	if err := ctrl.SetWritePathBlocklist(effectiveWorkflowWriteDenylist(res)); err != nil {
		return nil, err
	}
	if err := ctrl.SetPanelLimits(effectiveWorkflowPanelLimits(res)); err != nil {
		return nil, err
	}
	if err := ctrl.SetWorkDir(identity.Root); err != nil {
		return nil, err
	}
	// Wire the run's pinned git context so the controller's post-implement
	// diff-size gate can measure the worktree diff itself (the delivery-time
	// gate remains authoritative either way). Best-effort: a worktree that
	// cannot be verified keeps the gate off and delivery-time enforcement as
	// the only guard.
	if identity.Root != "" && identity.MainRoot != "" && identity.WorktreeName != "" {
		if gitDir, gerr := delivery.VerifyGitDir(context.Background(), identity.MainRoot, identity.WorktreeName, identity.Root); gerr == nil {
			if serr := ctrl.SetGitContext(delivery.GitContext{Dir: identity.Root, GitDir: gitDir}); serr != nil {
				return nil, serr
			}
		}
	}
	if err := ctrl.SetPanelLimiter(processWorkflowServices().panelLimiter); err != nil {
		return nil, err
	}
	if baseline != nil {
		if err := ctrl.SetModuleBaseline(baseline); err != nil {
			return nil, err
		}
	}
	return ctrl, nil
}

// warnSandboxDisabledOnce prints the escape-hatch warning at most once per
// process: newWorkflowController may run more than once across resume/retry
// paths within a single CLI invocation, and the operator only needs to hear
// this at startup, not on every workflow build.
var warnSandboxDisabledOnce sync.Once

// applyHarnessSandboxSetting installs the process-wide verifier sandbox
// toggle from [harness] sandbox. This is a harness-level setting, not a
// per-verifier or per-run one: it decides HOW every evidence-gate command
// this process runs is executed, regardless of which project or verifier
// declared it.
func applyHarnessSandboxSetting(hc config.HarnessConfig) {
	enabled := hc.SandboxEnabled()
	definition.SetSandboxEnabled(enabled)
	if !enabled {
		warnSandboxDisabledOnce.Do(func() {
			fmt.Fprintln(os.Stderr, "warning: [harness] sandbox = false — evidence-gate commands run directly on the host with no filesystem, network, or environment isolation")
		})
	}
}

// workflowOwnerSessionID resolves the owning chat session's ID from an
// admission context. The workflow_run tool admission is the only frame where
// the session caller exists; every layer below receives the value as an
// explicit parameter and never reads ctx again.
func workflowOwnerSessionID(ctx context.Context) string {
	caller, ok := runtime.CallerFrom(ctx)
	if !ok {
		return ""
	}
	return caller.SessionID
}

// workflowChildRunRegistrar returns the CoordinatorRunner hook that registers
// each workflow child run in the orchestration handle registry. The record
// carries TWO identities on purpose:
//   - ownership: the ownerSessionID principal and sessionRepo - the OWNING
//     SESSION's ledger repository, the same instance the session's
//     inspect_agents/join_run/cancel_run tools carry. The access gate compares
//     exactly this repo, so this is what makes the child resolvable by its
//     owner. The seam applies the effective-repo fallback to it.
//   - execution: coord, handle, and d are this workflow run's own - acting on
//     the child (join, cancel) must go through the coordinator that runs it.
//
// Registration is additive: it changes no coordinator or pool behavior.
//
// Fail-closed by design: with no owning session, or a session without a repo
// (the operator CLI paths), the hook stays unset, one notice says so, and the
// standard tools keep answering "unknown run_id" for the run's children - a
// run registered under no session repo could never be inspected or canceled
// by anyone.
func workflowChildRunRegistrar(d *runtime.Dispatcher, coord coordinator.Coordinator, cfg config.SubagentConfig, ownerSessionID string, sessionRepo ledger.LedgerRepository) func(context.Context, string, *coordinator.RunHandle) {
	if strings.TrimSpace(ownerSessionID) == "" || sessionRepo == nil {
		log.Printf("workflow: child run registration skipped: no owning session or session ledger repo")
		return nil
	}
	return func(_ context.Context, runID string, handle *coordinator.RunHandle) {
		if err := cliorchestrate.RegisterChildRunHandle(runID, coord, handle, sessionRepo, d, ownerSessionID, cfg); err != nil {
			log.Printf("workflow: child run %s registration failed: %v", runID, err)
		}
	}
}

func workflowAdmission(setup workflowBuildSetup, inputs map[string]string, recorded *workflowledger.RunSnapshot) controller.Admission {
	// The delivery target's fetched origin tip (set only on a fresh
	// delivery-active admission) overrides identity's source-branch-derived
	// OriginBaseCommit: delivery-time rewrite detection must compare
	// against the TARGET branch's history, not the branch this run's
	// worktree happened to start from.
	originBaseCommit := setup.identity.OriginBaseCommit
	if setup.originBaseCommit != "" {
		originBaseCommit = setup.originBaseCommit
	}
	admission := controller.Admission{BaseRef: setup.identity.BaseRef, BaseCommit: setup.identity.BaseCommit, OriginBaseCommit: originBaseCommit, WorktreeName: setup.identity.WorktreeName, InputDigest: workflowledger.InputDigest(inputs), RemoteURL: setup.remoteURL}
	if recorded != nil {
		admission.InputDigest, admission.DeadlineAt, admission.RemoteURL = recorded.InputDigest, recorded.DeadlineAt, recorded.RemoteURL
		// Both of these are compared by sameAdmission, so both must come from the
		// record and never from a value this binary recomputed or left empty. The
		// invocation key is written only on the fresh-start path, so a run started
		// with one could never resume; the digest moves whenever the definition
		// types gain a field.
		admission.InvocationKey, admission.WorkflowDigest = recorded.InvocationKey, recorded.WorkflowDigest
	}
	return admission
}

// workflowDeliveryProbe checks the provider's PR tool at admission. It is a
// variable so tests can drive the missing-tool path without changing PATH.
var workflowDeliveryProbe = delivery.ProbePRTool

// workflowDeliveryAdmission verifies that a fresh delivery-required run can
// publish: the workflow must be write-capable, the repository must have an
// origin remote, and the delivery base must CONTAIN the admitted worktree
// base commit (delivery.AdmitDeliveryTarget). It returns the resolved origin
// URL and the target's fetched origin tip, both for the immutable admission
// record. The base admitted against is the EFFECTIVE base: the caller resolves
// a valid pr_base input override once (delivery.EffectiveBase), and admission
// uses it instead of the workflow-declared policy.Base, because delivery
// honors the same override at publish time. An empty effectiveBase resolves to
// the declared base. The fetch is bounded by WorkflowDeliveryTimeout so an
// offline or hung origin cannot block run creation forever.
func workflowDeliveryAdmission(wf *definition.CompiledWorkflow, identity workflowspace.Identity, writeCapable bool, effectiveBase string) (originURL, originBaseCommit string, err error) {
	if !writeCapable {
		return "", "", fmt.Errorf("workflow %s declares delivery but its agents cannot write files; delivery requires a run worktree", wf.Name)
	}
	policy, ok := delivery.FromCompiled(wf)
	if !ok {
		return "", "", fmt.Errorf("workflow %s delivery policy is not active", wf.Name)
	}
	if err := workflowDeliveryProbe(policy.Provider); err != nil {
		return "", "", err
	}
	if effectiveBase == "" {
		effectiveBase = policy.Base
	}
	// Bound the admission fetch: an offline or hung origin must not block run
	// creation forever. The same timeout that bounds one delivery attempt
	// bounds the admission fetch, so both read WorkflowDeliveryTimeout.
	ctx, cancel := context.WithTimeout(context.Background(), WorkflowDeliveryTimeout)
	defer cancel()
	gitCtx := delivery.GitContext{Dir: identity.MainRoot, GitDir: filepath.Join(identity.MainRoot, ".git")}
	return delivery.AdmitDeliveryTarget(ctx, WorkflowDeliverGit, gitCtx, effectiveBase, identity.BaseCommit)
}

// deliveryRequiresPublication reports whether the workflow's delivery policy
// must publish a pull request when the run settles at its success terminal.
func deliveryRequiresPublication(wf *definition.CompiledWorkflow) bool {
	return wf.DeliveryActive()
}
