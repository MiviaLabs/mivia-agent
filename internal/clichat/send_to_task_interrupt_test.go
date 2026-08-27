package clichat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
)

// TestSendToTaskInterruptValidation: the interrupt flag is a steer-only
// signal. kind="answer" + interrupt=true must be rejected before any message
// construction with an error mentioning interrupt/steer (and nothing durable
// may be persisted); kind="steer" + interrupt=true must be accepted, reach
// message construction, and carry the flag onto the durable message.
func TestSendToTaskInterruptValidation(t *testing.T) {
	run := spawnBroadcastRun(t,
		map[string]bool{"t-int-val": true}, "t-int-val")
	defer joinReleased(t, run)
	run.waitStarted(t, "t-int-val")

	// answer + interrupt=true → rejected, error mentions interrupt/steer.
	out, err := run.tool.Execute(run.ctx, json.RawMessage(`{
		"run_id":"`+run.runID+`",
		"task_id":"t-int-val",
		"kind":"answer",
		"body":"yes",
		"in_reply_to":"msg-q",
		"interrupt":true
	}`))
	rejection := ""
	if err != nil {
		rejection = err.Error()
	} else {
		rejection = out
	}
	if !strings.Contains(rejection, "interrupt") && !strings.Contains(rejection, "steer") {
		t.Fatalf("answer+interrupt must be rejected with an interrupt/steer error, out=%q err=%v", out, err)
	}
	// The rejection happens before message construction: no durable answer.
	list, err := run.coord.ListRunMessages(context.Background(), run.runID, "t-int-val")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range list {
		if m.Kind == agentmsg.KindAnswer {
			t.Fatalf("rejected answer must not be persisted: %+v", m)
		}
	}

	// steer + interrupt=true → accepted, reaches message construction.
	out, err = run.tool.Execute(run.ctx, json.RawMessage(`{
		"run_id":"`+run.runID+`",
		"task_id":"t-int-val",
		"kind":"steer",
		"body":"stop now",
		"interrupt":true
	}`))
	if err != nil {
		t.Fatalf("steer+interrupt execute: %v", err)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if res["status"] != "sent" || res["delivered"] != true {
		t.Fatalf("steer+interrupt result = %v, want status=sent delivered=true (out=%s)", res, out)
	}
	// The interrupt flag survives message construction into the durable store.
	assertDurableInterrupt(t, run, "t-int-val", "stop now")
}

// TestSendToTaskInterruptBroadcast: a broadcast steer with interrupt=true to
// two live tasks delivers to both, and each target's durable message carries
// Interrupt==true (observed via the ledger load path, ListRunMessages →
// LoadMessageBody).
func TestSendToTaskInterruptBroadcast(t *testing.T) {
	run := spawnBroadcastRun(t,
		map[string]bool{"t-int-1": true, "t-int-2": true}, "t-int-1", "t-int-2")
	defer joinReleased(t, run)
	// Deterministic (no time.Sleep): wait until both live handlers are running
	// so the broadcast provably reaches them while live.
	run.waitStarted(t, "t-int-1", "t-int-2")

	out, err := run.tool.Execute(run.ctx, json.RawMessage(`{
		"run_id":"`+run.runID+`",
		"task_ids":["t-int-1","t-int-2"],
		"kind":"steer",
		"body":"interrupt now",
		"interrupt":true
	}`))
	if err != nil {
		t.Fatalf("broadcast execute: %v", err)
	}
	var res struct {
		Status  string                    `json:"status"`
		Results map[string]map[string]any `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if res.Status != "sent" {
		t.Fatalf("status = %q, want sent", res.Status)
	}
	for _, id := range []string{"t-int-1", "t-int-2"} {
		entry, ok := res.Results[id]
		if !ok {
			t.Fatalf("missing result for %s in %s", id, out)
		}
		if entry["delivered"] != true {
			t.Fatalf("%s delivered = %v, want true (results=%s)", id, entry["delivered"], out)
		}
		if errStr, _ := entry["error"].(string); errStr != "" {
			t.Fatalf("%s unexpected error %q", id, errStr)
		}
		assertDurableInterrupt(t, run, id, "interrupt now")
	}
}

// assertDurableInterrupt finds the target's durable steer whose synopsis
// contains body and asserts the full loaded message envelope carries
// Interrupt==true. This is the same durable load path the existing broadcast
// test uses (ListRunMessages for the announcement, LoadMessageBody for the
// full envelope).
func assertDurableInterrupt(t *testing.T, run *broadcastRun, taskID, body string) {
	t.Helper()
	list, err := run.coord.ListRunMessages(context.Background(), run.runID, taskID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range list {
		if m.Kind == agentmsg.KindSteer && strings.Contains(m.Synopsis, body) {
			found = true
			if m.ContentRef == "" {
				t.Fatalf("%s durable steer has empty content_ref: %+v", taskID, m)
			}
			full, err := run.coord.LoadMessageBody(context.Background(), m.ContentRef)
			if err != nil {
				t.Fatalf("%s LoadMessageBody: %v", taskID, err)
			}
			if !full.Interrupt {
				t.Fatalf("%s durable message Interrupt = false, want true (full=%+v)", taskID, full)
			}
		}
	}
	if !found {
		t.Fatalf("no durable steer %q for %s: %+v", body, taskID, list)
	}
}
