package localengine_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/localengine"
)

// templateStepTemplate is the admitted template body for the template-step
// workspace. It renders inputs.task into the step prompt.
const templateStepTemplate = "Plan the task: {{ inputs.task }}."

// writeTemplateStepWorkspace writes a one-step agent workflow whose step "one"
// declares templateRef under the workflows directory.
func writeTemplateStepWorkspace(t *testing.T, templateRef, templateContent string) string {
	t.Helper()
	root := t.TempDir()
	initGitRepo(t, root)
	wfRoot := filepath.Join(root, ".mivia", "workflows")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(wfRoot, templateRef)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wfRoot, templateRef), []byte(templateContent), 0o600); err != nil {
		t.Fatal(err)
	}
	body := `version = 1
name = "template-step"
initial_step = "one"

[inputs.task]
type = "string"
required = true
max_bytes = 100

[[steps]]
id = "one"
kind = "agent"
agent = "one"
template = "` + templateRef + `"
context = [
  { from = "inputs.task", as = "task", max_bytes = 100 },
]
on_failure = "failure"

[[transitions]]
from = "one"
to = "success"
[transitions.match]
status = "succeeded"
`
	if err := os.WriteFile(filepath.Join(wfRoot, "template-step.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// capturingRunner records every dispatched AgentStepRequest so a test can
// assert what admission built for the step.
type capturingRunner struct {
	inner controller.AgentStepRunner
	reqs  chan controller.AgentStepRequest
}

func (r *capturingRunner) RunStep(ctx context.Context, req controller.AgentStepRequest) (controller.AgentStepResult, error) {
	r.reqs <- req
	return r.inner.RunStep(ctx, req)
}

func startTemplateStep(t *testing.T, svc *workflowledger.Service) workflowledger.StartResult {
	t.Helper()
	out, err := mustTool(t, svc, workflowledger.ToolWorkflowRun).Execute(
		context.Background(), json.RawMessage(`{"workflow":"template-step","inputs":{"task":"x"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var started workflowledger.StartResult
	if err := json.Unmarshal([]byte(out), &started); err != nil {
		t.Fatal(err)
	}
	if started.RunID == "" {
		t.Fatalf("start = %+v", started)
	}
	return started
}

// TestStartNewSnapshotCarriesTemplateBytes proves admission pins the agent
// step's template bytes into the run snapshot and dispatches the step with
// that template: the rendered prompt carries the template body (CLI parity
// with loadWorkflowRuntimes).
func TestStartNewSnapshotCarriesTemplateBytes(t *testing.T) {
	root := writeTemplateStepWorkspace(t, "templates/plan.md", templateStepTemplate)
	repo := workflowledger.NewMemoryRepository()
	reqs := make(chan controller.AgentStepRequest, 1)
	engine := &localengine.Engine{
		WorkspaceRoot: root,
		Repo:          repo,
		NewRunner: func() controller.AgentStepRunner {
			return &capturingRunner{inner: &localengine.StaticStepRunner{Output: json.RawMessage(`{"ok":true}`)}, reqs: reqs}
		},
	}
	svc := mustService(t, engine, repo)
	started := startTemplateStep(t, svc)
	waitRun(t, engine, started.RunID)

	var req controller.AgentStepRequest
	select {
	case req = <-reqs:
	default:
		t.Fatal("agent step was not dispatched")
	}
	if req.Template != templateStepTemplate {
		t.Fatalf("dispatched Template = %q, want the admitted template bytes", req.Template)
	}
	if !strings.Contains(req.Prompt, "Plan the task: x.") {
		t.Fatalf("dispatched Prompt must render the template body, got %q", req.Prompt)
	}

	raw, err := repo.GetRunSnapshot(context.Background(), started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := workflowledger.UnmarshalSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	ref, ok := snapshot.Templates["templates/plan.md"]
	if !ok || ref.Digest == "" || string(ref.Bytes) != templateStepTemplate {
		t.Fatalf("admitted template pin = %+v, want the workspace file bytes", ref)
	}
}

// TestIntegrationResumeServesAdmittedTemplateBytes proves resume dispatches the
// resumed step with the template admitted at start, not the template edited on
// disk after admission (CLI parity with loadStepReferences' prior-snapshot
// path). No filesystem read of the template happens on resume.
func TestIntegrationResumeServesAdmittedTemplateBytes(t *testing.T) {
	root := writeTemplateStepWorkspace(t, "templates/plan.md", templateStepTemplate)
	repo := workflowledger.NewMemoryRepository()
	block := make(chan struct{})
	entered := make(chan struct{}, 1)
	reqs := make(chan controller.AgentStepRequest, 2)
	engine := &localengine.Engine{
		WorkspaceRoot: root, Repo: repo,
		NewRunner: func() controller.AgentStepRunner {
			return &capturingRunner{inner: &localengine.StaticStepRunner{
				Output: json.RawMessage(`{"ok":true}`), BlockUntil: block,
				OnStep: func(controller.AgentStepRequest) {
					select {
					case entered <- struct{}{}:
					default:
					}
				},
			}, reqs: reqs}
		},
	}
	svc := mustService(t, engine, repo)
	started := startTemplateStep(t, svc)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("step did not start")
	}
	if err := engine.Interrupt(started.RunID); err != nil {
		t.Fatal(err)
	}
	close(block)
	assertInterruptedNonTerminal(t, repo, started.RunID)
	// Edit the template after admission; the resumed dispatch must not see it.
	templatePath := filepath.Join(root, ".mivia", "workflows", "templates", "plan.md")
	if err := os.WriteFile(templatePath, []byte("CHANGED {{ inputs.task }}."), 0o600); err != nil {
		t.Fatal(err)
	}
	resumeAndAssertSucceeded(t, engine, svc, started.RunID)

	var resumed controller.AgentStepRequest
	for i := 0; i < 2; i++ {
		resumed = <-reqs
	}
	if resumed.Template != templateStepTemplate {
		t.Fatalf("resumed dispatch Template = %q, want the admitted bytes", resumed.Template)
	}
	if !strings.Contains(resumed.Prompt, "Plan the task: x.") || strings.Contains(resumed.Prompt, "CHANGED") {
		t.Fatalf("resumed dispatch Prompt = %q, want the admitted body only", resumed.Prompt)
	}
}

// TestStartNewWithRegistryPinsRealAgentDigest proves that with an agent
// registry the admission pins the real definition digest (and the declared
// provider/model pair) into the snapshot, and dispatch carries that digest
// instead of the synthetic "sha256:agent-<name>".
func TestStartNewWithRegistryPinsRealAgentDigest(t *testing.T) {
	root := writeTemplateStepWorkspace(t, "templates/plan.md", templateStepTemplate)
	repo := workflowledger.NewMemoryRepository()
	registry := agents.NewRegistry()
	agent := agents.ResolvedAgent{Name: "one", Provider: "deepseek", Model: "deepseek-v4"}
	if err := registry.Publish(agent); err != nil {
		t.Fatal(err)
	}
	wantDigest, err := agent.DefinitionDigest()
	if err != nil {
		t.Fatal(err)
	}
	reqs := make(chan controller.AgentStepRequest, 1)
	engine := &localengine.Engine{
		WorkspaceRoot: root, Repo: repo, AgentRegistry: registry,
		NewRunner: func() controller.AgentStepRunner {
			return &capturingRunner{inner: &localengine.StaticStepRunner{Output: json.RawMessage(`{"ok":true}`)}, reqs: reqs}
		},
	}
	svc := mustService(t, engine, repo)
	started := startTemplateStep(t, svc)
	waitRun(t, engine, started.RunID)

	req := <-reqs
	if req.AgentDigest != wantDigest {
		t.Fatalf("dispatched AgentDigest = %q, want the admitted definition digest %q", req.AgentDigest, wantDigest)
	}
	if req.ProviderName != "deepseek" || req.Model != "deepseek-v4" {
		t.Fatalf("dispatched binding = %s/%s, want the admitted pair", req.ProviderName, req.Model)
	}
	raw, err := repo.GetRunSnapshot(context.Background(), started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := workflowledger.UnmarshalSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	pin := snapshot.Agents["one"]
	if pin.Digest != wantDigest || pin.ProviderName != "deepseek" || pin.Model != "deepseek-v4" {
		t.Fatalf("snapshot agent pin = %+v, want digest %q with the declared pair", pin, wantDigest)
	}
}

// TestIntegrationResumeFailsClosedOnDriftedAgent proves resume re-verifies a
// pinned agent against the live registry and refuses a definition that changed
// after admission (CLI parity with workflowAgent's prior check).
func TestIntegrationResumeFailsClosedOnDriftedAgent(t *testing.T) {
	root := writeTemplateStepWorkspace(t, "templates/plan.md", templateStepTemplate)
	repo := workflowledger.NewMemoryRepository()
	registry := agents.NewRegistry()
	if err := registry.Publish(agents.ResolvedAgent{Name: "one"}); err != nil {
		t.Fatal(err)
	}
	block := make(chan struct{})
	entered := make(chan struct{}, 1)
	engine := &localengine.Engine{
		WorkspaceRoot: root, Repo: repo, AgentRegistry: registry,
		NewRunner: func() controller.AgentStepRunner {
			return &localengine.StaticStepRunner{
				Output: json.RawMessage(`{"ok":true}`), BlockUntil: block,
				OnStep: func(controller.AgentStepRequest) {
					select {
					case entered <- struct{}{}:
					default:
					}
				},
			}
		},
	}
	svc := mustService(t, engine, repo)
	started := startTemplateStep(t, svc)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("step did not start")
	}
	if err := engine.Interrupt(started.RunID); err != nil {
		t.Fatal(err)
	}
	close(block)
	assertInterruptedNonTerminal(t, repo, started.RunID)

	// Swap the registry for one whose "one" definition drifted.
	drifted := agents.NewRegistry()
	if err := drifted.Publish(agents.ResolvedAgent{Name: "one", SystemPrompt: "changed"}); err != nil {
		t.Fatal(err)
	}
	engine.AgentRegistry = drifted

	_, err := mustTool(t, svc, workflowledger.ToolWorkflowRun).Execute(
		context.Background(), json.RawMessage(fmt.Sprintf(
			`{"resume":true,"run_id":%q,"force":true}`, started.RunID)))
	if err == nil || !strings.Contains(err.Error(), "changed since workflow admission") {
		t.Fatalf("resume with a drifted agent must fail closed, got %v", err)
	}
}
