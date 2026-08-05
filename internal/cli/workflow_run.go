package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/template"
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
		return fmt.Errorf("workflow: expected run or resume")
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
	switch args[0] {
	case "run":
		inputs, rest, _ := flagVar(args[1:], "--input")
		if len(rest) != 1 {
			return fmt.Errorf("workflow run: expected one workflow name")
		}
		return executeWorkflowRun(rest[0], workspaceRoot, configPath, inputs, stdout, stderr)
	case "resume":
		if len(args) != 2 {
			return fmt.Errorf("workflow resume: expected one run ID")
		}
		return executeWorkflowResume(args[1], workspaceRoot, configPath, force, stdout, stderr)
	default:
		return fmt.Errorf("workflow: unknown subcommand %q", args[0])
	}
}

func executeWorkflowRun(name, root, configPath string, rawInputs []string, stdout, stderr io.Writer) error {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	work, err := workspace.Open(root)
	if err != nil {
		return err
	}
	configPath = workflowConfigPath(work.Abs, configPath)
	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, AllowMissingConfig: true})
	if err != nil {
		return err
	}
	applyPrivacyPolicy(res)
	applyWorkflowStoreRoot(res, work.Abs)
	store, repo, closeFn, err := openWorkflowStore(work.Abs, res.Subagents)
	if err != nil {
		return err
	}
	defer closeFn()
	workflows, err := definition.DiscoverWorkflows(work.Abs)
	if err != nil {
		return err
	}
	var found *definition.DiscoveredWorkflow
	for i := range workflows {
		if workflows[i].Name == name {
			found = &workflows[i]
			break
		}
	}
	if found == nil {
		return fmt.Errorf("workflow %q was not found", name)
	}
	wf, _, err := definition.ParseWorkflowTOML(found.Raw, found.Name+".toml")
	if err != nil {
		return err
	}
	compiled, err := compiler.Compile(&wf)
	if err != nil {
		return err
	}
	inputs, inputSnapshot, err := parseWorkflowInputs(rawInputs, compiled.Inputs)
	if err != nil {
		return err
	}
	runID := newCLIWorkflowRunID()
	finishExecution, err := beginWorkflowExecution(work.Abs, contextStorePath(work.Abs, res.Subagents), runID)
	if err != nil {
		return err
	}
	defer finishExecution()
	built, err := workflowRunBuild(work.Abs, res, store, repo, compiled, filepath.Dir(found.Path), inputs, inputSnapshot, found.Raw, runID, nil, nil)
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
		InputDigest: workflowledger.InputDigest(inputSnapshot),
	}
	if recorded != nil {
		admission.InputDigest = recorded.InputDigest
		admission.DeadlineAt = recorded.DeadlineAt
	}
	return workflowControllerBuild{Controller: ctrl, Dispatcher: dispatcher, Admission: admission, Cleanup: cleanup}, nil
}

func parseWorkflowInputs(raw []string, defs map[string]definition.InputDef) (map[string]any, map[string]string, error) {
	values := make(map[string]any)
	snapshot := make(map[string]string)
	for _, item := range raw {
		key, value, ok := strings.Cut(item, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, nil, fmt.Errorf("workflow input must use name=value")
		}
		def, exists := defs[key]
		if !exists {
			return nil, nil, fmt.Errorf("unknown workflow input %q", key)
		}
		if def.MaxBytes > 0 && len(value) > def.MaxBytes {
			return nil, nil, fmt.Errorf("workflow input %q exceeds %d bytes", key, def.MaxBytes)
		}
		parsed, err := parseWorkflowInputValue(value, def.Type)
		if err != nil {
			return nil, nil, fmt.Errorf("workflow input %q: %w", key, err)
		}
		values[key] = parsed
		snapshot[key] = value
	}
	for key, def := range defs {
		if def.Required {
			if _, ok := values[key]; !ok {
				return nil, nil, fmt.Errorf("required workflow input %q is missing", key)
			}
		}
	}
	return values, snapshot, nil
}

func parseWorkflowInputValue(value, typ string) (any, error) {
	if typ == "string" {
		return value, nil
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(value)))
	decoder.UseNumber()
	var parsed any
	if err := decoder.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("value is not valid %s JSON", typ)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("value contains more than one JSON value")
	}
	valid := false
	switch typ {
	case "boolean":
		_, valid = parsed.(bool)
	case "integer":
		number, ok := parsed.(json.Number)
		valid = ok && !strings.ContainsAny(number.String(), ".eE")
	case "number":
		_, valid = parsed.(json.Number)
	case "object":
		_, valid = parsed.(map[string]any)
	case "array":
		_, valid = parsed.([]any)
	default:
		return nil, fmt.Errorf("unsupported input type %q", typ)
	}
	if !valid {
		return nil, fmt.Errorf("value does not match type %q", typ)
	}
	return parsed, nil
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

func cloneStringMap(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func readWorkflowRef(base, ref string, max int) ([]byte, error) {
	clean := filepath.Clean(ref)
	if clean == "." || filepath.IsAbs(ref) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return nil, fmt.Errorf("workflow reference %q escapes its directory", ref)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat(clean)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("workflow reference %q is not a regular file", ref)
	}
	file, err := root.Open(clean)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(max)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > max {
		return nil, fmt.Errorf("workflow reference %q exceeds %d bytes", ref, max)
	}
	return data, nil
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
