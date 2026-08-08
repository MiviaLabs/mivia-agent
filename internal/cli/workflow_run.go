package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/secretpath"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/verifier"
	workflowspace "github.com/MiviaLabs/mivia-agent/internal/workflows/workspace"
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
	return runWorkflowWithIO(args, os.Stdout, os.Stderr)
}

func runWorkflowWithIO(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("workflow: expected run, runs, resume, deliver, status, events, approve, reject, cancel, cleanup, or delete")
	}
	var workspaceRoot, configPath string
	var found bool
	workspaceRoot, args, found = flagValue(args, "--workspace")
	_ = found
	configPath, args, _ = flagValue(args, "--config")
	force := false
	filtered := args[:0]
	for _, arg := range args {
		if arg == "--force" {
			force = true
			continue
		}
		filtered = append(filtered, arg)
	}
	args = filtered
	if len(args) == 0 {
		return fmt.Errorf("workflow: expected run, runs, resume, deliver, status, events, approve, reject, cancel, cleanup, or delete")
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
		return runWorkflowCommandDelete(args[1:], workspaceRoot, configPath, stdout, stderr)
	default:
		return fmt.Errorf("workflow: unknown subcommand %q", args[0])
	}
}

func executeWorkflowRun(name, root, configPath string, rawInputs []string, allowPublish bool, stdout, stderr io.Writer) error {
	prepared, err := prepareWorkflowRun(name, root, configPath, rawInputs)
	if err != nil {
		return err
	}
	defer prepared.closeFn()
	runID := newCLIWorkflowRunID()
	finishExecution, err := beginWorkflowExecution(prepared.root, contextStorePath(prepared.root, prepared.res.Subagents), runID)
	if err != nil {
		return err
	}
	defer finishExecution()
	built, err := workflowRunBuild(prepared.root, prepared.res, prepared.store, prepared.repo, prepared.compiled, prepared.refBase, prepared.inputs, prepared.inputSnapshot, prepared.raw, runID, nil, nil)
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
	if snap.Status == workflowledger.RunStatusDeliveryPending {
		mode := ""
		if prepared.compiled.Delivery != nil {
			mode = prepared.compiled.Delivery.Mode
		}
		return finishWorkflowRunDelivery(context.Background(), prepared.root, prepared.res, prepared.store, prepared.repo, built.Controller.RunID, name, mode, allowPublish, stdout, stderr)
	}
	return nil
}

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
	inputs, inputSnapshot, err := parseWorkflowInputs(rawInputs, compiled.Inputs)
	if err != nil {
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
// without it prints the non-publication explanation.
func finishWorkflowRunDelivery(ctx context.Context, root string, res *config.Resolved, store *storage.SQLite, repo workflowledger.Repository, runID, workflowName, mode string, allowPublish bool, stdout, stderr io.Writer) error {
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
		return nil
	}
	fmt.Fprintf(stdout, "workflow %s reached its success terminal; delivery mode=%s requires --allow-publish\n", workflowName, mode)
	fmt.Fprintf(stdout, "deliver with: mivia workflow deliver %s --allow-publish\n", runID)
	return nil
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

func applyWorkflowStoreRoot(res *config.Resolved, root string) {
	if res != nil && res.Subagents.StoreBackend == "sqlite" && !res.StorePathSet {
		res.Subagents.StorePath = workspace.ContextStorePath(root)
	}
}

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
	coord := initCoordinator(dispatcher, res.Subagents, legacy)
	ctrl, err := workflowBuildController(repo, controller.NewCoordinatorRunner(coord), wf, runtime.Steps, inputs, runID, runtime.Snapshot)
	if err != nil {
		return nil, err
	}
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
	if err := ctrl.SetWorkDir(identity.Root); err != nil {
		return nil, err
	}
	if baseline != nil {
		if err := ctrl.SetModuleBaseline(baseline); err != nil {
			return nil, err
		}
	}
	return ctrl, nil
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
