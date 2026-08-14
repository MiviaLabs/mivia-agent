package cli

// Pins the chunk-scope plumbing that the smoke-stack-3chunk-v3 live run
// proved missing: a chunk-mode run received only the full original task and
// a bare chunk ID, never the decompose plan's slice (title + files), so
// every chunk's implement agent did the WHOLE task and sibling PRs merged
// duplicate definitions of the same functions into master. The chunk run's
// admission inputs must carry the chunk's own plan entry as chunk_plan.

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestChunkRunInputsCarryTheChunkPlanSlice(t *testing.T) {
	plan := &ChunkPlan{
		ID:        "c2",
		Title:     "Add internal/pathutil SplitExt",
		Files:     []string{"internal/pathutil/pathutil.go", "internal/pathutil/pathutil_test.go"},
		DependsOn: []string{"c1"},
	}
	inputs, snapshot := chunkRunInputs(map[string]string{"task": "whole feature"}, "c2", "master", "2/3", plan)
	raw, ok := inputs["chunk_plan"].(string)
	if !ok || raw == "" {
		t.Fatalf("inputs[chunk_plan] = %v, want the chunk's plan entry as JSON", inputs["chunk_plan"])
	}
	if snapshot["chunk_plan"] != raw {
		t.Fatalf("snapshot chunk_plan = %q, want the same JSON as inputs", snapshot["chunk_plan"])
	}
	var got ChunkPlan
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("chunk_plan is not valid JSON: %v", err)
	}
	if got.ID != "c2" || got.Title != plan.Title || len(got.Files) != 2 || got.Files[0] != plan.Files[0] {
		t.Fatalf("chunk_plan = %+v, want the chunk's own id, title, and files", got)
	}
	if !strings.Contains(raw, "internal/pathutil/pathutil.go") {
		t.Fatalf("chunk_plan %q must name the chunk's declared files", raw)
	}
}

func TestChunkRunInputsOmitChunkPlanWithoutAPlanEntry(t *testing.T) {
	// The integration run has no chunk slice; it must not carry chunk_plan
	// (stack_mode=single admission forbids it).
	inputs, snapshot := chunkRunInputs(map[string]string{"task": "whole feature"}, stackIntegrationChunkID, "master", "", nil)
	if _, present := inputs["chunk_plan"]; present {
		t.Fatalf("inputs[chunk_plan] present for a nil plan entry; the integration run must omit it")
	}
	if _, present := snapshot["chunk_plan"]; present {
		t.Fatalf("snapshot chunk_plan present for a nil plan entry; the integration run must omit it")
	}
}
