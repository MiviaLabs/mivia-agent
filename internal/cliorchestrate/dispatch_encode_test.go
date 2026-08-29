package cliorchestrate

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

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

// TestEncodeResultsEmitsToolCallsRef pins the report-plus-references
// contract: the envelope carries the task's recorded tool-call trace by
// reference only. tool_calls_ref is the snapshot's ToolCallsRef verbatim
// (INV-AG-10: read from the task record, never re-minted, so it resolves),
// no inline "tool_calls" array rides on the wire, and a task with no
// recorded trace emits no key at all.
func TestEncodeResultsEmitsToolCallsRef(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	steps := []subagents.ToolCallStep{
		{ToolCallID: "call-1", Name: "read_file", Kind: "start", Input: "path=a.go", At: time.Now()},
		{ToolCallID: "call-1", Name: "read_file", Kind: "end", Output: "contents-of-a", At: time.Now()},
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
	if decoded[0].ToolCallsRef != ref {
		t.Fatalf("tool_calls_ref = %q, want %q", decoded[0].ToolCallsRef, ref)
	}
	if strings.Contains(raw, `"tool_calls":`) {
		t.Fatalf("envelope still carries an inline tool_calls array: %s", raw)
	}
	// The ref handed to the model must resolve (INV-AG-10).
	if _, err := repo.LoadContent(t.Context(), decoded[0].ToolCallsRef); err != nil {
		t.Fatalf("tool_calls_ref does not resolve: %v", err)
	}

	// A task with no recorded trace emits no tool_calls_ref key.
	rawNone := tool.encodeResults(
		[]ledger.TaskSnapshot{{TaskID: "t2", Status: "completed"}},
		[]subagents.Result{{TaskID: "t2", Status: "completed"}},
	)
	if strings.Contains(rawNone, "tool_calls_ref") {
		t.Fatalf("ref-less task emits tool_calls_ref: %s", rawNone)
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
