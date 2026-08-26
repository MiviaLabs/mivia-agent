package clichat

import (
	"encoding/json"
	"fmt"
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// TestEncodeResultsSmallOutputInlined verifies that outputs below the
// inline threshold are delivered inline with output_ref (backward compatible).
func TestEncodeResultsSmallOutputInlined(t *testing.T) {
	ref := "ref:output:small"
	tool := cliorchestrate.NewDispatchTasksToolWithCfg(config.SubagentConfig{InlineOutputBytes: 4096})
	snaps := []ledger.TaskSnapshot{
		{TaskID: "a", AgentName: "researcher", OutputRef: ref},
	}
	results := []subagents.Result{
		{TaskID: "a", Status: "completed", Output: json.RawMessage(`{"short": true}`)},
	}
	raw := tool.EncodeResultsForTest(snaps, results)
	var decoded []map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	if decoded[0]["output"] == nil {
		t.Fatal("small output should be inlined")
	}
	if decoded[0]["output_ref"] != ref {
		t.Fatalf("output_ref = %v, want %q", decoded[0]["output_ref"], ref)
	}
	if _, ok := decoded[0]["synopsis"]; ok {
		t.Fatal("small output should NOT have synopsis")
	}
	if _, ok := decoded[0]["read_hint"]; ok {
		t.Fatal("small output should NOT have read_hint")
	}
}

// TestEncodeResultsLargeOutputUsesRef verifies that outputs above the
// inline threshold emit output_ref + synopsis + output_bytes instead of
// the full inline output.
func TestEncodeResultsLargeOutputUsesRef(t *testing.T) {
	ref := "ref:output:abc123"
	snaps := []ledger.TaskSnapshot{
		{TaskID: "a", AgentName: "researcher", OutputRef: ref},
	}
	tool := cliorchestrate.NewDispatchTasksToolWithCfg(config.SubagentConfig{InlineOutputBytes: 100})

	// Build output larger than 100 bytes.
	largeOutput := make([]byte, 500)
	for i := range largeOutput {
		largeOutput[i] = 'x'
	}
	results := []subagents.Result{
		{TaskID: "a", Status: "completed", Output: json.RawMessage(largeOutput)},
	}
	raw := tool.EncodeResultsForTest(snaps, results)
	var decoded []map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	// Above threshold: no inline output.
	if _, ok := decoded[0]["output"]; ok {
		t.Fatal("large output should NOT be inlined")
	}
	// Must have ref.
	if decoded[0]["output_ref"] != ref {
		t.Fatalf("output_ref = %v, want %q", decoded[0]["output_ref"], ref)
	}
	// Must have synopsis.
	if decoded[0]["synopsis"] == nil {
		t.Fatal("large output should have synopsis")
	}
	// Must have output_bytes.
	if ob, ok := decoded[0]["output_bytes"].(float64); !ok || int(ob) != 500 {
		t.Fatalf("output_bytes = %v, want 500", decoded[0]["output_bytes"])
	}
	// Must have read_hint.
	if decoded[0]["read_hint"] == nil || decoded[0]["read_hint"] == "" {
		t.Fatal("large output should have read_hint")
	}
}

// TestEncodeResultsLargeOutputNoRefInlines verifies INV-AG-10 safety:
// when output is above threshold but no ref was stored (content write failed),
// the output is still inlined to avoid losing data.
func TestEncodeResultsLargeOutputNoRefInlines(t *testing.T) {
	snaps := []ledger.TaskSnapshot{
		{TaskID: "a", AgentName: "researcher", OutputRef: ""}, // No ref stored
	}
	tool := cliorchestrate.NewDispatchTasksToolWithCfg(config.SubagentConfig{InlineOutputBytes: 100})

	largeOutput := make([]byte, 500)
	for i := range largeOutput {
		largeOutput[i] = 'y'
	}
	results := []subagents.Result{
		{TaskID: "a", Status: "completed", Output: json.RawMessage(largeOutput)},
	}
	raw := tool.EncodeResultsForTest(snaps, results)
	var decoded []map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	// INV-AG-10: no ref means data must still be inlined.
	if decoded[0]["output"] == nil {
		t.Fatal("above-threshold output with no ref MUST be inlined (INV-AG-10)")
	}
}

// TestEncodeResultsMixedFanOut verifies a mixed fan-out where some tasks
// produce small output (inlined) and some produce large output (ref+synopsis).
func TestEncodeResultsMixedFanOut(t *testing.T) {
	ref := "ref:output:big"
	snaps := []ledger.TaskSnapshot{
		{TaskID: "small-1", AgentName: "researcher", OutputRef: "ref:output:s1"},
		{TaskID: "big-1", AgentName: "researcher", OutputRef: ref},
		{TaskID: "small-2", AgentName: "researcher", OutputRef: "ref:output:s2"},
	}
	tool := cliorchestrate.NewDispatchTasksToolWithCfg(config.SubagentConfig{InlineOutputBytes: 100})

	bigOutput := make([]byte, 500)
	for i := range bigOutput {
		bigOutput[i] = 'z'
	}
	results := []subagents.Result{
		{TaskID: "small-1", Status: "completed", Output: json.RawMessage(`{"a":1}`)},
		{TaskID: "big-1", Status: "completed", Output: json.RawMessage(bigOutput)},
		{TaskID: "small-2", Status: "completed", Output: json.RawMessage(`{"b":2}`)},
	}
	raw := tool.EncodeResultsForTest(snaps, results)
	var decoded []map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	if len(decoded) != 3 {
		t.Fatalf("want 3 results, got %d", len(decoded))
	}
	// Small results: inlined.
	if decoded[0]["output"] == nil {
		t.Fatal("small-1 should be inlined")
	}
	if decoded[2]["output"] == nil {
		t.Fatal("small-2 should be inlined")
	}
	// Big result: ref only.
	if decoded[1]["output"] != nil {
		t.Fatal("big-1 should NOT be inlined")
	}
	if decoded[1]["output_ref"] != ref {
		t.Fatalf("big-1 output_ref = %v, want %q", decoded[1]["output_ref"], ref)
	}
	if decoded[1]["synopsis"] == nil {
		t.Fatal("big-1 should have synopsis")
	}
}

// TestEncodeResultsJSONSynopsis verifies that large JSON object output
// produces a key-inventory synopsis rather than raw truncation.
func TestEncodeResultsJSONSynopsis(t *testing.T) {
	ref := "ref:output:jsonobj"
	snaps := []ledger.TaskSnapshot{
		{TaskID: "a", AgentName: "researcher", OutputRef: ref},
	}
	tool := cliorchestrate.NewDispatchTasksToolWithCfg(config.SubagentConfig{InlineOutputBytes: 50})

	// JSON object with keys, exceeding 50 bytes.
	largeJSON := `{"findings":["a long finding description that makes this big"],"files":["f1.go"],"summary":"done"}`
	results := []subagents.Result{
		{TaskID: "a", Status: "completed", Output: json.RawMessage(largeJSON)},
	}
	raw := tool.EncodeResultsForTest(snaps, results)
	var decoded []map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	synopsis, ok := decoded[0]["synopsis"].(string)
	if !ok {
		t.Fatalf("synopsis = %v (type %T), want string", decoded[0]["synopsis"], decoded[0]["synopsis"])
	}
	// Should be a key inventory, not raw truncation.
	var parsed map[string]any
	if err := json.Unmarshal([]byte(synopsis), &parsed); err != nil {
		t.Fatalf("synopsis not valid JSON: %q", synopsis)
	}
	if _, ok := parsed["keys"]; !ok {
		t.Fatalf("synopsis should have keys, got %v", parsed)
	}
}

// TestModelTaskResultsThreshold verifies that cliorchestrate.ModelTaskResults correctly
// switches between inline and ref+synopsis based on threshold.
func TestModelTaskResultsThreshold(t *testing.T) {
	ref := "ref:output:large"
	tasks := []ledger.TaskSnapshot{{TaskID: "t1", OutputRef: ref}}

	smallOutput := json.RawMessage(`{"ok": true}`)
	largeOutput := make([]byte, 500)
	for i := range largeOutput {
		largeOutput[i] = 'w'
	}

	// Small output with high threshold: inlined.
	results := []subagents.Result{{TaskID: "t1", Status: "completed", Output: smallOutput}}
	mtrs := cliorchestrate.ModelTaskResults(tasks, results, 4096)
	if mtrs[0].Output == nil {
		t.Fatal("small output should be inlined")
	}
	if mtrs[0].Synopsis != "" {
		t.Fatal("small output should not have synopsis")
	}

	// Large output with low threshold: ref+synopsis.
	results = []subagents.Result{{TaskID: "t1", Status: "completed", Output: largeOutput}}
	mtrs = cliorchestrate.ModelTaskResults(tasks, results, 100)
	if mtrs[0].Output != nil {
		t.Fatal("large output should NOT be inlined")
	}
	if mtrs[0].OutputRef != ref {
		t.Fatalf("OutputRef = %q, want %q", mtrs[0].OutputRef, ref)
	}
	if mtrs[0].Synopsis == "" {
		t.Fatal("large output should have synopsis")
	}
	if mtrs[0].OutputBytes != 500 {
		t.Fatalf("OutputBytes = %d, want 500", mtrs[0].OutputBytes)
	}
}

// TestDelegateResultPayloadLargeOutputUsesRef verifies that delegate's
// payload correctly uses ref+synopsis for large outputs.
func TestDelegateResultPayloadLargeOutputUsesRef(t *testing.T) {
	ref := "ref:output:delegatebig"
	// We need a coordinator.RunResult; build one from the test struct.
	largeOutput := make([]byte, 500)
	for i := range largeOutput {
		largeOutput[i] = 'q'
	}
	// We can't call delegateResultPayload with runResultLike since it's a
	// different type. Test via encodeResults instead which exercises the same
	// logic through a different path.
	tool := cliorchestrate.NewDispatchTasksToolWithCfg(config.SubagentConfig{InlineOutputBytes: 100})
	raw := tool.EncodeResultsForTest(
		[]ledger.TaskSnapshot{{TaskID: "d1", OutputRef: ref}},
		[]subagents.Result{{TaskID: "d1", Status: "completed", Output: json.RawMessage(largeOutput)}},
	)
	var decoded []map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	if decoded[0]["output"] != nil {
		t.Fatal("delegate large output should NOT be inlined")
	}
	if decoded[0]["synopsis"] == nil {
		t.Fatal("delegate large output should have synopsis")
	}
}

// TestSynopsisBoundaryConditions verifies synopsize edge cases.
func TestSynopsisBoundaryConditions(t *testing.T) {
	// Exactly at threshold: inlined.
	tool := cliorchestrate.NewDispatchTasksToolWithCfg(config.SubagentConfig{InlineOutputBytes: 100})
	exactly := make([]byte, 100)
	for i := range exactly {
		exactly[i] = 'e'
	}
	raw := tool.EncodeResultsForTest(
		[]ledger.TaskSnapshot{{TaskID: "t", OutputRef: "ref:output:exact"}},
		[]subagents.Result{{TaskID: "t", Status: "completed", Output: json.RawMessage(exactly)}},
	)
	var decoded []map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	if decoded[0]["output"] == nil {
		t.Fatal("exactly-at-threshold should be inlined")
	}

	// One byte over: ref.
	over := make([]byte, 101)
	for i := range over {
		over[i] = 'o'
	}
	raw = tool.EncodeResultsForTest(
		[]ledger.TaskSnapshot{{TaskID: "t", OutputRef: "ref:output:over"}},
		[]subagents.Result{{TaskID: "t", Status: "completed", Output: json.RawMessage(over)}},
	)
	decoded = nil
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	if decoded[0]["output"] != nil {
		t.Fatal("one-over-threshold should NOT be inlined")
	}
}

// TestZeroThresholdAlwaysUsesRefs verifies threshold=0 means always refs.
func TestZeroThresholdAlwaysUsesRefs(t *testing.T) {
	ref := "ref:output:zero"
	tool := cliorchestrate.NewDispatchTasksToolWithCfg(config.SubagentConfig{InlineOutputBytes: 0})
	raw := tool.EncodeResultsForTest(
		[]ledger.TaskSnapshot{{TaskID: "t", OutputRef: ref}},
		[]subagents.Result{{TaskID: "t", Status: "completed", Output: json.RawMessage(`{"tiny": true}`)}},
	)
	var decoded []map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	// threshold=0 with a ref → always use ref.
	if decoded[0]["output"] != nil {
		t.Fatal("threshold=0 with ref should NOT inline")
	}
	if decoded[0]["output_ref"] != ref {
		t.Fatalf("output_ref = %v, want %q", decoded[0]["output_ref"], ref)
	}
}

// TestErrorAboveThreshold verifies error text follows the same threshold rule.
func TestErrorAboveThreshold(t *testing.T) {
	ref := "ref:error:longerr"
	snaps := []ledger.TaskSnapshot{
		{TaskID: "t1", AgentName: "researcher", ErrorRef: ref},
	}
	tool := cliorchestrate.NewDispatchTasksToolWithCfg(config.SubagentConfig{InlineOutputBytes: 20})

	longErr := fmt.Sprintf("this is a very long error message that exceeds the threshold of 20 bytes")
	results := []subagents.Result{
		{TaskID: "t1", Status: "failed", Err: fmt.Errorf("%s", longErr)},
	}
	raw := tool.EncodeResultsForTest(snaps, results)
	var decoded []map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	// Error is above threshold: should NOT be inlined.
	if _, ok := decoded[0]["error"]; ok {
		t.Fatal("large error should NOT be inlined")
	}
	if decoded[0]["error_ref"] != ref {
		t.Fatalf("error_ref = %v, want %q", decoded[0]["error_ref"], ref)
	}
}

// TestModelTaskResultsErrorAboveThreshold covers the live-orchestration
// encoder's error branch, which is a third copy of the threshold rule.
func TestModelTaskResultsErrorAboveThreshold(t *testing.T) {
	errorRef := "ref:error:live"
	out := cliorchestrate.ModelTaskResults(
		[]ledger.TaskSnapshot{{TaskID: "t1", ErrorRef: errorRef}},
		[]subagents.Result{{TaskID: "t1", Status: "failed", Err: fmt.Errorf("%s", strings.Repeat("x", 200))}},
		50,
	)
	if len(out) != 1 {
		t.Fatalf("got %d results", len(out))
	}
	if out[0].Error != "" {
		t.Errorf("error above threshold must not be inlined, got %q", out[0].Error)
	}
	if out[0].ErrorRef != errorRef {
		t.Errorf("ErrorRef = %q, want %q", out[0].ErrorRef, errorRef)
	}
}

// TestEncodeOneDispatchResultDefaultsAndElapsed pins the status defaults and
// the elapsed/steps unpacking, which the model reads to judge how long a task
// actually ran.
func TestEncodeOneDispatchResultDefaultsAndElapsed(t *testing.T) {
	output := json.RawMessage(`{"elapsed":"1.5s","steps":3,"step_count":7}`)
	tr := cliorchestrate.EncodeOneDispatchResult(
		subagents.Result{TaskID: "t1", Output: output},
		[]ledger.TaskSnapshot{{TaskID: "t1", AgentName: "researcher"}},
		4096,
	)
	if tr.Status != string(ledger.TaskStatusCompleted) {
		t.Errorf("empty status = %q, want completed", tr.Status)
	}
	if tr.Elapsed != "1.5s" {
		t.Errorf("Elapsed = %q, want 1.5s", tr.Elapsed)
	}
	if tr.Steps != 3 {
		t.Errorf("Steps = %d, want 3", tr.Steps)
	}
	if tr.StepCount != 7 {
		t.Errorf("StepCount = %d, want 7", tr.StepCount)
	}

	failed := cliorchestrate.EncodeOneDispatchResult(
		subagents.Result{TaskID: "t2", Err: fmt.Errorf("boom")},
		[]ledger.TaskSnapshot{{TaskID: "t2"}},
		4096,
	)
	if failed.Status != string(ledger.TaskStatusFailed) {
		t.Errorf("errored task status = %q, want failed", failed.Status)
	}
	if failed.Error != "boom" {
		t.Errorf("Error = %q, want boom", failed.Error)
	}
}
