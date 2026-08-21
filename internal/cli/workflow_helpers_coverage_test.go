package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func TestWorkflowAuthorityHelpers(t *testing.T) {
	if _, err := workflowDefaultRegistry(filepath.Join(t.TempDir(), "missing"), &config.Resolved{}); err == nil {
		t.Fatal("workflowDefaultRegistry() error = nil for a missing workspace")
	}
	root := t.TempDir()
	authority, err := workflowDefaultRegistry(root, &config.Resolved{})
	if err != nil {
		t.Fatalf("workflowDefaultRegistry() error = %v", err)
	}
	registry := agents.NewRegistry()
	wf := &definition.CompiledWorkflow{Steps: []definition.Step{{ID: "one", Kind: "agent", Agent: "missing"}}}
	if _, err := workflowWriteAuthority(wf, registry, authority, nil); err == nil {
		t.Fatal("workflowWriteAuthority() error = nil for an unknown agent")
	}
	if err := registry.Publish(agents.ResolvedAgent{Name: "reader", EffectiveTools: []string{"read_file"}}); err != nil {
		t.Fatal(err)
	}
	wf.Steps = []definition.Step{
		{ID: "one", Kind: "agent", Agent: "reader"},
		{ID: "two", Kind: "agent_gate", Agent: "reader"},
		{ID: "three", Kind: "evidence_gate", Verifier: "go-test"},
	}
	if got, err := workflowWriteAuthority(wf, registry, authority, nil); err != nil || got {
		t.Fatalf("workflowWriteAuthority() = %v, %v; want false, nil", got, err)
	}
	if err := registry.Publish(agents.ResolvedAgent{Name: "writer", EffectiveTools: []string{"write_file"}}); err != nil {
		t.Fatal(err)
	}
	wf.Steps = []definition.Step{{ID: "write", Kind: "agent", Agent: "writer"}}
	if got, err := workflowWriteAuthority(wf, registry, authority, nil); err != nil || !got {
		t.Fatalf("workflowWriteAuthority() = %v, %v; want true, nil", got, err)
	}
}

func TestValidateWorkflowResumeSnapshotRejectsInvalidData(t *testing.T) {
	if _, _, _, err := validateWorkflowResumeSnapshot(workflowledger.RunSnapshot{}, nil); err == nil {
		t.Fatal("validateWorkflowResumeSnapshot() accepted a missing digest")
	}
	broken := []byte("{")
	run := workflowledger.RunSnapshot{SnapshotDigest: workflowledger.SnapshotDigest(broken)}
	if _, _, _, err := validateWorkflowResumeSnapshot(run, broken); err == nil {
		t.Fatal("validateWorkflowResumeSnapshot() accepted invalid JSON")
	}
	snapshot := workflowledger.Snapshot{SchemaVersion: workflowledger.SnapshotSchemaVersion}
	raw, err := workflowledger.MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	run = workflowledger.RunSnapshot{
		SnapshotDigest: workflowledger.SnapshotDigest(raw),
		InputDigest:    workflowledger.InputDigest(nil),
	}
	if _, _, _, err := validateWorkflowResumeSnapshot(run, raw); err == nil {
		t.Fatal("validateWorkflowResumeSnapshot() accepted an incomplete snapshot")
	}
	snapshot = workflowledger.Snapshot{
		SchemaVersion:  workflowledger.SnapshotSchemaVersion,
		DefinitionTOML: []byte("bad = ["), DefinitionDigest: "digest",
		Inputs: map[string]string{},
	}
	raw, err = workflowledger.MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	run = workflowledger.RunSnapshot{
		WorkflowName: "bad", SnapshotDigest: workflowledger.SnapshotDigest(raw),
		InputDigest: workflowledger.InputDigest(snapshot.Inputs),
	}
	if _, _, _, err := validateWorkflowResumeSnapshot(run, raw); err == nil {
		t.Fatal("validateWorkflowResumeSnapshot() accepted invalid TOML")
	}
}

// newPreviouslyAdmittedUnboundedSnapshot builds a resume snapshot for a
// workflow that plain Compile now rejects (uncapped cycle, no global limits).
func newPreviouslyAdmittedUnboundedSnapshot(t *testing.T, root string) (workflowledger.RunSnapshot, []byte) {
	t.Helper()
	storePath := filepath.Join(root, "workflow.db")
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	writeWorkflowRunFixture(t, root, "http://127.0.0.1:1", storePath)
	rawDefinition := []byte(`version = 1
name = "two-step"
initial_step = "one"
[inputs.task]
type = "string"
required = true
max_bytes = 100
[[steps]]
id = "one"
kind = "agent"
agent = "one"
template = "templates/one.md"
output_schema = "schemas/out.json"
context = [{ from = "inputs.task", as = "task", max_bytes = 100 }]
[[steps]]
id = "two"
kind = "agent"
agent = "two"
template = "templates/two.md"
output_schema = "schemas/out.json"
context = [{ from = "steps.one.output", as = "previous", max_bytes = 100 }]
[[transitions]]
from = "one"
to = "two"
[transitions.match]
status = "succeeded"
[[transitions]]
from = "two"
to = "one"
[transitions.match]
status = "succeeded"
[[transitions]]
from = "two"
to = "success"
[transitions.match]
status = "succeeded"
output = { ok = "true" }
`)
	if err := os.WriteFile(filepath.Join(root, ".mivia", "workflows", "two-step.toml"), rawDefinition, 0o600); err != nil {
		t.Fatal(err)
	}
	wf, _, err := definition.ParseWorkflowTOML(rawDefinition, "two-step.toml")
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := definition.CompileForResume(&wf)
	if err != nil {
		t.Fatalf("CompileForResume: %v", err)
	}
	snapshot := newForcedResumeSnapshot(t, root, compiled, rawDefinition)
	rawSnapshot, err := workflowledger.MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	run := workflowledger.RunSnapshot{
		RunID: "wfr-resume-unbounded", WorkflowName: compiled.Name, WorkflowDigest: compiled.Digest,
		SnapshotDigest: workflowledger.SnapshotDigest(rawSnapshot),
		InputDigest:    workflowledger.InputDigest(snapshot.Inputs),
		Status:         workflowledger.RunStatusPending, ActiveStepID: compiled.InitialStep,
	}
	return run, rawSnapshot
}

func TestValidateWorkflowResumeSnapshotAcceptsPreviouslyAdmittedUnboundedLoop(t *testing.T) {
	// A run admitted before the unbounded-cycle policy must still resume.
	// Plain Compile now rejects its definition; resume must not.
	root := t.TempDir()
	run, rawSnapshot := newPreviouslyAdmittedUnboundedSnapshot(t, root)
	if _, _, _, err := validateWorkflowResumeSnapshot(run, rawSnapshot); err != nil {
		t.Fatalf("resume validation rejected an admitted run: %v", err)
	}
}

func TestValidateWorkflowSnapshotReferences(t *testing.T) {
	wf := &definition.CompiledWorkflow{Steps: []definition.Step{{ID: "one", Agent: "agent", Template: "prompt.txt"}}}
	snapshot := workflowledger.Snapshot{Schemas: map[string]workflowledger.RefSnapshot{
		"bad.json": {Digest: "bad", Bytes: []byte("{}")},
	}}
	if err := validateWorkflowSnapshotReferences(wf, snapshot); err == nil || !strings.Contains(err.Error(), "schema") {
		t.Fatalf("schema error = %v", err)
	}
	snapshot.Schemas = nil
	if err := validateWorkflowSnapshotReferences(wf, snapshot); err == nil || !strings.Contains(err.Error(), "agent") {
		t.Fatalf("agent error = %v", err)
	}
	snapshot.Agents = map[string]workflowledger.AgentSnapshot{"agent": {Digest: "agent-digest"}}
	if err := validateWorkflowSnapshotReferences(wf, snapshot); err == nil || !strings.Contains(err.Error(), "template") {
		t.Fatalf("template error = %v", err)
	}
	templateBytes := []byte("prompt")
	snapshot.Templates = map[string]workflowledger.RefSnapshot{
		"prompt.txt": {Digest: digestBytes(templateBytes), Bytes: templateBytes},
	}
	if err := validateWorkflowSnapshotReferences(wf, snapshot); err != nil {
		t.Fatalf("validateWorkflowSnapshotReferences() error = %v", err)
	}
}

func TestReconcileWorkflowTerminalStates(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	if _, err := reconcileWorkflowTerminal(ctx, repo, "wfr-missing", false, &bytes.Buffer{}); err == nil {
		t.Fatal("reconcileWorkflowTerminal() error = nil for a missing run")
	}
	run := workflowledger.RunSnapshot{RunID: "wfr-pending", Status: workflowledger.RunStatusPending, ActiveStepID: "one"}
	if err := repo.CreateRun(ctx, run, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	terminal, err := reconcileWorkflowTerminal(ctx, repo, run.RunID, false, &bytes.Buffer{})
	if err != nil || terminal {
		t.Fatalf("reconcileWorkflowTerminal() = %v, %v; want false, nil", terminal, err)
	}
}

func TestWorkflowExecutionLockIdentityAndHookFailure(t *testing.T) {
	root := t.TempDir()
	realStore := filepath.Join(root, "store.db")
	if err := os.WriteFile(realStore, []byte("store"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias.db")
	if err := os.Symlink(realStore, alias); err != nil {
		t.Fatal(err)
	}
	_, realIdentity, err := workflowStoreLockIdentity(realStore)
	if err != nil {
		t.Fatal(err)
	}
	_, aliasIdentity, err := workflowStoreLockIdentity(alias)
	if err != nil {
		t.Fatal(err)
	}
	if aliasIdentity != realIdentity {
		t.Fatalf("alias identity = %q, want %q", aliasIdentity, realIdentity)
	}
	if _, _, err := workflowStoreLockIdentity(filepath.Join(root, "missing", "store.db")); err == nil {
		t.Fatal("workflowStoreLockIdentity() error = nil for a missing parent")
	}
	finish, err := beginWorkflowExecution(filepath.Join(root, "not-a-workspace"), realStore, "wfr-hook")
	if err != nil {
		t.Fatalf("beginWorkflowExecution() error = %v", err)
	}
	finish()
	release, err := acquireWorkflowExecutionLock(realStore, "wfr-hook")
	if err != nil {
		t.Fatalf("lock remained held after workflow finish: %v", err)
	}
	release()
}

func TestWorkflowCommandAndConfigHelpers(t *testing.T) {
	for _, args := range [][]string{nil, {"run"}, {"resume"}, {"other"}} {
		if err := runWorkflowWithIO(args, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Fatalf("runWorkflowWithIO(%v) error = nil", args)
		}
	}
	if err := runWorkflow(nil); err == nil {
		t.Fatal("runWorkflow() error = nil")
	}
	root := t.TempDir()
	if got := workflowConfigPath(root, "explicit.toml"); got != "explicit.toml" {
		t.Fatalf("workflowConfigPath() = %q", got)
	}
	if got := workflowConfigPath(root, ""); got != "" {
		t.Fatalf("workflowConfigPath() = %q for a missing file", got)
	}
	path := filepath.Join(root, ".mivia", "mivia.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := workflowConfigPath(root, ""); got != path {
		t.Fatalf("workflowConfigPath() = %q, want %q", got, path)
	}
	applyWorkflowStoreRoot(nil, root)
	resolved := &config.Resolved{Subagents: config.SubagentConfig{StoreBackend: "sqlite"}}
	applyWorkflowStoreRoot(resolved, root)
	if resolved.Subagents.StorePath != workspace.ContextStorePath(root) {
		t.Fatalf("store path = %q", resolved.Subagents.StorePath)
	}
	resolved.StorePathSet = true
	resolved.Subagents.StorePath = "kept.db"
	applyWorkflowStoreRoot(resolved, root)
	if resolved.Subagents.StorePath != "kept.db" {
		t.Fatalf("explicit store path = %q", resolved.Subagents.StorePath)
	}
}

func TestParseWorkflowInputsRejectsInvalidValues(t *testing.T) {
	defs := map[string]definition.InputDef{
		"count": {Type: "integer", Required: true, MaxBytes: 2},
	}
	for _, raw := range [][]string{{"bad"}, {"other=1"}, {"count=123"}, {"count=true"}, nil} {
		if _, _, err := parseWorkflowInputs(raw, defs); err == nil {
			t.Fatalf("parseWorkflowInputs(%v) error = nil", raw)
		}
	}
	values, snapshot, err := parseWorkflowInputs([]string{"count=12"}, defs)
	if err != nil {
		t.Fatal(err)
	}
	if values["count"] == nil || snapshot["count"] != "12" {
		t.Fatalf("values = %v, snapshot = %v", values, snapshot)
	}
}

func TestLoadWorkflowReferencesRejectsInvalidFiles(t *testing.T) {
	step := definition.Step{Template: "prompt.txt", OutputSchema: "schema.json"}
	prior := &workflowledger.Snapshot{Templates: map[string]workflowledger.RefSnapshot{
		"prompt.txt": {Digest: "bad", Bytes: []byte("prompt")},
	}}
	if _, _, _, _, err := loadStepReferences("", step, prior); err == nil {
		t.Fatal("loadStepReferences() accepted a bad template digest")
	}
	templateBytes := []byte("prompt")
	prior.Templates["prompt.txt"] = workflowledger.RefSnapshot{Digest: digestBytes(templateBytes), Bytes: templateBytes}
	prior.Schemas = map[string]workflowledger.RefSnapshot{
		"schema.json": {Digest: "bad", Bytes: []byte("{}")},
	}
	if _, _, _, _, err := loadStepReferences("", step, prior); err == nil {
		t.Fatal("loadStepReferences() accepted a bad schema digest")
	}
	badSchema := []byte("{")
	prior.Schemas["schema.json"] = workflowledger.RefSnapshot{Digest: digestBytes(badSchema), Bytes: badSchema}
	if _, _, _, _, err := loadStepReferences("", step, prior); err == nil {
		t.Fatal("loadStepReferences() accepted invalid schema JSON")
	}
	root := t.TempDir()
	if _, err := readWorkflowRef(root, "../escape", 10); err == nil {
		t.Fatal("readWorkflowRef() accepted an escaping path")
	}
	large := filepath.Join(root, "large.txt")
	if err := os.WriteFile(large, []byte("large"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readWorkflowRef(root, "large.txt", 4); err == nil {
		t.Fatal("readWorkflowRef() accepted an oversized file")
	}
}

func TestWorkflowRuntimeAndWorkspaceErrors(t *testing.T) {
	wf := &definition.CompiledWorkflow{Steps: []definition.Step{{ID: "gate", Kind: "unsupported"}}}
	if _, _, err := loadWorkflowRuntimes(t.TempDir(), "", wf, agents.NewRegistry(), nil); err == nil {
		t.Fatal("loadWorkflowRuntimes() accepted an unsupported step")
	}
	wf.Steps[0] = definition.Step{ID: "one", Kind: "agent", Agent: "missing"}
	if _, _, err := loadWorkflowRuntimes(t.TempDir(), "", wf, agents.NewRegistry(), nil); err == nil {
		t.Fatal("loadWorkflowRuntimes() accepted an unknown agent")
	}
	if _, err := prepareWorkflowRuntime(t.TempDir(), "", wf, agents.NewRegistry(), nil, nil, nil, nil, SessionDispatcherOpts{}); err == nil {
		t.Fatal("prepareWorkflowRuntime() accepted an unknown agent")
	}
	root := t.TempDir()
	identity, cleanup, err := selectWorkflowWorkspace(context.Background(), root, "wfr-read", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if identity.Root == "" {
		t.Fatal("read-only workspace root is empty")
	}
	recorded := &workflowledger.RunSnapshot{RunID: "wfr-write"}
	if _, _, err := selectWorkflowWorkspace(context.Background(), root, recorded.RunID, true, recorded); err == nil {
		t.Fatal("selectWorkflowWorkspace() accepted a write run without a worktree")
	}
	if _, _, err := selectWorkflowWorkspace(context.Background(), root, strings.Repeat("x", 300), true, nil); err == nil {
		t.Fatal("selectWorkflowWorkspace() accepted an invalid run ID")
	}
}

func TestLoadWorkflowRuntimesAcceptsAgentAndHostSteps(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, ".mivia", "workflows")
	if err := os.MkdirAll(filepath.Join(base, "templates"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "schemas"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "templates", "agent.md"), []byte("agent"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "schemas", "result.json"), []byte(`{"type":"object"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := agents.NewRegistry()
	if err := registry.Publish(agents.ResolvedAgent{Name: "worker", EffectiveTools: []string{"read_file"}}); err != nil {
		t.Fatal(err)
	}
	wf := &definition.CompiledWorkflow{
		Digest: "digest",
		Steps: []definition.Step{
			{ID: "agent", Kind: "agent", Agent: "worker", Template: "templates/agent.md", OutputSchema: "schemas/result.json"},
			{ID: "review", Kind: "agent_gate", Agent: "worker", Template: "templates/agent.md", OutputSchema: "schemas/result.json"},
			{ID: "verify", Kind: "evidence_gate", Verifier: "go-test", OutputSchema: "schemas/result.json"},
			{ID: "approval", Kind: "human_gate"},
		},
	}
	runtimes, snapshot, err := loadWorkflowRuntimes(root, base, wf, registry, nil)
	if err != nil {
		t.Fatalf("loadWorkflowRuntimes() error = %v", err)
	}
	if len(runtimes) != 2 || runtimes["agent"].Agent.Name != "worker" || runtimes["review"].Agent.Name != "worker" {
		t.Fatalf("runtimes = %+v; want agent runtimes only", runtimes)
	}
	if _, ok := snapshot.Schemas["schemas/result.json"]; !ok {
		t.Fatal("snapshot does not retain the evidence-gate schema")
	}
}
