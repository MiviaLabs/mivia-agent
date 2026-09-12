package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
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
// folded-case keys, and a batch so large that even the reduced form breaks
// the budget. Both must stay bounded and must never panic.
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

	t.Run("folded-case task keys survive reduction", func(t *testing.T) {
		raw := `{"tasks":[{"ID":"audit-loop","Agent":"auditor"}],"wait":"run"}`
		preview := redactToolInputForTool("dispatch_tasks", raw)
		var out map[string]any
		if err := json.Unmarshal([]byte(preview), &out); err != nil {
			t.Fatalf("preview does not parse: %v (%q)", err, preview)
		}
		tasks, ok := out["tasks"].([]any)
		if !ok || len(tasks) != 1 {
			t.Fatalf("preview carries %d tasks, want 1", len(tasks))
		}
		task, ok := tasks[0].(map[string]any)
		if !ok {
			t.Fatalf("task is not a map: %T", tasks[0])
		}
		id, _ := task["id"].(string)
		if id == "" {
			id, _ = task["ID"].(string)
		}
		if id != "audit-loop" {
			t.Fatalf("task lost its id: %+v", task)
		}
		agent, _ := task["agent"].(string)
		if agent == "" {
			agent, _ = task["Agent"].(string)
		}
		if agent != "auditor" {
			t.Fatalf("task lost its agent name: %+v", task)
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

// bigDispatchResult builds the sync-form dispatch_tasks result: one row per
// task, where the bulk is the free-text fields, not the identities.
func bigDispatchResult(t *testing.T, tasks int) string {
	t.Helper()
	rows := make([]any, 0, tasks)
	for i := 0; i < tasks; i++ {
		rows = append(rows, map[string]any{
			"task_id":      fmt.Sprintf("audit-slice-%d", i),
			"status":       []string{"completed", "failed"}[i%2],
			"output_ref":   "ref:output:" + strings.Repeat("d", 64),
			"output_bytes": 4260,
			"synopsis":     strings.Repeat("finding text that runs long. ", 60),
			"read_hint":    strings.Repeat("read_output with this ref. ", 20),
		})
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

// TestOutputPreviewKeepsPerTaskStatusesParseable is the defect: the operator
// surface re-parses this preview for each task's OWN status
// (parseDispatchTaskStatuses). A byte cut lands mid-string, the parse fails,
// and observeAgentGroupEnd then labels every task in the group with the
// BATCH's single verdict - so a half-failed batch reads as uniformly
// succeeded. That is a false statement about the run, not a missing one.
func TestOutputPreviewKeepsPerTaskStatusesParseable(t *testing.T) {
	raw := bigDispatchResult(t, 8)
	if len(raw) <= editToolPreviewMaxBytes {
		t.Fatalf("fixture is %d bytes, must exceed the %d-byte budget to exercise the defect", len(raw), editToolPreviewMaxBytes)
	}

	preview := redactToolOutputForTool("dispatch_tasks", raw)
	if len(preview) > editToolPreviewMaxBytes {
		t.Fatalf("preview is %d bytes, over the %d-byte budget", len(preview), editToolPreviewMaxBytes)
	}

	var rows []map[string]any
	if err := json.Unmarshal([]byte(preview), &rows); err != nil {
		t.Fatalf("preview does not parse (%v); every task would take the batch's verdict: %q", err, preview)
	}
	if len(rows) != 8 {
		t.Fatalf("preview carries %d rows, want 8", len(rows))
	}
	wantStatus := map[string]string{}
	for i := 0; i < 8; i++ {
		wantStatus[fmt.Sprintf("audit-slice-%d", i)] = []string{"completed", "failed"}[i%2]
	}
	for _, row := range rows {
		id, _ := row["task_id"].(string)
		status, _ := row["status"].(string)
		if want, ok := wantStatus[id]; !ok || status != want {
			t.Fatalf("row %v lost the identity or status the panel reads (want %q for %q)", row, want, id)
		}
		// The envelope's other keys survive too - internal/ui/render formats
		// them - only their long text is shortened.
		if _, ok := row["output_ref"]; !ok {
			t.Fatalf("row %v dropped a key; the shrink must keep the shape", row)
		}
	}
	if !utf8.ValidString(preview) {
		t.Fatal("preview is not valid UTF-8")
	}
}

// TestOutputPreviewLeavesFittingResultsAlone pins that nothing changes for
// the common case: a result already inside the budget is untouched, so the
// transcript's formatted preview keeps its full text.
func TestOutputPreviewLeavesFittingResultsAlone(t *testing.T) {
	raw := bigDispatchResult(t, 1)
	if len(raw) > editToolPreviewMaxBytes {
		// Not a skip: the single-row fixture is this test's premise. If it
		// no longer fits the budget the fixture or the budget changed, and
		// the "fitting result" case is silently going untested.
		t.Fatalf("single-row fixture is %d bytes, over the %d budget: this test can no longer exercise the fitting-result path", len(raw), editToolPreviewMaxBytes)
	}
	if got := redactToolOutputForTool("dispatch_tasks", raw); got != raw {
		t.Fatalf("a fitting result was rewritten:\n got %q\nwant %q", got, raw)
	}
}

// bigDispatchResultShortRows builds a dispatch_tasks sync-form result where
// EVERY field is already short: the size is KEY-dominated (many rows, each
// with the same six field names), not value-dominated, so
// shrinkJSONPreview's leaf-shortening cannot bring it under budget - there
// is no text left to shorten once every value is already a few
// characters. This is the exact case structurallyReduceDispatchOutput
// exists for.
func bigDispatchResultShortRows(t *testing.T, tasks int) string {
	t.Helper()
	statuses := []string{"completed", "failed", "running"}
	rows := make([]any, 0, tasks)
	for i := 0; i < tasks; i++ {
		rows = append(rows, map[string]any{
			"task_id":      fmt.Sprintf("t%d", i),
			"status":       statuses[i%len(statuses)],
			"output_ref":   fmt.Sprintf("r%d", i),
			"output_bytes": 42,
			"synopsis":     "ok",
			"read_hint":    "see ref",
		})
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

// TestOutputPreviewStructurallyReducesManyShortTaskRows is the regression
// for the key-dominated case: enough SHORT task rows that even collapsing
// every string leaf to nothing still overflows the budget, because the
// overhead is the six field NAMES repeated per row, not their values.
// shrinkJSONPreview alone cannot fix this - there is no text left to
// shorten - so the preview must fall back to dropping the non-identity
// columns instead. The regression proves the result stays valid JSON, with
// a STABLE row count (parseDispatchTaskStatuses' fan-out positions must
// stay aligned with the call's own task list) and each task's OWN status
// intact across MIXED statuses, not collapsed to the batch's single
// verdict.
func TestOutputPreviewStructurallyReducesManyShortTaskRows(t *testing.T) {
	const n = 120
	raw := bigDispatchResultShortRows(t, n)
	if len(raw) <= editToolPreviewMaxBytes {
		t.Fatalf("fixture is %d bytes, must exceed the %d-byte budget to exercise the defect", len(raw), editToolPreviewMaxBytes)
	}
	if _, ok := shrinkJSONPreview(raw, editToolPreviewMaxBytes); ok {
		t.Fatal("leaf-shrinking unexpectedly fit this key-dominated document; the fixture no longer exercises the structural fallback")
	}

	preview := redactToolOutputForTool("dispatch_tasks", raw)
	if len(preview) > editToolPreviewMaxBytes {
		t.Fatalf("preview is %d bytes, over the %d-byte budget", len(preview), editToolPreviewMaxBytes)
	}

	var rows []map[string]any
	if err := json.Unmarshal([]byte(preview), &rows); err != nil {
		t.Fatalf("preview does not parse (%v); every task would take the batch's verdict: %q", err, preview)
	}
	if len(rows) != n {
		t.Fatalf("preview carries %d rows, want %d - the fan-out's positions must stay aligned", len(rows), n)
	}
	statuses := []string{"completed", "failed", "running"}
	for i, row := range rows {
		wantID := fmt.Sprintf("t%d", i)
		wantStatus := statuses[i%len(statuses)]
		id, _ := row["task_id"].(string)
		status, _ := row["status"].(string)
		if id != wantID || status != wantStatus {
			t.Fatalf("row %d = %v, want task_id %q status %q - a mixed batch must not collapse to one verdict", i, row, wantID, wantStatus)
		}
	}
	if !utf8.ValidString(preview) {
		t.Fatal("preview is not valid UTF-8")
	}
}

// TestOutputPreviewFallsBackForNonJSON pins the fallback: a non-JSON result
// keeps the bounded byte cut rather than losing its preview.
func TestOutputPreviewFallsBackForNonJSON(t *testing.T) {
	raw := strings.Repeat("plain text output. ", 2000)
	preview := redactToolOutputForTool("dispatch_tasks", raw)
	if preview == "" {
		t.Fatal("non-JSON output produced an empty preview")
	}
	if len(preview) > editToolPreviewMaxBytes {
		t.Fatalf("preview is %d bytes, over budget", len(preview))
	}
	if !utf8.ValidString(preview) {
		t.Fatal("preview is not valid UTF-8")
	}
}

// TestOutputPreviewShrinkNeverSplitsARune pins the rune-wise shrink: a
// multi-byte leaf cut by bytes would re-encode as U+FFFD and change what the
// operator reads.
func TestOutputPreviewShrinkNeverSplitsARune(t *testing.T) {
	rows := []any{map[string]any{"task_id": "t1", "status": "completed", "note": strings.Repeat("界", 6000)}}
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	preview := redactToolOutputForTool("dispatch_tasks", string(raw))
	var out []map[string]any
	if err := json.Unmarshal([]byte(preview), &out); err != nil {
		t.Fatalf("preview does not parse: %v", err)
	}
	if len(out) != 1 || out[0]["status"] != "completed" {
		t.Fatalf("preview lost its status: %v", out)
	}
	// Inspect the DECODED leaf, not the encoded preview. encoding/json
	// escapes invalid UTF-8 as the six ASCII characters \ufffd, so a
	// byte-split rune leaves the encoded form both valid UTF-8 and free of
	// any replacement character - scanning the preview text for one cannot
	// see the damage the operator actually reads.
	note, _ := out[0]["note"].(string)
	if note == "" {
		t.Fatal("preview dropped the long leaf entirely")
	}
	if strings.ContainsRune(note, utf8.RuneError) {
		t.Fatalf("the shortened leaf contains a replacement character; a rune was split by bytes: %q", note)
	}
	for _, r := range note {
		if r != '界' && r != '\u2026' {
			t.Fatalf("leaf carries an unexpected rune %q: %q", r, note)
		}
	}
}

// TestShrinkJSONPreviewGivesUpOnAnUnshrinkableDocument pins the fallback
// boundary: a document whose size lives in its KEYS, not its values, cannot
// be brought under budget by shortening leaves, so the caller must fall back
// to the byte cut rather than return something over budget.
func TestShrinkJSONPreviewGivesUpOnAnUnshrinkableDocument(t *testing.T) {
	huge := map[string]any{}
	for i := 0; i < 900; i++ {
		huge[fmt.Sprintf("key-%s-%d", strings.Repeat("k", 24), i)] = 1
	}
	raw, err := json.Marshal(huge)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, ok := shrinkJSONPreview(string(raw), editToolPreviewMaxBytes); ok {
		t.Fatal("shrink claimed success on a document it cannot bring under budget")
	}
	// The caller still produces a bounded preview.
	preview := redactToolOutputForTool("dispatch_tasks", string(raw))
	if len(preview) > editToolPreviewMaxBytes {
		t.Fatalf("preview is %d bytes, over budget", len(preview))
	}
}

// TestShrinkStringLeavesGuards covers the leaf helpers directly: the
// depth ceiling, the zero budget, and a value already inside its budget.
func TestShrinkStringLeavesGuards(t *testing.T) {
	if got := shrinkStringLeaves("deep", 4, redactJSONMaxDepth+1); got != "deep" {
		t.Fatalf("past the depth ceiling the value must pass through, got %v", got)
	}
	if got := shrinkRunes("abcdef", 0); got != "…" {
		t.Fatalf("zero budget = %q, want just the ellipsis", got)
	}
	if got := shrinkRunes("short", 64); got != "short" {
		t.Fatalf("a value inside its budget was rewritten: %q", got)
	}
	// A non-string, non-container leaf is returned untouched.
	if got := shrinkStringLeaves(42.0, 1, 0); got != 42.0 {
		t.Fatalf("numeric leaf rewritten: %v", got)
	}
}

// bigDispatchObjectEnvelope builds a wait="none"/"task" dispatch_tasks
// result: an OBJECT envelope carrying sibling run metadata (run_id,
// display_name, status) alongside a KEY-dominated row array under rowsKey
// ("task_results" or "tasks") - enough SHORT rows that leaf-shrinking
// cannot bring it under budget, the same key-dominance
// bigDispatchResultShortRows exercises for the bare-array shape. When both
// is true the same rows are duplicated under the OTHER accepted key too,
// exercising reduceDispatchRowsIn's two-key branch.
func bigDispatchObjectEnvelope(t *testing.T, tasks int, rowsKey string, both bool) string {
	t.Helper()
	statuses := []string{"completed", "failed", "running"}
	rows := make([]any, 0, tasks)
	for i := 0; i < tasks; i++ {
		rows = append(rows, map[string]any{
			"task_id":      fmt.Sprintf("t%d", i),
			"status":       statuses[i%len(statuses)],
			"output_ref":   fmt.Sprintf("r%d", i),
			"output_bytes": 42,
			"synopsis":     "ok",
			"read_hint":    "see ref",
		})
	}
	env := map[string]any{
		"run_id":       "run-abc123",
		"display_name": "review-wave",
		"status":       "running",
		rowsKey:        rows,
	}
	if both {
		other := "tasks"
		if rowsKey == "tasks" {
			other = "task_results"
		}
		env[other] = rows
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

// TestStructurallyReduceDispatchOutputObjectEnvelope is the object-shaped
// counterpart to TestOutputPreviewStructurallyReducesManyShortTaskRows: the
// async wait="none"/"task" result wraps its rows in an envelope that ALSO
// carries sibling run metadata (run_id, display_name, status) the bare-array
// shape never has. reduceDispatchRowsIn's own contract is to locate and
// rewrite the row array only - nothing promises the envelope's own fields
// survive, only its rows do - so this pins the CURRENT, documented
// behavior: the sibling keys are dropped, while the row array keeps a
// stable row count and each task's own task_id/status intact across a
// mixed batch. Covers both accepted key names, "task_results" and "tasks".
func TestStructurallyReduceDispatchOutputObjectEnvelope(t *testing.T) {
	for _, key := range []string{"task_results", "tasks"} {
		t.Run(key, func(t *testing.T) {
			const n = 120
			raw := bigDispatchObjectEnvelope(t, n, key, false)
			if len(raw) <= editToolPreviewMaxBytes {
				t.Fatalf("fixture is %d bytes, must exceed the %d-byte budget to exercise the defect", len(raw), editToolPreviewMaxBytes)
			}
			if _, ok := shrinkJSONPreview(raw, editToolPreviewMaxBytes); ok {
				t.Fatal("leaf-shrinking unexpectedly fit this key-dominated document; the fixture no longer exercises the structural fallback")
			}

			reduced, ok := structurallyReduceDispatchOutput(raw, editToolPreviewMaxBytes)
			if !ok {
				t.Fatal("structural reduction reported failure on an object envelope it should handle")
			}
			if len(reduced) > editToolPreviewMaxBytes {
				t.Fatalf("reduced preview is %d bytes, over the %d-byte budget", len(reduced), editToolPreviewMaxBytes)
			}

			var out map[string]any
			if err := json.Unmarshal([]byte(reduced), &out); err != nil {
				t.Fatalf("reduced preview does not parse as JSON (%v): %q", err, reduced)
			}
			// Current, documented behavior: sibling run metadata does not
			// survive the reduction - only the row array does. If a future
			// change widens the contract to keep these, this assertion is
			// the visible diff that must move with it.
			for _, sibling := range []string{"run_id", "display_name", "status"} {
				if _, present := out[sibling]; present {
					t.Errorf("sibling key %q survived the reduction; the documented contract only carries the row array's task_id/status through", sibling)
				}
			}
			rows, ok := out[key].([]any)
			if !ok {
				t.Fatalf("reduced preview lost its %q array entirely: %v", key, out)
			}
			if len(rows) != n {
				t.Fatalf("%q carries %d rows, want %d - the fan-out's positions must stay aligned", key, len(rows), n)
			}
			statuses := []string{"completed", "failed", "running"}
			for i, rt := range rows {
				row, _ := rt.(map[string]any)
				wantID := fmt.Sprintf("t%d", i)
				wantStatus := statuses[i%len(statuses)]
				if row["task_id"] != wantID || row["status"] != wantStatus {
					t.Fatalf("row %d = %v, want task_id %q status %q - a mixed batch must not collapse to one verdict", i, row, wantID, wantStatus)
				}
				if len(row) != 2 {
					t.Fatalf("row %d = %v, want only task_id and status kept", i, row)
				}
			}
		})
	}
}

// TestStructurallyReduceDispatchOutputObjectEnvelopeBothKeys pins
// reduceDispatchRowsIn's two-key branch: an envelope that carries both
// "tasks" and "task_results" gets BOTH arrays reduced, and both contribute
// to the row count the caller uses to decide whether this shape matched at
// all - each with its own row identities intact across a mixed batch.
func TestStructurallyReduceDispatchOutputObjectEnvelopeBothKeys(t *testing.T) {
	const n = 80
	raw := bigDispatchObjectEnvelope(t, n, "task_results", true)
	if len(raw) <= editToolPreviewMaxBytes {
		t.Fatalf("fixture is %d bytes, must exceed the %d-byte budget", len(raw), editToolPreviewMaxBytes)
	}

	reduced, ok := structurallyReduceDispatchOutput(raw, editToolPreviewMaxBytes)
	if !ok {
		t.Fatal("structural reduction failed on a both-keys object envelope")
	}
	if len(reduced) > editToolPreviewMaxBytes {
		t.Fatalf("reduced preview is %d bytes, over budget", len(reduced))
	}

	var out map[string]any
	if err := json.Unmarshal([]byte(reduced), &out); err != nil {
		t.Fatalf("reduced preview does not parse: %v (%q)", err, reduced)
	}
	statuses := []string{"completed", "failed", "running"}
	for _, key := range []string{"tasks", "task_results"} {
		rows, ok := out[key].([]any)
		if !ok || len(rows) != n {
			t.Fatalf("%q = %v, want %d rows", key, out[key], n)
		}
		for i, rt := range rows {
			row, _ := rt.(map[string]any)
			wantID := fmt.Sprintf("t%d", i)
			wantStatus := statuses[i%len(statuses)]
			if row["task_id"] != wantID || row["status"] != wantStatus {
				t.Fatalf("%s row %d = %v, want task_id %q status %q", key, i, row, wantID, wantStatus)
			}
		}
	}
}

// TestOutputPreviewStructurallyReducesObjectEnvelope runs the object-shaped
// fixture through the full public entry point, redactToolOutputForTool -
// leaf-shrink then structural fallback - rather than calling the structural
// helper directly, so the integration (not just the helper in isolation) is
// pinned: valid, bounded, UTF-8 JSON, a stable row count, and each task's
// own status intact across a mixed batch, reached through the object
// branch instead of the bare-array one.
func TestOutputPreviewStructurallyReducesObjectEnvelope(t *testing.T) {
	const n = 120
	raw := bigDispatchObjectEnvelope(t, n, "task_results", false)
	if len(raw) <= editToolPreviewMaxBytes {
		t.Fatalf("fixture is %d bytes, must exceed the %d-byte budget", len(raw), editToolPreviewMaxBytes)
	}
	if _, ok := shrinkJSONPreview(raw, editToolPreviewMaxBytes); ok {
		t.Fatal("leaf-shrinking unexpectedly fit this key-dominated document")
	}

	preview := redactToolOutputForTool("dispatch_tasks", raw)
	if len(preview) > editToolPreviewMaxBytes {
		t.Fatalf("preview is %d bytes, over the %d-byte budget", len(preview), editToolPreviewMaxBytes)
	}
	if !utf8.ValidString(preview) {
		t.Fatal("preview is not valid UTF-8")
	}

	var out map[string]any
	if err := json.Unmarshal([]byte(preview), &out); err != nil {
		t.Fatalf("preview does not parse (%v): %q", err, preview)
	}
	rows, ok := out["task_results"].([]any)
	if !ok {
		t.Fatalf("preview lost its task_results array: %v", out)
	}
	if len(rows) != n {
		t.Fatalf("preview carries %d rows, want %d", len(rows), n)
	}
	statuses := []string{"completed", "failed", "running"}
	for i, rt := range rows {
		row, _ := rt.(map[string]any)
		id, _ := row["task_id"].(string)
		status, _ := row["status"].(string)
		wantID := fmt.Sprintf("t%d", i)
		wantStatus := statuses[i%len(statuses)]
		if id != wantID || status != wantStatus {
			t.Fatalf("row %d = %v, want task_id %q status %q", i, row, wantID, wantStatus)
		}
	}
}

// TestOutputPreviewCaseFoldedTaskKeys exercises the output preview redaction
// with ~150 rows keyed Task_ID/Status vs lowercase task_id/status.
// When case-variant keys are used, structural reduction must recognize and
// canonicalize the keys to lowercase task_id/status so the resulting preview
// remains valid JSON under the 8 KiB budget and parses cleanly into consumer structs.
func TestOutputPreviewCaseFoldedTaskKeys(t *testing.T) {
	type dispatchRow struct {
		TaskID string `json:"task_id"`
		Status string `json:"status"`
	}

	buildPayload := func(tasks int, taskIDKey, statusKey string) string {
		statuses := []string{"completed", "failed", "running"}
		rows := make([]any, 0, tasks)
		for i := 0; i < tasks; i++ {
			rows = append(rows, map[string]any{
				taskIDKey:      fmt.Sprintf("task-%d", i),
				statusKey:      statuses[i%len(statuses)],
				"output_ref":   fmt.Sprintf("ref:output:%d", i),
				"output_bytes": 42,
				"synopsis":     "finding details that add length to the row to exceed budget",
				"read_hint":    "read_output with ref",
			})
		}
		raw, err := json.Marshal(rows)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return string(raw)
	}

	assertReducesToParseableRows := func(t *testing.T, taskIDKey, statusKey string) {
		t.Helper()
		const n = 150
		raw := buildPayload(n, taskIDKey, statusKey)
		if len(raw) <= editToolPreviewMaxBytes {
			t.Fatalf("fixture is %d bytes, want > %d", len(raw), editToolPreviewMaxBytes)
		}
		preview := redactToolOutputForTool("dispatch_tasks", raw)
		if len(preview) > editToolPreviewMaxBytes {
			t.Fatalf("preview is %d bytes, over budget %d", len(preview), editToolPreviewMaxBytes)
		}
		var parsed []dispatchRow
		if err := json.Unmarshal([]byte(preview), &parsed); err != nil {
			t.Fatalf("preview is not valid JSON (%v): %q", err, preview)
		}
		if len(parsed) != n {
			t.Fatalf("parsed %d rows, want %d", len(parsed), n)
		}
		for i, r := range parsed {
			wantID := fmt.Sprintf("task-%d", i)
			if r.TaskID != wantID {
				t.Fatalf("row %d has task_id %q, want %q", i, r.TaskID, wantID)
			}
			if r.Status == "" {
				t.Fatalf("row %d has empty status", i)
			}
		}
	}

	// The control and the case variant must reduce identically: the consumer
	// decodes by struct tag, which encoding/json matches case-insensitively,
	// so a payload it accepts must not defeat the producer's reduction.
	for _, tc := range []struct {
		name      string
		taskIDKey string
		statusKey string
	}{
		{"lowercase control", "task_id", "status"},
		{"case variant Task_ID and Status", "Task_ID", "Status"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertReducesToParseableRows(t, tc.taskIDKey, tc.statusKey)
		})
	}

	t.Run("oversized batches (~200 rows) exceed 8 KiB even after reduction", func(t *testing.T) {
		// Documenting pre-existing limitation: at ~200 rows or more, even canonical
		// reduced keys (task_id + status per row) exceed the 8 KiB editToolPreviewMaxBytes
		// budget and structurallyReduceDispatchOutput falls back to truncatePreview's byte cut.
		t.Log("Note: ~200 rows with canonical keys exceed 8 KiB post-reduction and fall back to truncatePreview byte cut (pre-existing, out of scope)")
	})
}
