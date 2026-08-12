package cli

// Workflow run construction and delivery admission. Split out of
// workflow_run.go so the entrypoint/dispatch file stays under the
// go-structure soft line cap; these helpers build the controller, wire its
// dispatcher and verifiers, and decide whether a fresh delivery-required run
// may publish.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/secretpath"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/verifier"
	workflowspace "github.com/MiviaLabs/mivia-agent/internal/workflows/workspace"
)

type workflowControllerBuild struct {
	Controller *controller.LinearController
	Dispatcher interface{ Close() }
	Admission  controller.Admission
	Cleanup    func()
}

func buildWorkflowController(root string, res *config.Resolved, store *storage.SQLite, repo workflowledger.Repository, wf *compiler.CompiledWorkflow, refBase string, inputs map[string]any, inputSnapshot map[string]string, definitionTOML []byte, runID string, prior *workflowledger.Snapshot, recorded *workflowledger.RunSnapshot) (workflowControllerBuild, error) {
	setup, err := prepareWorkflowBuild(root, res, wf, runID, recorded)
	if err != nil {
		return workflowControllerBuild{}, err
	}
	dispatcher, opts, legacy, err := newWorkflowDispatcher(res, store, setup)
	if err != nil {
		setup.cleanup()
		return workflowControllerBuild{}, err
	}
	runtime, baseline, err := prepareWorkflowBuildRuntime(root, refBase, wf, inputSnapshot, definitionTOML, prior, setup, opts)
	if err != nil {
		dispatcher.Close()
		setup.cleanup()
		return workflowControllerBuild{}, err
	}
	ctrl, err := newWorkflowController(repo, dispatcher, legacy, res, wf, inputs, runID, runtime, setup.identity, baseline)
	if err != nil {
		dispatcher.Close()
		setup.cleanup()
		return workflowControllerBuild{}, err
	}
	return workflowControllerBuild{Controller: ctrl, Dispatcher: dispatcher, Admission: workflowAdmission(setup, inputSnapshot, recorded), Cleanup: setup.cleanup}, nil
}

type workflowBuildSetup struct {
	skills    *skills.Registry
	loaded    agentLoadResult
	authority *tools.Registry
	identity  workflowspace.Identity
	cleanup   func()
	remoteURL string
}

func prepareWorkflowBuild(root string, res *config.Resolved, wf *compiler.CompiledWorkflow, runID string, recorded *workflowledger.RunSnapshot) (workflowBuildSetup, error) {
	skills, err := workflowBuildLoadSkills(root)
	if err != nil {
		return workflowBuildSetup{}, err
	}
	loaded, err := workflowBuildLoadAgents(root, "", skills)
	if err != nil {
		return workflowBuildSetup{}, err
	}
	if err := compiler.ValidateAgentSkillReferences(wf, loaded.Registry, skills); err != nil {
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
	closeMCP, err := addMCPTools(authority, res, workflowMCPServers(wf, loaded.Registry))
	if err != nil {
		cleanup()
		return workflowBuildSetup{}, err
	}
	previousCleanup := cleanup
	cleanup = func() {
		closeMCP()
		previousCleanup()
	}
	remoteURL, err := workflowBuildRemoteURL(wf, identity, writeCapable, recorded)
	if err != nil {
		cleanup()
		return workflowBuildSetup{}, err
	}
	return workflowBuildSetup{skills: skills, loaded: loaded, authority: authority, identity: identity, cleanup: cleanup, remoteURL: remoteURL}, nil
}

func workflowBuildRemoteURL(wf *compiler.CompiledWorkflow, identity workflowspace.Identity, writeCapable bool, recorded *workflowledger.RunSnapshot) (string, error) {
	if recorded != nil || !deliveryRequiresPublication(wf) {
		return "", nil
	}
	return workflowDeliveryAdmission(wf, identity, writeCapable)
}

func newWorkflowDispatcher(res *config.Resolved, store *storage.SQLite, setup workflowBuildSetup) (*runtime.Dispatcher, SessionDispatcherOpts, ledger.LedgerRepository, error) {
	comp, err := workflowBuildProvider(res)
	if err != nil {
		return nil, SessionDispatcherOpts{}, nil, err
	}
	legacy := ledger.NewStorageLedgerRepository(store)
	opts := SessionDispatcherOpts{Registry: setup.authority, AuthorityRegistry: setup.authority, Completer: comp, Model: res.Model, ProviderName: res.ProviderName, AllowWorkspaceAgentProviders: setup.loaded.Global.AllowWorkspaceAgentProviders, ModelCatalog: res.ModelCatalog(), CompleterFactory: newProviderCompleterFactory(res), Config: res.Subagents, MCP: res.MCP, Repo: legacy, SharedSQLite: store, SkillReg: setup.skills, WorkflowSkillSnapshots: make(map[string]workflowledger.RefSnapshot), AgentRegistry: setup.loaded.Registry, WorkspaceRoot: setup.identity.Root}
	dispatcher, err := workflowBuildDispatcher(opts)
	if err != nil {
		return nil, SessionDispatcherOpts{}, nil, err
	}
	return dispatcher, opts, legacy, nil
}

func prepareWorkflowBuildRuntime(root, refBase string, wf *compiler.CompiledWorkflow, inputs map[string]string, definition []byte, prior *workflowledger.Snapshot, setup workflowBuildSetup, opts SessionDispatcherOpts) (preparedWorkflowRuntime, *verifier.GoModuleBaseline, error) {
	runtime, err := prepareWorkflowRuntime(root, refBase, wf, setup.loaded.Registry, prior, definition, inputs, opts)
	if err != nil {
		return preparedWorkflowRuntime{}, nil, err
	}
	if err := verifyWorkflowSkillSnapshot(wf, setup.skills, prior); err != nil {
		return preparedWorkflowRuntime{}, nil, err
	}
	if prior == nil {
		runtime.Snapshot, err = pinWorkflowSkills(runtime.Snapshot, wf, setup.skills)
		if err != nil {
			return preparedWorkflowRuntime{}, nil, err
		}
	}
	if err := installWorkflowSkillSnapshots(opts.WorkflowSkillSnapshots, runtime.Snapshot); err != nil {
		return preparedWorkflowRuntime{}, nil, err
	}
	baseline, err := workflowModuleBaseline(wf, setup.identity.Root, prior)
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

func newWorkflowController(repo workflowledger.Repository, dispatcher *runtime.Dispatcher, legacy ledger.LedgerRepository, res *config.Resolved, wf *compiler.CompiledWorkflow, inputs map[string]any, runID string, runtime preparedWorkflowRuntime, identity workflowspace.Identity, baseline *verifier.GoModuleBaseline) (*controller.LinearController, error) {
	applyHarnessSandboxSetting(res.Harness)
	coord := initCoordinator(dispatcher, res.Subagents, legacy)
	runner := controller.NewCoordinatorRunner(coord)
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
	if err := ctrl.SetVerifiers(verifier.DefaultCatalogue(policy)); err != nil {
		return nil, err
	}
	if err := ctrl.SetSecretPolicy(policy); err != nil {
		return nil, err
	}
	if err := ctrl.SetWritePathBlocklist(effectiveWorkflowWriteDenylist(res)); err != nil {
		return nil, err
	}
	if err := ctrl.SetWorkDir(identity.Root); err != nil {
		return nil, err
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
	verifier.SetSandboxEnabled(enabled)
	if !enabled {
		warnSandboxDisabledOnce.Do(func() {
			fmt.Fprintln(os.Stderr, "warning: [harness] sandbox = false — evidence-gate commands run directly on the host with no filesystem, network, or environment isolation")
		})
	}
}

func workflowAdmission(setup workflowBuildSetup, inputs map[string]string, recorded *workflowledger.RunSnapshot) controller.Admission {
	admission := controller.Admission{BaseRef: setup.identity.BaseRef, BaseCommit: setup.identity.BaseCommit, OriginBaseCommit: setup.identity.OriginBaseCommit, WorktreeName: setup.identity.WorktreeName, InputDigest: workflowledger.InputDigest(inputs), RemoteURL: setup.remoteURL}
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
// origin remote, and the delivery base must sit at the admitted base commit.
// It returns the resolved origin URL for the immutable admission record.
func workflowDeliveryAdmission(wf *compiler.CompiledWorkflow, identity workflowspace.Identity, writeCapable bool) (string, error) {
	if !writeCapable {
		return "", fmt.Errorf("workflow %s declares delivery but its agents cannot write files; delivery requires a run worktree", wf.Name)
	}
	policy, ok := delivery.FromCompiled(wf)
	if !ok {
		return "", fmt.Errorf("workflow %s delivery policy is not active", wf.Name)
	}
	if err := workflowDeliveryProbe(policy.Provider); err != nil {
		return "", err
	}
	gitCtx := delivery.GitContext{Dir: identity.MainRoot, GitDir: filepath.Join(identity.MainRoot, ".git")}
	originURL, err := workflowDeliverGit.Run(context.Background(), gitCtx, "remote", "get-url", "origin")
	if err != nil {
		return "", fmt.Errorf("workflow requires delivery but the repository has no origin remote")
	}
	baseCommit, err := workflowDeliverGit.Run(context.Background(), gitCtx, "rev-parse", "--verify", "--end-of-options", "refs/heads/"+policy.Base+"^{commit}")
	if err != nil || strings.TrimSpace(baseCommit) != identity.BaseCommit {
		return "", fmt.Errorf("delivery base %q is not at the admitted base commit", policy.Base)
	}
	return strings.TrimSpace(originURL), nil
}

// deliveryRequiresPublication reports whether the workflow's delivery policy
// must publish a pull request when the run settles at its success terminal.
func deliveryRequiresPublication(wf *compiler.CompiledWorkflow) bool {
	return wf.DeliveryActive()
}
