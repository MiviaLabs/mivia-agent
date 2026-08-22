package cli

import (
	"context"
	"io"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/cliworkflow"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contentref"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"os"
	"path/filepath"
	"testing"
)

// writeWorkflowFixture creates a .mivia/workflows/<name>.toml file in dir.
// Duplicated from internal/cliworkflow (Go forbids cross-package _test.go
// sharing); characterization_test.go depends on it and must not change.
func writeWorkflowFixture(t *testing.T, dir, name, body string) {
	t.Helper()
	wfDir := filepath.Join(dir, ".mivia", "workflows")
	if err := os.MkdirAll(wfDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wfDir, name+".toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// runWorkflowsWithIO delegates to cliworkflow.RunWorkflowsWithIO. A local
// wrapper keeps characterization_test.go byte-identical after the
// cliworkflow extraction.
func runWorkflowsWithIO(args []string, stdout, stderr io.Writer) error {
	return cliworkflow.RunWorkflowsWithIO(args, stdout, stderr)
}

// miniStackWorkflowTOML is a minimal stacking-enabled delivery workflow: the
// plan run goes plan -> decompose -> chunk_plan_validate -> success and must
// settle at delivery_pending (delivery policy active), which is exactly the
// shape that used to return from the delivery branch before the stack drive.
const miniStackWorkflowTOML = `version = 1
name = "mini-stack"
description = "Minimal stacking + delivery workflow for the drive-ordering regression."
initial_step = "plan"

[inputs.task]
type = "string"
required = true

[[steps]]
id = "plan"
kind = "agent"
agent = "one"

[[steps]]
id = "implement"
kind = "agent"
agent = "two"

[[transitions]]
from = "plan"
to = "implement"
match = { status = "succeeded" }

[[transitions]]
from = "implement"
to = "success"
match = { status = "succeeded" }

[stacking]
plan_step = "plan"
implement_step = "implement"
merge_policy = "auto"

[delivery]
kind = "pull_request"
mode = "draft"
provider = "github"
base = "main"
title_template = "feat: {{ inputs.task }}"
`

func compileWorkflowFile(t *testing.T, path string) *definition.CompiledWorkflow {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wf, _, err := definition.ParseWorkflowTOML(raw, filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := definition.Compile(&wf)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

// multiChunkPlanOutput is a decompose step output that satisfies
// schemas/chunk-plan-v1.json and routes stack_mode=multi with two chunks.
// Duplicated from internal/cliworkflow (workflow_stack_drive_order_test.go).
const multiChunkPlanOutput = `{"stack_mode":"multi","chunk_plan":{"chunks":[
	{"id":"c1","title":"chunk one","files":["a.go"],"est_diff_lines":20,"tests":true,"depends_on":[]},
	{"id":"c2","title":"chunk two","files":["b.go"],"est_diff_lines":30,"tests":true,"depends_on":["c1"]}
]}}`

// scriptedMiniStackRuntimes builds per-step runtimes for the mini-stack
// fixture. Duplicated from internal/cliworkflow (workflow_stack_drive_order_test.go).
func scriptedMiniStackRuntimes(t *testing.T, synth *definition.CompiledWorkflow) map[string]controller.StepRuntime {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Dir(filepath.Dir(cwd)) // internal/cli -> repo root
	templatesDir := filepath.Join(repoRoot, ".mivia", "workflows", "templates")
	steps := make(map[string]controller.StepRuntime, len(synth.Steps))
	for _, s := range synth.Steps {
		runtime := controller.StepRuntime{Agent: agents.ResolvedAgent{Name: "one"}, Digest: "digest-" + s.ID}
		if s.Template != "" {
			data, readErr := os.ReadFile(filepath.Join(templatesDir, filepath.Base(s.Template)))
			if readErr != nil {
				t.Fatalf("read template %s: %v", s.Template, readErr)
			}
			runtime.Template = string(data)
		}
		if s.ID == "implement" {
			runtime.Agent = agents.ResolvedAgent{Name: "two"}
		}
		steps[s.ID] = runtime
	}
	return steps
}

// miniStackSnapshot builds the admitted snapshot for the mini-stack fixture.
// Duplicated from internal/cliworkflow (workflow_stack_drive_order_test.go).
func miniStackSnapshot(t *testing.T, root string, compiled *definition.CompiledWorkflow, rawDefinition []byte) workflowledger.Snapshot {
	t.Helper()
	skills, err := loadChatSkills(root)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := loadAgentDefinitions(root, "", skills)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := workflowledger.Snapshot{
		SchemaVersion:  workflowledger.SnapshotSchemaVersion,
		DefinitionTOML: rawDefinition, DefinitionDigest: compiled.Digest,
		Inputs: map[string]string{"task": "x"},
		Agents: map[string]workflowledger.AgentSnapshot{},
	}
	for _, name := range []string{"one", "two"} {
		agent, ok := loaded.Registry.Get(name)
		if !ok {
			t.Fatalf("agent %q is missing", name)
		}
		digest, digestErr := agent.DefinitionDigest()
		if digestErr != nil {
			t.Fatal(digestErr)
		}
		snapshot.Agents[name] = workflowledger.AgentSnapshot{
			Digest: digest, ProviderName: "openrouter", Model: "test/model",
		}
	}
	return snapshot
}

// workflowTestDispatcher is a no-op dispatcher stand-in for workflow tests.
// Duplicated from internal/cliworkflow (workflow_helpers_pass4_test.go).
type workflowTestDispatcher struct{}

// Close closes the test dispatcher.
func (workflowTestDispatcher) Close() {}

// newWorkflowBuildFixture builds the standard two-step workflow build
// fixture. Duplicated from internal/cliworkflow (workflow_helpers_pass4_test.go).
func newWorkflowBuildFixture(t *testing.T) (string, *config.Resolved, *storage.SQLite, workflowledger.Repository, *definition.CompiledWorkflow) {
	t.Helper()
	root := t.TempDir()
	storePath := filepath.Join(root, "workflow.db")
	writeWorkflowRunFixture(t, root, "https://example.com", storePath)
	res, err := config.Load(config.LoadOptions{ConfigPath: filepath.Join(root, "config.toml")})
	if err != nil {
		t.Fatal(err)
	}
	store, repo, closeFn, err := cliworkflow.OpenWorkflowStore(root, res.Subagents)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeFn)
	raw, err := os.ReadFile(filepath.Join(root, ".mivia", "workflows", "two-step.toml"))
	if err != nil {
		t.Fatal(err)
	}
	definitionFile, _, err := definition.ParseWorkflowTOML(raw, "two-step.toml")
	if err != nil {
		t.Fatal(err)
	}
	wf, err := definition.Compile(&definitionFile)
	if err != nil {
		t.Fatal(err)
	}
	return root, res, store, repo, wf
}

// seedSucceededDecomposeAttempt records a succeeded decompose attempt whose
// output is the given plan JSON. Duplicated from internal/cliworkflow.
func seedSucceededDecomposeAttempt(t *testing.T, repo workflowledger.Repository, runID string, output []byte) {
	t.Helper()
	ctx := context.Background()
	ref := contentref.Reference(contentref.KindOutput, output)
	if err := repo.StoreContent(ctx, ref, output); err != nil {
		t.Fatal(err)
	}
	attempt := workflowledger.StepAttempt{AttemptID: "wfa-decompose-1", RunID: runID, StepID: delivery.DecomposeStepID, AttemptNo: 1}
	if err := repo.RecordStepAttemptOutcome(ctx, attempt, workflowledger.AttemptOutcome{
		Status: workflowledger.AttemptStatusSucceeded, OutputRef: ref, OutputDigest: workflowledger.DigestHex(output),
	}); err != nil {
		t.Fatal(err)
	}
}
