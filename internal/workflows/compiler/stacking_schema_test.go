package compiler_test

// End-to-end regression for the chunk-plan validation split: the static
// chunk-plan-v1.json schema (the decompose step's OutputSchema) must do
// structural/type validation only, while the config-driven
// controller.ValidateChunkPlan host validator is the sole quantitative
// authority. A workflow that declares hard_lines=600 and max_chunks=20 must
// accept a 500-line / 15-chunk plan through BOTH gates, in the order the
// runtime consults them, and the schema must still reject structurally
// invalid plans. Before the fix, the schema hard-coded the default stacking
// bounds (maxItems: 12, maximum: 400), so a plan inside the declared
// thresholds was rejected by the static schema before the host validator was
// ever consulted and decompose failed on attempt 1.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/jschema"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// repoRoot returns the repository root directory, located from the test file
// path so the test does not depend on the working directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(file))))
}

// multiChunkPlanJSON builds a stack_mode=multi chunk plan with chunkCount
// disjoint chunks, each carrying linesPerChunk estimated diff lines.
func multiChunkPlanJSON(chunkCount, linesPerChunk int) json.RawMessage {
	chunks := make([]string, 0, chunkCount)
	for i := 0; i < chunkCount; i++ {
		chunks = append(chunks, fmt.Sprintf(
			`{"id":"c%d","title":"chunk %d","files":["f%d.go"],"est_diff_lines":%d,"tests":true,"depends_on":[]}`,
			i, i, i, linesPerChunk))
	}
	return json.RawMessage(fmt.Sprintf(`{"stack_mode":"multi","chunk_plan":{"chunks":[%s]}}`, strings.Join(chunks, ",")))
}

// loadChunkPlanSchema reads and compiles the real chunk-plan-v1.json schema
// exactly as the runtime does: admission pins it (compiler
// ValidateSchemaReferences) and the controller validates decompose output
// against it via jschema.
func loadChunkPlanSchema(t *testing.T) *jschema.Compiled {
	t.Helper()
	path := filepath.Join(repoRoot(t), ".mivia", "workflows", "schemas", "chunk-plan-v1.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read real chunk-plan schema: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("parse real chunk-plan schema: %v", err)
	}
	compiled, err := jschema.Compile(schema)
	if err != nil {
		t.Fatalf("compile real chunk-plan schema: %v", err)
	}
	return compiled
}

func TestChunkPlanDeclaredThresholdsAcceptPlanEndToEnd(t *testing.T) {
	// A workflow declaring hard_lines=600 and max_chunks=20 compiles cleanly
	// and resolves those thresholds into its stacking config.
	wf := &definition.WorkflowFile{
		Version:     1,
		Name:        "stack-thresholds",
		InitialStep: "plan",
		Steps: []definition.Step{
			{ID: "plan", Kind: "agent", Agent: "workflow-engineer"},
			{ID: "implement", Kind: "agent", Agent: "workflow-engineer",
				OutputSchema: "schemas/change-summary-v1.json",
				Context: []definition.ContextBinding{
					{From: "steps.plan.output", As: "plan"},
				}},
		},
		Transitions: []definition.Transition{
			{From: "plan", To: "implement"},
			{From: "implement", To: "success"},
		},
		Stacking: &definition.Stacking{PlanStep: "plan", ImplementStep: "implement", HardLines: 600, MaxChunks: 20},
	}
	cw, err := compiler.Compile(wf)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	if cw.Stacking == nil {
		t.Fatal("Compile resolved no stacking config")
	}
	if cw.Stacking.HardLines != 600 || cw.Stacking.MaxChunks != 20 {
		t.Fatalf("resolved stacking = hard_lines=%d max_chunks=%d, want 600/20", cw.Stacking.HardLines, cw.Stacking.MaxChunks)
	}

	// A 500-line / 15-chunk plan is inside the declared thresholds. The
	// runtime consults two gates in order: the static schema on the decompose
	// step's OutputSchema, then the config-driven ValidateChunkPlan host
	// validator. Both must accept it.
	plan := multiChunkPlanJSON(15, 500)

	compiled := loadChunkPlanSchema(t)
	if _, err := compiled.ValidateJSONBytes(plan); err != nil {
		t.Fatalf("real chunk-plan schema rejected a plan inside the declared thresholds: %v", err)
	}

	out, err := controller.ValidateChunkPlan(plan, cw.Stacking)
	if err != nil {
		t.Fatalf("ValidateChunkPlan decode error: %v", err)
	}
	if !out.Valid {
		t.Fatalf("ValidateChunkPlan rejected a plan inside the declared thresholds: %v", out.Reasons)
	}
}

func TestChunkPlanSchemaStillRejectsStructurallyInvalidPlans(t *testing.T) {
	// Stripping the quantitative bounds must not weaken the structural
	// contract: additional properties and missing required keys are still
	// rejected by the real schema.
	compiled := loadChunkPlanSchema(t)

	for _, raw := range []string{
		`{"stack_mode":"multi","chunk_plan":{"chunks":[]},"extra":true}`,
		`{"stack_mode":"multi","chunk_plan":{"chunks":[{"id":"c1","title":"t","files":["a.go"],"est_diff_lines":10,"depends_on":[]}]}}`,
	} {
		if _, err := compiled.ValidateJSONBytes(json.RawMessage(raw)); err == nil {
			t.Errorf("schema accepted structurally invalid plan %s", raw)
		}
	}
}
