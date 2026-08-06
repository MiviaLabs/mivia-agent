package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/template"
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
		return fmt.Errorf("workflow: expected run, resume, deliver, status, events, approve, reject, cancel, or cleanup")
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
		return fmt.Errorf("workflow: expected run, resume, deliver, status, events, approve, reject, cancel, or cleanup")
	}
	switch args[0] {
	case "run":
		return runWorkflowCommandRun(args[1:], workspaceRoot, configPath, stdout, stderr)
	case "deliver":
		return runWorkflowCommandDeliver(args[1:], workspaceRoot, configPath, stdout, stderr)
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
	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, AllowMissingConfig: true})
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
		if err := deliverRunWithStore(ctx, root, res, store, repo, runID, allowPublish, stdout, stderr); err != nil {
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
	candidate := filepath.Join(root, ".mivia", "mivia.toml")
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
	skills, err := workflowBuildLoadSkills(root)
	if err != nil {
		return workflowControllerBuild{}, err
	}
	loaded, err := workflowBuildLoadAgents(root, "", skills)
	if err != nil {
		return workflowControllerBuild{}, err
	}
	authority, err := workflowBuildRegistry(root, res)
	if err != nil {
		return workflowControllerBuild{}, err
	}
	writeCapable, err := workflowWriteAuthority(wf, loaded.Registry, authority, loaded.Global.MandatoryToolDenylistAdditions)
	if err != nil {
		return workflowControllerBuild{}, err
	}
	identity, cleanup, err := workflowBuildWorkspace(context.Background(), root, runID, writeCapable, recorded)
	if err != nil {
		return workflowControllerBuild{}, err
	}
	authority, err = workflowBuildRegistry(identity.Root, res)
	if err != nil {
		cleanup()
		return workflowControllerBuild{}, err
	}
	admissionRemoteURL := ""
	if recorded == nil && deliveryRequiresPublication(wf) {
		remoteURL, err := workflowDeliveryAdmission(wf, identity, writeCapable)
		if err != nil {
			cleanup()
			return workflowControllerBuild{}, err
		}
		admissionRemoteURL = remoteURL
	}
	comp, err := workflowBuildProvider(res)
	if err != nil {
		cleanup()
		return workflowControllerBuild{}, err
	}
	reg := authority
	legacy := ledger.NewStorageLedgerRepository(store)
	dispatcherOpts := SessionDispatcherOpts{Registry: reg, AuthorityRegistry: reg, Completer: comp, Model: res.Model, ProviderName: res.ProviderName, ModelCatalog: res.ModelCatalog(), CompleterFactory: newProviderCompleterFactory(res), Config: res.Subagents, Repo: legacy, SharedSQLite: store, SkillReg: skills, AgentRegistry: loaded.Registry, WorkspaceRoot: identity.Root}
	dispatcher, err := workflowBuildDispatcher(dispatcherOpts)
	if err != nil {
		cleanup()
		return workflowControllerBuild{}, err
	}
	runtime, err := prepareWorkflowRuntime(root, refBase, wf, loaded.Registry, prior, definitionTOML, inputSnapshot, dispatcherOpts)
	if err != nil {
		dispatcher.Close()
		cleanup()
		return workflowControllerBuild{}, err
	}
	coord := initCoordinator(dispatcher, res.Subagents, legacy)
	ctrl, err := workflowBuildController(repo, controller.NewCoordinatorRunner(coord), wf, runtime.Steps, inputs, runID, runtime.Snapshot)
	if err != nil {
		dispatcher.Close()
		cleanup()
		return workflowControllerBuild{}, err
	}
	admission := controller.Admission{
		BaseRef: identity.BaseRef, BaseCommit: identity.BaseCommit, WorktreeName: identity.WorktreeName,
		InputDigest: workflowledger.InputDigest(inputSnapshot), RemoteURL: admissionRemoteURL,
	}
	if recorded != nil {
		admission.InputDigest = recorded.InputDigest
		admission.DeadlineAt = recorded.DeadlineAt
		admission.RemoteURL = recorded.RemoteURL
	}
	return workflowControllerBuild{Controller: ctrl, Dispatcher: dispatcher, Admission: admission, Cleanup: cleanup}, nil
}

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

// parseWorkflowBoolFlag parses a boolean workflow flag in bare (--name) or
// --name=true|false form, removing it from args. It reports an error for a
// malformed value or a duplicate occurrence. It does not reuse flagValue
// because that helper only handles string-valued flags.
func parseWorkflowBoolFlag(args []string, name string) (bool, []string, error) {
	rest := make([]string, 0, len(args))
	value := false
	found := false
	for _, arg := range args {
		switch {
		case arg == name:
			if found {
				return false, nil, fmt.Errorf("workflow flag %s may only be given once", name)
			}
			value, found = true, true
		case strings.HasPrefix(arg, name+"="):
			if found {
				return false, nil, fmt.Errorf("workflow flag %s may only be given once", name)
			}
			raw := strings.TrimPrefix(arg, name+"=")
			parsed, err := strconv.ParseBool(raw)
			if err != nil {
				return false, nil, fmt.Errorf("workflow flag %s expects true or false, got %q", name, raw)
			}
			value, found = parsed, true
		default:
			rest = append(rest, arg)
		}
	}
	return value, rest, nil
}

// deliveryRequiresPublication reports whether the workflow's delivery policy
// must publish a pull request when the run settles at its success terminal.
func deliveryRequiresPublication(wf *compiler.CompiledWorkflow) bool {
	return wf.DeliveryActive()
}

func loadWorkflowRuntimes(root, base string, wf *compiler.CompiledWorkflow, registry *agents.AgentRegistry, prior *workflowledger.Snapshot) (map[string]controller.StepRuntime, workflowledger.Snapshot, error) {
	if base == "" {
		base = filepath.Join(root, ".mivia", "workflows")
	}
	result := make(map[string]controller.StepRuntime)
	snapshot := workflowledger.Snapshot{SchemaVersion: workflowledger.SnapshotSchemaVersion, DefinitionDigest: wf.Digest, Agents: map[string]workflowledger.AgentSnapshot{}, Schemas: map[string]workflowledger.RefSnapshot{}, Templates: map[string]workflowledger.RefSnapshot{}}
	for _, step := range wf.Steps {
		if step.Kind != "agent" {
			return nil, snapshot, fmt.Errorf("phase 3 supports agent steps only; step %q is %q", step.ID, step.Kind)
		}
		agent, ok := registry.Get(step.Agent)
		if !ok {
			return nil, snapshot, fmt.Errorf("workflow step %q references unknown agent %q", step.ID, step.Agent)
		}
		digest, err := agent.DefinitionDigest()
		if err != nil {
			return nil, snapshot, err
		}
		if prior != nil {
			pinned, ok := prior.Agents[agent.Name]
			if !ok || pinned.Digest != digest {
				return nil, snapshot, fmt.Errorf("agent %q changed since workflow admission", agent.Name)
			}
		}
		tmpl, schema, tmplBytes, schemaBytes, err := loadStepReferences(base, step, prior)
		if err != nil {
			return nil, snapshot, err
		}
		result[step.ID] = controller.StepRuntime{Agent: agent, Digest: digest, Template: tmpl, Schema: schema}
		snapshot.Agents[agent.Name] = workflowledger.AgentSnapshot{Digest: digest}
		if step.Template != "" {
			snapshot.Templates[step.Template] = workflowledger.RefSnapshot{Digest: digestBytes(tmplBytes), Bytes: append([]byte(nil), tmplBytes...)}
		}
		if step.OutputSchema != "" {
			snapshot.Schemas[step.OutputSchema] = workflowledger.RefSnapshot{Digest: digestBytes(schemaBytes), Bytes: append([]byte(nil), schemaBytes...)}
		}
	}
	if prior == nil && wf.DeliveryActive() {
		snapshot.Delivery = &workflowledger.DeliverySnapshot{
			Mode: wf.Delivery.Mode, Provider: wf.Delivery.Provider, Base: wf.Delivery.Base,
		}
	}
	return result, snapshot, nil
}

func loadStepReferences(base string, step definition.Step, prior *workflowledger.Snapshot) (string, map[string]any, []byte, []byte, error) {
	if prior != nil {
		t := prior.Templates[step.Template]
		s := prior.Schemas[step.OutputSchema]
		if step.Template != "" && (t.Digest == "" || digestBytes(t.Bytes) != t.Digest) {
			return "", nil, nil, nil, fmt.Errorf("snapshot template %q digest is invalid", step.Template)
		}
		if step.OutputSchema != "" && (s.Digest == "" || digestBytes(s.Bytes) != s.Digest) {
			return "", nil, nil, nil, fmt.Errorf("snapshot schema %q digest is invalid", step.OutputSchema)
		}
		var schema map[string]any
		if len(s.Bytes) > 0 && json.Unmarshal(s.Bytes, &schema) != nil {
			return "", nil, nil, nil, fmt.Errorf("snapshot schema %q is invalid", step.OutputSchema)
		}
		return string(t.Bytes), schema, t.Bytes, s.Bytes, nil
	}
	var templateBytes []byte
	var err error
	if step.Template != "" {
		templateBytes, err = readWorkflowRef(base, step.Template, template.MaxTemplateBytes)
		if err != nil {
			return "", nil, nil, nil, err
		}
	}
	var schema map[string]any
	if step.OutputSchema != "" {
		data, err := readWorkflowRef(base, step.OutputSchema, definition.MaxWorkflowFileBytes)
		if err != nil {
			return "", nil, nil, nil, err
		}
		if err := json.Unmarshal(data, &schema); err != nil {
			return "", nil, nil, nil, fmt.Errorf("schema %q is invalid: %w", step.OutputSchema, err)
		}
		return string(templateBytes), schema, templateBytes, data, nil
	}
	return string(templateBytes), schema, templateBytes, nil, nil
}
