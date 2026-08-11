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

	"github.com/MiviaLabs/mivia-agent/internal/jschema"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/localengine"
)

// TestIntegrationResumeSurvivesDeletedSchemaFile proves resume rebuilds step
// runtimes from the admitted snapshot's schema bytes, not the live filesystem:
// deleting the schema file after admission must not break resume (CLI parity
// with loadStepReferences' prior-snapshot path).
func TestIntegrationResumeSurvivesDeletedSchemaFile(t *testing.T) {
	engine, repo, svc, started, block, entered := startBlockedSchemaStep(t)
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
	schemaPath := filepath.Join(engine.WorkspaceRoot, ".mivia", "workflows", "schemas", "out.json")
	if err := os.Remove(schemaPath); err != nil {
		t.Fatal(err)
	}
	resumeAndAssertSucceeded(t, engine, svc, started.RunID)
}

// TestIntegrationResumePinsAdmittedSchemaBytes proves resume validates agent
// output against the schema bytes admitted at start, not a schema edited on
// disk afterwards: an edited schema that would reject the step output must not
// affect the resumed run.
func TestIntegrationResumePinsAdmittedSchemaBytes(t *testing.T) {
	engine, repo, svc, started, block, entered := startBlockedSchemaStep(t)
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
	// Edit the schema to one that rejects the admitted output {"ok":true}.
	edited := `{"type":"object","required":["ok","extra"],"properties":{"ok":{"type":"boolean"},"extra":{"type":"string"}},"additionalProperties":false}`
	schemaPath := filepath.Join(engine.WorkspaceRoot, ".mivia", "workflows", "schemas", "out.json")
	if err := os.WriteFile(schemaPath, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	resumeAndAssertSucceeded(t, engine, svc, started.RunID)
}

// TestStartNewFailsClosedOnMissingSchemaFile proves admission still fails
// closed when the workspace lacks the output_schema file: startNew keeps
// reading from the workspace at admission (that IS admission); only resume
// reads the admitted snapshot.
func TestStartNewFailsClosedOnMissingSchemaFile(t *testing.T) {
	root := writeSchemaStepWorkspace(t, "schemas/out.json", "")
	repo := workflowledger.NewMemoryRepository()
	engine := &localengine.Engine{
		WorkspaceRoot: root,
		Repo:          repo,
		NewRunner: func() controller.AgentStepRunner {
			return &localengine.StaticStepRunner{Output: json.RawMessage(`{"ok":true}`)}
		},
	}
	svc := mustService(t, engine, repo)
	_, err := mustTool(t, svc, agenttools.ToolWorkflowRun).Execute(
		context.Background(), json.RawMessage(`{"workflow":"schema-step","inputs":{"task":"build"}}`))
	if err == nil {
		t.Fatal("startNew with a missing output_schema file must fail closed")
	}
	if !strings.Contains(err.Error(), "output_schema") {
		t.Fatalf("startNew error = %v, want an output_schema failure", err)
	}
}

// TestStartNewSnapshotCarriesSchemaBytes pins that admission records the
// resolved output_schema bytes (with content digest) in the run snapshot, so
// resume has the pinned bytes without touching the filesystem.
func TestStartNewSnapshotCarriesSchemaBytes(t *testing.T) {
	schemaContent := `{"type":"object","required":["ok"],"properties":{"ok":{"type":"boolean"}},"additionalProperties":false}`
	root := writeSchemaStepWorkspace(t, "schemas/out.json", schemaContent)
	repo := workflowledger.NewMemoryRepository()
	engine := &localengine.Engine{
		WorkspaceRoot: root,
		Repo:          repo,
		NewRunner: func() controller.AgentStepRunner {
			return &localengine.StaticStepRunner{Output: json.RawMessage(`{"ok":true}`)}
		},
	}
	svc := mustService(t, engine, repo)
	started := startSchemaStep(t, svc)
	raw, err := repo.GetRunSnapshot(context.Background(), started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := workflowledger.UnmarshalSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	ref, ok := snapshot.Schemas["schemas/out.json"]
	if !ok {
		t.Fatalf("admitted snapshot has no schema entry for schemas/out.json: %+v", snapshot.Schemas)
	}
	if ref.Digest == "" || len(ref.Bytes) == 0 {
		t.Fatalf("admitted schema entry is not pinned: digest=%q bytes=%d", ref.Digest, len(ref.Bytes))
	}
	if string(ref.Bytes) != schemaContent {
		t.Fatalf("admitted schema bytes = %q, want the workspace file bytes", ref.Bytes)
	}
	// Join the background run goroutine. Its final .mivia/runs trace write
	// must finish before t.TempDir cleanup removes the workspace.
	waitRun(t, engine, started.RunID)
}

// schemaStepSchema is the closed JSON Schema accepted at admission for the
// schema-step workspace: it admits exactly {"ok":true} style outputs.
const schemaStepSchema = `{"type":"object","required":["ok"],"properties":{"ok":{"type":"boolean"}},"additionalProperties":false}`

// writeSchemaStepWorkspace writes a one-step agent workflow whose step "one"
// declares output_schema schemaRef under the workflows directory. An empty
// schemaContent skips writing the file (for missing-schema admission tests).
func writeSchemaStepWorkspace(t *testing.T, schemaRef, schemaContent string) string {
	t.Helper()
	root := t.TempDir()
	wfRoot := filepath.Join(root, ".mivia", "workflows")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(wfRoot, schemaRef)), 0o700); err != nil {
		t.Fatal(err)
	}
	if schemaContent != "" {
		if err := os.WriteFile(filepath.Join(wfRoot, schemaRef), []byte(schemaContent), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	body := `version = 1
name = "schema-step"
initial_step = "one"

[inputs.task]
type = "string"
required = true
max_bytes = 100

[[steps]]
id = "one"
kind = "agent"
agent = "one"
output_schema = "` + schemaRef + `"
on_failure = "failure"

[[transitions]]
from = "one"
to = "success"
[transitions.match]
status = "succeeded"
`
	if err := os.WriteFile(filepath.Join(wfRoot, "schema-step.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// startSchemaStep starts a schema-step run through the tool path and returns
// the admission result.
func startSchemaStep(t *testing.T, svc *agenttools.Service) agenttools.StartResult {
	t.Helper()
	out, err := mustTool(t, svc, agenttools.ToolWorkflowRun).Execute(
		context.Background(), json.RawMessage(`{"workflow":"schema-step","inputs":{"task":"x"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var started agenttools.StartResult
	if err := json.Unmarshal([]byte(out), &started); err != nil {
		t.Fatal(err)
	}
	if started.RunID == "" || started.Status == "" {
		t.Fatalf("start = %+v", started)
	}
	return started
}

// schemaEnforcingRunner mirrors the production CoordinatorRunner's schema gate
// (controller/agent_step.go finish -> validateOutput): output that fails the
// step's declared output_schema fails the step. StaticStepRunner alone never
// validates, so without this wrapper the schema-pinning resume tests could not
// distinguish the admitted schema from a schema edited on disk.
type schemaEnforcingRunner struct {
	inner controller.AgentStepRunner
}

func (r *schemaEnforcingRunner) RunStep(ctx context.Context, req controller.AgentStepRequest) (controller.AgentStepResult, error) {
	result, err := r.inner.RunStep(ctx, req)
	if err != nil {
		return result, err
	}
	if req.OutputSchema == nil {
		return result, nil
	}
	compiled, err := jschema.Compile(req.OutputSchema)
	if err != nil {
		return result, fmt.Errorf("compile step output schema: %w", err)
	}
	if _, err := compiled.ValidateJSONBytes(result.Output); err != nil {
		return result, fmt.Errorf("step %q: output schema validation failed: %w", req.StepID, err)
	}
	return result, nil
}

// startBlockedSchemaStep starts a schema-step run whose agent step blocks
// until the returned channel is closed, mirroring startBlockedTwoStep. The
// runner enforces the step's output_schema like the production coordinator
// runner does.
func startBlockedSchemaStep(t *testing.T) (*localengine.Engine, workflowledger.Repository, *agenttools.Service, agenttools.StartResult, chan struct{}, chan struct{}) {
	t.Helper()
	root := writeSchemaStepWorkspace(t, "schemas/out.json", schemaStepSchema)
	repo := workflowledger.NewMemoryRepository()
	block := make(chan struct{})
	entered := make(chan struct{}, 1)
	engine := &localengine.Engine{
		WorkspaceRoot: root, Repo: repo,
		NewRunner: func() controller.AgentStepRunner {
			return &schemaEnforcingRunner{inner: &localengine.StaticStepRunner{
				Output: json.RawMessage(`{"ok":true}`), BlockUntil: block,
				OnStep: func(controller.AgentStepRequest) {
					select {
					case entered <- struct{}{}:
					default:
					}
				},
			}}
		},
	}
	svc := mustService(t, engine, repo)
	started := startSchemaStep(t, svc)
	return engine, repo, svc, started, block, entered
}
