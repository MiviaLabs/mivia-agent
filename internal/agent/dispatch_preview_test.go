package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// bigDispatchArgs builds a dispatch_tasks argument object the size a real
// multi-task batch reaches: the prompts are the payload, and they are what
// pushes it past any byte budget.
func bigDispatchArgs(t *testing.T, tasks int) string {
	t.Helper()
	body := strings.Repeat("audit this slice in detail. ", 140) // ~3.9 KiB each
	list := make([]any, 0, tasks)
	for i := 0; i < tasks; i++ {
		list = append(list, map[string]any{
			"id":     []string{"auto-core", "live-fanout", "tool-surface", "memory-store", "render", "ui-cli"}[i%6],
			"agent":  "auditor",
			"prompt": body,
		})
	}
	raw, err := json.Marshal(map[string]any{"tasks": list, "wait": "run"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

// TestDispatchPreviewSurvivesARealMultiTaskBatch is the defect: the operator
// surface re-parses this preview to fan a batch into one row per task, and a
// byte cut that lands mid-object parses to nothing - so a six-task dispatch
// rendered as a single empty row while six subagents ran. Reproduced against
// a 20 KiB batch, which is the size a real audit dispatch reaches.
func TestDispatchPreviewSurvivesARealMultiTaskBatch(t *testing.T) {
	raw := bigDispatchArgs(t, 6)
	if len(raw) <= editToolPreviewMaxBytes {
		t.Fatalf("fixture is %d bytes, must exceed the %d-byte preview budget to exercise the defect", len(raw), editToolPreviewMaxBytes)
	}

	preview := redactToolInputForTool("dispatch_tasks", raw)

	var out map[string]any
	if err := json.Unmarshal([]byte(preview), &out); err != nil {
		t.Fatalf("preview does not parse as JSON (%v); the per-task fan-out reads nothing: %q", err, preview)
	}
	tasks, _ := out["tasks"].([]any)
	if len(tasks) != 6 {
		t.Fatalf("preview carries %d tasks, want 6", len(tasks))
	}
	for i, rt := range tasks {
		task, _ := rt.(map[string]any)
		if id, _ := task["id"].(string); id == "" {
			t.Fatalf("task %d has no id; its row would be labelled by position only", i)
		}
		if agent, _ := task["agent"].(string); agent != "auditor" {
			t.Fatalf("task %d lost its agent name: %v", i, task)
		}
		if _, carried := task["prompt"]; carried {
			t.Fatalf("task %d carried its prompt into the preview; the prompt is the payload, not an identity", i)
		}
	}
	if len(preview) > editToolPreviewMaxBytes {
		t.Fatalf("preview is %d bytes, over the %d-byte budget", len(preview), editToolPreviewMaxBytes)
	}
}

// TestDispatchPreviewFallsBackOnUnparseableInput pins the fallback: a call
// whose arguments are not a task object keeps the old bounded byte cut
// rather than losing the preview altogether.
func TestDispatchPreviewFallsBackOnUnparseableInput(t *testing.T) {
	for _, raw := range []string{`not json at all`, `{"tasks":[]}`, `{"tasks":"nope"}`} {
		preview := redactToolInputForTool("dispatch_tasks", raw)
		if preview == "" {
			t.Fatalf("input %q produced an empty preview", raw)
		}
		if len(preview) > editToolPreviewMaxBytes {
			t.Fatalf("input %q produced a %d-byte preview, over budget", raw, len(preview))
		}
	}
}

// TestNonDispatchPreviewsStayTight pins that nothing else widened: an
// ordinary tool keeps the 256-byte operator preview.
func TestNonDispatchPreviewsStayTight(t *testing.T) {
	raw := `{"path":"` + strings.Repeat("a", 4096) + `"}`
	if got := len(redactToolInputForTool("read_file", raw)); got > 256 {
		t.Fatalf("read_file preview is %d bytes, want at most 256", got)
	}
}

// TestDispatchPreviewHandlesOddBatches covers the shapes a model can emit
// that the happy path does not: a task entry that is not an object at all,
// and a batch so large that even the reduced form breaks the budget. Both
// must stay bounded and must never panic.
func TestDispatchPreviewHandlesOddBatches(t *testing.T) {
	t.Run("non-object task keeps its row", func(t *testing.T) {
		raw := `{"tasks":[{"id":"real","agent":"auditor"},"not-an-object",42]}`
		preview := redactToolInputForTool("dispatch_tasks", raw)
		var out map[string]any
		if err := json.Unmarshal([]byte(preview), &out); err != nil {
			t.Fatalf("preview does not parse: %v (%q)", err, preview)
		}
		tasks, _ := out["tasks"].([]any)
		if len(tasks) != 3 {
			t.Fatalf("preview carries %d task rows, want 3 - a malformed entry still occupies a row so the fan-out's positions line up", len(tasks))
		}
	})

	t.Run("oversized reduced form falls back", func(t *testing.T) {
		// Ids alone big enough that even the stripped object cannot fit.
		list := make([]any, 0, 400)
		for i := 0; i < 400; i++ {
			list = append(list, map[string]any{"id": strings.Repeat("i", 64), "agent": strings.Repeat("a", 64)})
		}
		raw, err := json.Marshal(map[string]any{"tasks": list})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		preview := redactToolInputForTool("dispatch_tasks", string(raw))
		if len(preview) > editToolPreviewMaxBytes {
			t.Fatalf("preview is %d bytes, over the %d-byte budget", len(preview), editToolPreviewMaxBytes)
		}
	})
}
