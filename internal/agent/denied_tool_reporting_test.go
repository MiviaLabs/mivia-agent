package agent

import (
	"strings"
	"testing"
)

// A refused tool call must not be reported to the operator as one that ran.
//
// The two halves of a denial used to disagree. The model was told "tool call
// denied by user"; every viewer was told the call completed. A denial returns
// from the approval wrapper without entering the dispatcher shim, so nothing
// recorded an outcome for the call, and the loop's no-outcome fallback emitted
// a tool_end reading "completed (duplicate)". Both status mappings read that
// as success - the NDJSON writer's toolEndStatus returns "ok" for it, and the
// TUI computes OK = true - so an operator could refuse a command and watch the
// transcript say it ran.

// TestADeniedCallIsRecordedAsFailedNotCompleted drives the recorder the
// approval wrapper calls, and asserts on the detail the loop's emitter derives
// from it - the same string every surface classifies on.
func TestADeniedCallIsRecordedAsFailedNotCompleted(t *testing.T) {
	var turn sdkTurnState
	turn.recordToolOutcome("call-1", "run_command", "tool call denied by user: denied", true)

	outcome := turn.takeToolCallOutcome("call-1")
	if outcome == nil {
		t.Fatal("the denial recorded no outcome, so the loop falls back to " +
			"\"completed (duplicate)\" and the refusal is reported as a success")
	}

	detail := sdkToolEndDetail(*outcome)
	if !strings.HasPrefix(detail, "failed") {
		t.Errorf("detail = %q; every surface classifies on this prefix, so a "+
			"refused call without it renders as a call that ran and succeeded",
			detail)
	}
	if strings.Contains(detail, "duplicate") {
		t.Errorf("detail = %q; a refused call is not a dedup-cache hit", detail)
	}
	if !strings.Contains(outcome.body, "denied") {
		t.Errorf("body = %q, want the refusal the model was given", outcome.body)
	}
}

// TestTheFallbackStillCoversARealDuplicate holds the behaviour the denial fix
// must not disturb: a call the dedup cache served really did produce a result
// the model saw, and must keep reading as completed rather than failed.
func TestTheFallbackStillCoversARealDuplicate(t *testing.T) {
	var turn sdkTurnState
	turn.recordToolOutcomeWithPreview("call-2", "read_file", "the notice", false, "", true, "the original body")

	outcome := turn.takeToolCallOutcome("call-2")
	if outcome == nil {
		t.Fatal("no outcome recorded for the duplicate")
	}
	if detail := sdkToolEndDetail(*outcome); strings.HasPrefix(detail, "failed") {
		t.Errorf("detail = %q; a dedup-served call is not a failure", detail)
	}
}
