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
	inputs, snapshot := chunkRunInputs(map[string]string{"task": "whole feature"}, "c2", "master", "2/3", plan, nil)
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
	inputs, snapshot := chunkRunInputs(map[string]string{"task": "whole feature"}, stackIntegrationChunkID, "master", "", nil, nil)
	if _, present := inputs["chunk_plan"]; present {
		t.Fatalf("inputs[chunk_plan] present for a nil plan entry; the integration run must omit it")
	}
	if _, present := snapshot["chunk_plan"]; present {
		t.Fatalf("snapshot chunk_plan present for a nil plan entry; the integration run must omit it")
	}
}

// TestChunkRunInputsCarrySiblingFiles pins the full-plan ground truth: one
// chunk's admission carries the union of every OTHER chunk's declared files
// as sibling_files, so the review filters can classify cross-tree sibling
// demands exactly instead of by directory heuristic. The chunk's own files
// never appear in the union.
func TestChunkRunInputsCarrySiblingFiles(t *testing.T) {
	chunks := map[string]*ChunkPlan{
		"c1": {ID: "c1", Title: "runeutil", Files: []string{"internal/runeutil/runeutil.go"}},
		"c2": {ID: "c2", Title: "pathutil", Files: []string{"internal/pathutil/pathutil.go", "./internal/pathutil/pathutil_test.go"}},
		"c3": {ID: "c3", Title: "cli wiring", Files: []string{"cmd/mivia/wire.go"}},
	}
	inputs, _ := chunkRunInputs(map[string]string{"task": "whole feature"}, "c2", "master", "2/3", chunks["c2"], siblingChunkFiles(chunks, "c2"))
	raw, ok := inputs["sibling_files"].(string)
	if !ok || raw == "" {
		t.Fatalf("inputs[sibling_files] = %v, want the sibling union as JSON", inputs["sibling_files"])
	}
	var got []string
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("sibling_files is not valid JSON: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("sibling_files = %#v, want only the OTHER chunks' files (own files excluded)", got)
	}
	for _, f := range got {
		if f != "internal/runeutil/runeutil.go" && f != "cmd/mivia/wire.go" {
			t.Fatalf("sibling_files = %#v, want only c1 and c3 files", got)
		}
	}
}

// TestChunkRunInputsOmitSiblingFilesWhenEmpty pins the fallback: no sibling
// union (integration run, single-chunk stack) omits the input entirely.
func TestChunkRunInputsOmitSiblingFilesWhenEmpty(t *testing.T) {
	inputs, snapshot := chunkRunInputs(map[string]string{"task": "x"}, "c1", "master", "1/1",
		&ChunkPlan{ID: "c1", Files: []string{"internal/a/a.go"}}, nil)
	if _, present := inputs["sibling_files"]; present {
		t.Fatalf("inputs[sibling_files] present with no siblings")
	}
	if _, present := snapshot["sibling_files"]; present {
		t.Fatalf("snapshot[sibling_files] present with no siblings")
	}
}
