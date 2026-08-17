package localengine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func runGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestResolveLocalIdentityResolvesRealRepo pins that a git workspace gets its
// real default branch and HEAD commit. Fabricated identities ("main" /
// "local-base") made every delivery attempt refuse with "base commit is not
// an ancestor of HEAD".
func TestResolveLocalIdentityResolvesRealRepo(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "test")
	runGit(t, root, "commit", "-q", "--allow-empty", "-m", "init")
	head := runGit(t, root, "rev-parse", "HEAD")

	baseRef, baseCommit, worktree, err := resolveLocalIdentity(root, "wfr-x")
	if err != nil {
		t.Fatal(err)
	}
	if baseCommit != head {
		t.Fatalf("baseCommit = %q, want %q", baseCommit, head)
	}
	if baseRef != "master" && baseRef != "main" {
		t.Fatalf("baseRef = %q, want the real default branch", baseRef)
	}
	if worktree != "workflow-wfr-x" {
		t.Fatalf("worktree = %q", worktree)
	}
}

// TestResolveLocalIdentityRejectsNonGitRoot pins that a non-git workspace is
// reported as an error instead of silently fabricating an identity.
func TestResolveLocalIdentityRejectsNonGitRoot(t *testing.T) {
	if _, _, _, err := resolveLocalIdentity(t.TempDir(), "wfr-x"); err == nil {
		t.Fatal("expected an error for a non-git root")
	}
}

// TestBuildStepRuntimesPopulatesOutputSchema pins that buildStepRuntimes
// fills StepRuntime.Schema from the compiled workflow's output_schema refs,
// mirroring the CLI's loadStepReferences. A nil Schema makes the controller's
// validateOutput skip jschema entirely (agent_step.go), so without this the
// localengine path accepts output that violates the step's declared schema.
func TestBuildStepRuntimesPopulatesOutputSchema(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, ".mivia", "workflows")
	if err := os.MkdirAll(filepath.Join(base, "schemas"), 0o700); err != nil {
		t.Fatal(err)
	}
	schemaContent := `{"type":"object","required":["ok"],"properties":{"ok":{"type":"boolean"}},"additionalProperties":false}`
	if err := os.WriteFile(filepath.Join(base, "schemas", "out.json"), []byte(schemaContent), 0o600); err != nil {
		t.Fatal(err)
	}

	wf := &definition.WorkflowFile{
		Version:     1,
		Name:        "schema-test",
		InitialStep: "plan",
		Steps: []definition.Step{
			{ID: "plan", Kind: "agent", Agent: "planner", OutputSchema: "schemas/out.json"},
			{ID: "review", Kind: "agent_gate", Agent: "reviewer"},
			{ID: "validate", Kind: "evidence_gate", Verifier: "go-test", OutputSchema: "schemas/out.json"},
		},
		Transitions: []definition.Transition{
			{From: "plan", To: "review", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "review", To: "validate", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "validate", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := buildStepRuntimes(compiled, base)
	if err != nil {
		t.Fatal(err)
	}

	plan, ok := steps["plan"]
	if !ok {
		t.Fatal("agent step runtime is missing")
	}
	if plan.Schema == nil {
		t.Fatal("agent step with output_schema must have a populated Schema")
	}
	required, ok := plan.Schema["required"].([]any)
	if !ok || len(required) != 1 || required[0] != "ok" {
		t.Fatalf("plan Schema = %#v, want the compiled out.json shape", plan.Schema)
	}

	review, ok := steps["review"]
	if !ok {
		t.Fatal("agent_gate step runtime is missing")
	}
	if review.Schema != nil {
		t.Fatal("agent_gate step without output_schema must keep a nil Schema")
	}
	if _, ok := steps["validate"]; ok {
		t.Fatal("evidence_gate step must not produce a step runtime")
	}
}

func TestLoadPanelSnapshotAssetsPinsMemberWork(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "security.md"), []byte("Review {{inputs.task}}."), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "correctness.md"), []byte("Review {{inputs.task}}."), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "report.json"), []byte(`{"type":"object"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	wf := &compiler.CompiledWorkflow{Steps: []definition.Step{{
		ID: "review", Kind: "agent_panel", Panel: &definition.AgentPanel{Members: []definition.PanelMember{
			{ID: "security", Agent: "panel-reviewer", Provider: "deepseek", Model: "deepseek-v4", Skill: "secure-change", Template: "security.md", OutputSchema: "report.json"},
			{ID: "correctness", Agent: "panel-reviewer", Provider: "zai", Model: "glm-5", Skill: "bug-audit", Template: "correctness.md", OutputSchema: "report.json"},
		}},
	}}}
	schemas, err := loadOutputSchemas(base, wf)
	if err != nil {
		t.Fatal(err)
	}
	registry := agents.NewRegistry()
	if err := registry.Publish(agents.ResolvedAgent{Name: "panel-reviewer", Model: "deepseek-v4"}); err != nil {
		t.Fatal(err)
	}
	templates, bindings, err := loadPanelSnapshotAssets(base, wf, schemas, registry)
	if err != nil {
		t.Fatal(err)
	}
	if len(templates) != 2 || len(bindings) != 2 {
		t.Fatalf("assets templates=%d bindings=%d", len(templates), len(bindings))
	}
	binding := bindings["review/security"]
	if binding.AgentName != "panel-reviewer" || binding.ProviderName != "deepseek" || binding.Model != "deepseek-v4" || binding.TemplateDigest != templates["security.md"].Digest || binding.SchemaDigest != schemas["report.json"].Digest {
		t.Fatalf("security binding = %+v", binding)
	}
	snapshot := newRunSnapshot(wf, []byte("raw"), map[string]string{"task": "change"}, schemas, templates, bindings, nil)
	if got := snapshot.PanelBindings["review/correctness"]; got.ProviderName != "zai" || got.Model != "glm-5" {
		t.Fatalf("snapshot correctness binding = %+v", got)
	}
}

func TestLoadPanelSnapshotAssetsRejectsSymlinkTemplate(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "report.json"), []byte(`{"type":"object"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hostname", filepath.Join(base, "template.md")); err != nil {
		t.Fatal(err)
	}
	wf := &compiler.CompiledWorkflow{Steps: []definition.Step{{ID: "review", Kind: "agent_panel", Panel: &definition.AgentPanel{Members: []definition.PanelMember{{ID: "member", Agent: "reviewer", Provider: "deepseek", Model: "deepseek-v4", Skill: "bug-audit", Template: "template.md", OutputSchema: "report.json"}}}}}}
	schemas, err := loadOutputSchemas(base, wf)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadPanelSnapshotAssets(base, wf, schemas, agents.NewRegistry()); err == nil {
		t.Fatal("expected symlink template rejection")
	}
}

func TestLoadTemplateBytesUsesTemplateLimit(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "large.md"), []byte(strings.Repeat("x", 32769)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTemplateBytes(base, "large.md"); err == nil {
		t.Fatal("expected oversized template rejection")
	}
}

// TestBuildStepRuntimesRejectsMissingSchema pins that a step whose
// output_schema ref cannot be read fails admission instead of silently
// skipping schema validation (which would accept invalid agent output).
func TestBuildStepRuntimesRejectsMissingSchema(t *testing.T) {
	base := t.TempDir()
	wf := &definition.WorkflowFile{
		Version:     1,
		Name:        "missing-schema",
		InitialStep: "plan",
		Steps: []definition.Step{
			{ID: "plan", Kind: "agent", Agent: "planner", OutputSchema: "schemas/missing.json"},
		},
		Transitions: []definition.Transition{
			{From: "plan", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := buildStepRuntimes(compiled, base); err == nil {
		t.Fatal("missing output_schema file must fail buildStepRuntimes")
	}
}

// TestBuildStepRuntimesPopulatesTemplate pins that buildStepRuntimes fills
// StepRuntime.Template from the workspace template bytes, mirroring the CLI's
// loadStepReferences. An empty Template renders a prompt without the template
// body (linear_prompt.go), so dropping it silently changes what the agent is
// told to do.
func TestBuildStepRuntimesPopulatesTemplate(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "templates"), 0o700); err != nil {
		t.Fatal(err)
	}
	templateContent := "Plan {{inputs.task}}."
	if err := os.WriteFile(filepath.Join(base, "templates", "plan.md"), []byte(templateContent), 0o600); err != nil {
		t.Fatal(err)
	}
	wf := &definition.WorkflowFile{
		Version:     1,
		Name:        "template-test",
		InitialStep: "plan",
		Steps: []definition.Step{
			{ID: "plan", Kind: "agent", Agent: "planner", Template: "templates/plan.md"},
		},
		Transitions: []definition.Transition{
			{From: "plan", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := buildStepRuntimes(compiled, base)
	if err != nil {
		t.Fatal(err)
	}
	if steps["plan"].Template != templateContent {
		t.Fatalf("plan Template = %q, want the workspace template bytes", steps["plan"].Template)
	}
}

// TestBuildStepRuntimesFromSnapshotTemplatePins pins the resume-path template
// rules: a valid pin hydrates Template, a bad digest fails closed, and an
// absent pin degrades to the admitted template-less dispatch (a run admitted
// before template pinning; the whole-snapshot digest proves the absence is
// admission truth).
func TestBuildStepRuntimesFromSnapshotTemplatePins(t *testing.T) {
	wf := &compiler.CompiledWorkflow{Steps: []definition.Step{
		{ID: "plan", Kind: "agent", Agent: "planner", Template: "templates/plan.md"},
	}}
	content := []byte("Plan {{inputs.task}}.")

	steps, err := buildStepRuntimesFromSnapshot(wf, nil, map[string]workflowledger.RefSnapshot{
		"templates/plan.md": {Digest: digestRefBytes(content), Bytes: content},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if steps["plan"].Template != string(content) {
		t.Fatalf("plan Template = %q, want the pinned bytes", steps["plan"].Template)
	}

	if _, err := buildStepRuntimesFromSnapshot(wf, nil, map[string]workflowledger.RefSnapshot{
		"templates/plan.md": {Digest: "sha256:0000", Bytes: content},
	}, nil); err == nil {
		t.Fatal("an invalid template digest must fail closed")
	}

	steps, err = buildStepRuntimesFromSnapshot(wf, nil, nil, nil)
	if err != nil {
		t.Fatalf("a missing template pin must degrade for legacy runs: %v", err)
	}
	if steps["plan"].Template != "" {
		t.Fatalf("legacy degrade must keep the admitted empty Template, got %q", steps["plan"].Template)
	}
}

// TestBuildStepRuntimesFromSnapshotAgentPins pins the agent-pin rules: with
// pins the runtime carries the admitted digest and provider/model pair instead
// of the synthetic digest; an incomplete pin fails closed; nil pins keep the
// legacy synthetic digest.
func TestBuildStepRuntimesFromSnapshotAgentPins(t *testing.T) {
	wf := &compiler.CompiledWorkflow{Steps: []definition.Step{
		{ID: "plan", Kind: "agent", Agent: "planner"},
	}}
	pins := map[string]workflowledger.AgentSnapshot{
		"planner": {Digest: "sha256:real", ProviderName: "deepseek", Model: "deepseek-v4"},
	}
	steps, err := buildStepRuntimesFromSnapshot(wf, nil, nil, pins)
	if err != nil {
		t.Fatal(err)
	}
	runtime := steps["plan"]
	if runtime.Digest != "sha256:real" || runtime.ProviderName != "deepseek" || runtime.Model != "deepseek-v4" {
		t.Fatalf("plan runtime = %+v, want the admitted pins", runtime)
	}

	if _, err := buildStepRuntimesFromSnapshot(wf, nil, nil, map[string]workflowledger.AgentSnapshot{}); err == nil {
		t.Fatal("a missing agent pin must fail closed")
	}

	steps, err = buildStepRuntimesFromSnapshot(wf, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if steps["plan"].Digest != "sha256:agent-planner" {
		t.Fatalf("nil pins must keep the legacy synthetic digest, got %q", steps["plan"].Digest)
	}
}

// TestResolveStepAgentsPinsDefinition pins that admission records the real
// definition digest and the complete declared provider/model pair, and fails
// closed on an unknown agent.
func TestResolveStepAgentsPinsDefinition(t *testing.T) {
	registry := agents.NewRegistry()
	if err := registry.Publish(agents.ResolvedAgent{Name: "planner", Provider: "deepseek", Model: "deepseek-v4"}); err != nil {
		t.Fatal(err)
	}
	wf := &compiler.CompiledWorkflow{Steps: []definition.Step{
		{ID: "plan", Kind: "agent", Agent: "planner"},
	}}
	pins, err := resolveStepAgents(wf, registry)
	if err != nil {
		t.Fatal(err)
	}
	pin := pins["planner"]
	if pin.Digest == "" || pin.Digest == "sha256:agent-planner" {
		t.Fatalf("pin digest = %q, want the real definition digest", pin.Digest)
	}
	if pin.ProviderName != "deepseek" || pin.Model != "deepseek-v4" {
		t.Fatalf("pin binding = %s/%s, want the declared pair", pin.ProviderName, pin.Model)
	}

	wf.Steps = append(wf.Steps, definition.Step{ID: "ghost", Kind: "agent", Agent: "ghost"})
	if _, err := resolveStepAgents(wf, registry); err == nil {
		t.Fatal("an unknown agent must fail admission closed")
	}
}

// TestVerifyStepAgentsFailsClosedOnDrift pins that resume re-resolves pinned
// agents and refuses a definition whose digest no longer matches admission.
func TestVerifyStepAgentsFailsClosedOnDrift(t *testing.T) {
	wf := &compiler.CompiledWorkflow{Steps: []definition.Step{
		{ID: "plan", Kind: "agent", Agent: "planner"},
	}}
	registry := agents.NewRegistry()
	if err := registry.Publish(agents.ResolvedAgent{Name: "planner"}); err != nil {
		t.Fatal(err)
	}
	pins, err := resolveStepAgents(wf, registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyStepAgents(wf, registry, pins); err != nil {
		t.Fatalf("an unchanged definition must verify: %v", err)
	}

	drifted := agents.NewRegistry()
	if err := drifted.Publish(agents.ResolvedAgent{Name: "planner", SystemPrompt: "changed"}); err != nil {
		t.Fatal(err)
	}
	if err := verifyStepAgents(wf, drifted, pins); err == nil || !strings.Contains(err.Error(), "changed since workflow admission") {
		t.Fatalf("drifted definition must fail closed, got %v", err)
	}
	if err := verifyStepAgents(wf, agents.NewRegistry(), pins); err == nil {
		t.Fatal("a deleted agent must fail closed")
	}
}
