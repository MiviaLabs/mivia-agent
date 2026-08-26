package cliorchestrate

import (
	"encoding/json"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// storeToolCallSteps marshals steps and stores them under a fresh ref,
// returning the ref for use as a TaskSnapshot.ToolCallsRef.
func storeToolCallSteps(t *testing.T, repo ledger.LedgerRepository, steps []subagents.ToolCallStep) string {
	t.Helper()
	data, err := json.Marshal(steps)
	if err != nil {
		t.Fatalf("marshal steps: %v", err)
	}
	ref := ledger.Reference(ledger.RefKindToolCalls, data)
	if err := repo.StoreContent(t.Context(), ref, data); err != nil {
		t.Fatalf("StoreContent: %v", err)
	}
	return ref
}

// TestEncodeResultsMergesToolCallsByID proves loadToolCallSummaries groups
// raw start/end steps by ToolCallID (not Name), correctly attributes
// Input/Output per call even when two calls share a Name, and marks only
// the call missing its "end" event Incomplete.
func TestEncodeResultsMergesToolCallsByID(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	steps := []subagents.ToolCallStep{
		{ToolCallID: "call-1", Name: "read_file", Kind: "start", Input: "path=a.go", At: time.Now()},
		{ToolCallID: "call-2", Name: "read_file", Kind: "start", Input: "path=b.go", At: time.Now()},
		{ToolCallID: "call-1", Name: "read_file", Kind: "end", Output: "contents-of-a", At: time.Now()},
		{ToolCallID: "call-2", Name: "read_file", Kind: "end", Output: "contents-of-b", At: time.Now()},
		{ToolCallID: "call-3", Name: "run_command", Kind: "start", Input: "ls", At: time.Now()},
	}
	ref := storeToolCallSteps(t, repo, steps)
	tasks := []ledger.TaskSnapshot{{TaskID: "t1", Status: "completed", ToolCallsRef: ref}}
	results := []subagents.Result{{TaskID: "t1", Status: "completed"}}

	tool := &dispatchTasksTool{repo: repo, cfg: config.SubagentConfig{InlineOutputBytes: 4096}}
	raw := tool.encodeResults(tasks, results)

	var decoded []dispatchTaskResult
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	if len(decoded) != 1 {
		t.Fatalf("decoded len = %d, want 1", len(decoded))
	}
	calls := decoded[0].ToolCalls
	if len(calls) != 3 {
		t.Fatalf("tool_calls len = %d, want 3: %+v", len(calls), calls)
	}

	byID := map[string]toolCallSummary{}
	for _, c := range calls {
		byID[c.ToolCallID] = c
	}

	c1, ok := byID["call-1"]
	if !ok {
		t.Fatal("call-1 missing")
	}
	if c1.Input != "path=a.go" || c1.Output != "contents-of-a" {
		t.Fatalf("call-1 = %+v, want Input=path=a.go Output=contents-of-a", c1)
	}
	if c1.Incomplete {
		t.Fatalf("call-1 Incomplete = true, want false (has end event)")
	}

	c2, ok := byID["call-2"]
	if !ok {
		t.Fatal("call-2 missing")
	}
	if c2.Input != "path=b.go" || c2.Output != "contents-of-b" {
		t.Fatalf("call-2 = %+v, want Input=path=b.go Output=contents-of-b", c2)
	}
	if c2.Incomplete {
		t.Fatalf("call-2 Incomplete = true, want false (has end event)")
	}

	c3, ok := byID["call-3"]
	if !ok {
		t.Fatal("call-3 missing")
	}
	if !c3.Incomplete {
		t.Fatal("call-3 Incomplete = false, want true (no end event)")
	}
	if c3.Output != "" {
		t.Fatalf("call-3 Output = %q, want empty (no end event)", c3.Output)
	}
}

// TestEncodeResultsCapsToolCallsToCompletePairs proves the
// envelopeMaxToolCallPairs cap applies AFTER merging: with 25 fully
// completed calls, the result carries exactly 20 rows, all complete -
// never a fragmented/spurious Incomplete row from a cap that split a pair.
func TestEncodeResultsCapsToolCallsToCompletePairs(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	var steps []subagents.ToolCallStep
	const totalCalls = 25
	for i := 0; i < totalCalls; i++ {
		id := "call-" + time.Now().Add(time.Duration(i)*time.Nanosecond).Format("150405.000000000") + "-" + string(rune('a'+i%26))
		steps = append(steps,
			subagents.ToolCallStep{ToolCallID: id, Name: "tool", Kind: "start", Input: "in"},
			subagents.ToolCallStep{ToolCallID: id, Name: "tool", Kind: "end", Output: "out"},
		)
	}
	ref := storeToolCallSteps(t, repo, steps)
	tasks := []ledger.TaskSnapshot{{TaskID: "t1", Status: "completed", ToolCallsRef: ref}}
	results := []subagents.Result{{TaskID: "t1", Status: "completed"}}

	tool := &dispatchTasksTool{repo: repo, cfg: config.SubagentConfig{InlineOutputBytes: 4096}}
	raw := tool.encodeResults(tasks, results)

	var decoded []dispatchTaskResult
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	calls := decoded[0].ToolCalls
	if len(calls) != envelopeMaxToolCallPairs {
		t.Fatalf("tool_calls len = %d, want %d (capped, merged-first)", len(calls), envelopeMaxToolCallPairs)
	}
	for _, c := range calls {
		if c.Incomplete {
			t.Fatalf("call %+v is Incomplete, want all-complete: the cap must never fragment a pair", c)
		}
	}
}

// TestLoadToolCallSummariesTruncatesAtRuneBoundary proves Input/Output are
// bounded to synopsisMaxBytes and the cut never lands mid-rune, exercised
// with a multi-byte rune sitting exactly across the boundary.
func TestLoadToolCallSummariesTruncatesAtRuneBoundary(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	// Build a string whose byte length comfortably exceeds synopsisMaxBytes,
	// with multi-byte runes ("€" = 3 bytes) placed near the cut boundary so
	// truncation must choose a boundary, not slice mid-rune.
	big := make([]byte, 0, synopsisMaxBytes+64)
	for len(big) < synopsisMaxBytes+64 {
		big = append(big, "€"...)
	}
	longInput := string(big)
	longOutput := string(big)

	steps := []subagents.ToolCallStep{
		{ToolCallID: "call-1", Name: "big_tool", Kind: "start", Input: longInput},
		{ToolCallID: "call-1", Name: "big_tool", Kind: "end", Output: longOutput},
	}
	ref := storeToolCallSteps(t, repo, steps)
	tasks := []ledger.TaskSnapshot{{TaskID: "t1", Status: "completed", ToolCallsRef: ref}}
	results := []subagents.Result{{TaskID: "t1", Status: "completed"}}

	tool := &dispatchTasksTool{repo: repo, cfg: config.SubagentConfig{InlineOutputBytes: 4096}}
	raw := tool.encodeResults(tasks, results)

	var decoded []dispatchTaskResult
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	if len(decoded[0].ToolCalls) != 1 {
		t.Fatalf("tool_calls len = %d, want 1", len(decoded[0].ToolCalls))
	}
	c := decoded[0].ToolCalls[0]
	if len(c.Input) > synopsisMaxBytes+len("…") {
		t.Fatalf("Input len = %d bytes, want <= %d", len(c.Input), synopsisMaxBytes+len("…"))
	}
	if len(c.Output) > synopsisMaxBytes+len("…") {
		t.Fatalf("Output len = %d bytes, want <= %d", len(c.Output), synopsisMaxBytes+len("…"))
	}
	if !json.Valid([]byte(raw)) {
		t.Fatal("encoded result is not valid JSON")
	}
	// UTF-8 validity is the real assertion: a mid-rune cut produces invalid UTF-8.
	if !utf8.ValidString(c.Input) {
		t.Fatalf("Input is not valid UTF-8 after truncation: %q", c.Input)
	}
	if !utf8.ValidString(c.Output) {
		t.Fatalf("Output is not valid UTF-8 after truncation: %q", c.Output)
	}
}
