package cliorchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
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

// completedToolCallSteps builds n distinct, fully completed (start+end)
// ToolCallStep pairs with unique ToolCallIDs, for use by the envelope-layer
// boundary tests below.
func completedToolCallSteps(n int) []subagents.ToolCallStep {
	steps := make([]subagents.ToolCallStep, 0, n*2)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("call-%04d", i)
		steps = append(steps,
			subagents.ToolCallStep{ToolCallID: id, Name: "tool", Kind: "start", Input: "in"},
			subagents.ToolCallStep{ToolCallID: id, Name: "tool", Kind: "end", Output: "out"},
		)
	}
	return steps
}

// TestLoadToolCallSummariesPairCountBoundaries covers the envelope cap's
// exact boundary values with GENUINELY COMPLETE calls: 0, 1,
// envelopeMaxToolCallPairs-1, envelopeMaxToolCallPairs, and
// envelopeMaxToolCallPairs+1. Chunk 5 already proved the cap engages (25
// calls -> 20 rows); this fills in the untested boundary points, in
// particular exactly-at-cap (must not drop or fabricate) and cap-1 (must
// not over-cap).
func TestLoadToolCallSummariesPairCountBoundaries(t *testing.T) {
	cases := []int{0, 1, envelopeMaxToolCallPairs - 1, envelopeMaxToolCallPairs, envelopeMaxToolCallPairs + 1}
	for _, n := range cases {
		t.Run(fmt.Sprintf("complete=%d", n), func(t *testing.T) {
			repo := ledger.NewMemoryLedgerRepository()
			ref := storeToolCallSteps(t, repo, completedToolCallSteps(n))
			got := loadToolCallSummaries(t.Context(), repo, ref)

			want := n
			if want > envelopeMaxToolCallPairs {
				want = envelopeMaxToolCallPairs
			}
			if len(got) != want {
				t.Fatalf("complete=%d: got %d summaries, want %d", n, len(got), want)
			}
			for _, c := range got {
				if c.Incomplete {
					t.Fatalf("complete=%d: summary %+v is Incomplete, want false", n, c)
				}
			}
		})
	}
}

// TestLoadToolCallSummariesNeverFragmentsPairAtCap is the required
// regression test for this session's round-2 architecture-review finding on
// the tool-call history envelope design: an earlier draft capped RAW
// (unmerged) ledger entries before merging start+end pairs into summaries,
// which could split a genuinely completed call across the cap boundary and
// fabricate a false Incomplete=true for what was actually a complete call
// whose "end" event simply fell past the raw cap. The accepted design
// (dispatch_encode.go's loadToolCallSummaries) merges FIRST, by ToolCallID,
// and only then truncates the merged list to envelopeMaxToolCallPairs - so
// capping can only ever drop whole trailing calls, never fragment one.
//
// This test constructs exactly envelopeMaxToolCallPairs+1 (21, given the
// current constant) genuinely, fully completed calls - each with both a
// start and an end event under a distinct ToolCallID - comfortably under
// the coordinator buffer layer's 200-raw-event cap (42 raw events here), so
// all 42 events survive to the ledger unmodified and this test exercises
// only the envelope layer's own capping logic. It asserts exactly
// envelopeMaxToolCallPairs summaries come back, and - the load-bearing
// assertion - every single one reports Incomplete=false. A regression to
// the rejected raw-capping design would either return a fragmented pair, or
// flip the last surfaced summary's Incomplete to true.
//
// loadToolCallSummaries is the single shared helper behind both dispatch-
// result producers (dispatchTaskResult here via encodeResults, and
// modelTaskResult in orchestrate_lifecycle.go via ModelTaskResultsWithRepo /
// RunTaskResultsWithRepo) - see dispatch_encode.go's toolCallSummary doc
// comment - so exercising it directly here is sufficient; it is not
// producer-specific logic that would need duplicating per producer.
func TestLoadToolCallSummariesNeverFragmentsPairAtCap(t *testing.T) {
	const totalCalls = envelopeMaxToolCallPairs + 1 // 21
	steps := completedToolCallSteps(totalCalls)
	if len(steps) != totalCalls*2 {
		t.Fatalf("setup: got %d raw steps, want %d (2 per call)", len(steps), totalCalls*2)
	}

	repo := ledger.NewMemoryLedgerRepository()
	ref := storeToolCallSteps(t, repo, steps)
	got := loadToolCallSummaries(t.Context(), repo, ref)

	if len(got) != envelopeMaxToolCallPairs {
		t.Fatalf("got %d summaries, want exactly %d (21st call dropped whole)", len(got), envelopeMaxToolCallPairs)
	}
	for i, c := range got {
		if c.Incomplete {
			t.Fatalf("summary[%d] = %+v is Incomplete=true; the cap must never fabricate a false incomplete by fragmenting a completed pair", i, c)
		}
		if c.Output == "" {
			t.Fatalf("summary[%d] = %+v has empty Output despite being a genuinely completed call", i, c)
		}
	}
}

// TestLoadToolCallSummariesPreservesGenuineIncompleteNearCap proves a
// REAL incomplete call (missing "end" event, e.g. from buffer-level
// truncation or a task cut off mid-call) sitting at the very last slot
// before the cap is preserved as Incomplete=true, and is not dropped,
// miscounted, or confused with a cap artifact. This distinguishes "a real
// incomplete call that happens to be near the boundary" (expected,
// meaningful signal) from "the cap fragmented a pair" (the bug chunk 8
// guards against above) - the two must never be conflated.
func TestLoadToolCallSummariesPreservesGenuineIncompleteNearCap(t *testing.T) {
	const completeCalls = envelopeMaxToolCallPairs - 1 // 19 complete...
	steps := completedToolCallSteps(completeCalls)
	// ...plus one genuinely incomplete call (start only, no end) appended
	// last, bringing the total to exactly envelopeMaxToolCallPairs (20).
	steps = append(steps, subagents.ToolCallStep{
		ToolCallID: "call-incomplete", Name: "tool", Kind: "start", Input: "in",
	})

	repo := ledger.NewMemoryLedgerRepository()
	ref := storeToolCallSteps(t, repo, steps)
	got := loadToolCallSummaries(t.Context(), repo, ref)

	if len(got) != envelopeMaxToolCallPairs {
		t.Fatalf("got %d summaries, want %d (all calls fit under the cap, none dropped)", len(got), envelopeMaxToolCallPairs)
	}
	last := got[len(got)-1]
	if last.ToolCallID != "call-incomplete" {
		t.Fatalf("last summary = %+v, want ToolCallID=call-incomplete (insertion order preserved)", last)
	}
	if !last.Incomplete {
		t.Fatal("last summary Incomplete = false, want true (genuinely missing its end event)")
	}
	for i, c := range got[:len(got)-1] {
		if c.Incomplete {
			t.Fatalf("summary[%d] = %+v is Incomplete=true, want false (a genuinely complete call)", i, c)
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

// TestTaskMessageIndexCarriesContentRef pins the envelope's resume path: a
// finding's synopsis entry must carry the event's content_ref so a reader can
// ledger_read the pinned full body WITHOUT the session-privileged
// run_messages tool - the dispatch/join envelope is the only message surface
// a dispatched task or an offline reader is guaranteed to see. Also pins the
// omitempty contract (legacy events without content_ref emit no key) and
// that non-finding/question kinds stay excluded.
func TestTaskMessageIndexCarriesContentRef(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	ctx := context.Background()
	runID := "run-cref"
	taskID := "t1"

	if err := repo.CreateRun(ctx, "", ledger.RunSnapshot{
		RunID: runID, Status: ledger.RunStatusRunning,
		Tasks: []ledger.TaskSnapshot{{RunID: runID, TaskID: taskID, Status: string(ledger.TaskStatusRunning)}},
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	ref := sdkadapter.Mint(sdkadapter.KindMessage, []byte(`{"kind":"finding","body":"full finding text"}`))
	if ref == "" {
		t.Fatal("expected non-empty content ref")
	}
	mustPayload := func(t *testing.T, p agentmsg.LifecyclePayload) []byte {
		t.Helper()
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		return raw
	}
	events := []ledger.LifecycleEvent{
		{
			ID: "evt-1", RunID: runID, Kind: coordinator.LifecycleKindTaskMessage, TaskID: taskID,
			Payload: mustPayload(t, agentmsg.LifecyclePayload{
				MessageID: "msg-1", Kind: agentmsg.KindFinding,
				Synopsis: "found the inversion", ContentRef: ref,
			}),
			CreatedAt: time.Now(),
		},
		{
			// Legacy event written before content_ref existed.
			ID: "evt-2", RunID: runID, Kind: coordinator.LifecycleKindTaskMessage, TaskID: taskID,
			Payload: mustPayload(t, agentmsg.LifecyclePayload{
				MessageID: "msg-2", Kind: agentmsg.KindQuestion, Synopsis: "pre-ref event",
			}),
			CreatedAt: time.Now(),
		},
		{
			// Answers are never envelope attachments.
			ID: "evt-3", RunID: runID, Kind: coordinator.LifecycleKindTaskMessage, TaskID: taskID,
			Payload: mustPayload(t, agentmsg.LifecyclePayload{
				MessageID: "msg-3", Kind: agentmsg.KindAnswer,
				Synopsis: "answer stays out", ContentRef: ref,
			}),
			CreatedAt: time.Now(),
		},
	}
	for _, evt := range events {
		if err := repo.AppendEvent(ctx, evt); err != nil {
			t.Fatalf("AppendEvent(%s): %v", evt.ID, err)
		}
	}

	idx := TaskMessageIndex(ctx, repo, []ledger.TaskSnapshot{{RunID: runID, TaskID: taskID}})
	msgs := idx[taskID]
	if len(msgs) != 2 {
		t.Fatalf("messages for %s = %+v, want 2 (finding + question; answer excluded)", taskID, msgs)
	}
	if msgs[0].MessageID != "msg-1" || msgs[0].ContentRef != ref {
		t.Fatalf("finding entry = %+v, want content_ref %q", msgs[0], ref)
	}
	if _, _, err := sdkadapter.Parse(msgs[0].ContentRef); err != nil {
		t.Fatalf("envelope content_ref %q does not parse: %v", msgs[0].ContentRef, err)
	}
	// omitempty: a legacy entry without content_ref must not emit the key.
	raw, err := json.Marshal(msgs[1])
	if err != nil {
		t.Fatalf("marshal legacy entry: %v", err)
	}
	if strings.Contains(string(raw), "content_ref") {
		t.Errorf("legacy entry emits content_ref: %s", raw)
	}
}
