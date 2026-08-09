package ledger

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// ---------------------------------------------------------------------------
// Coverage for events_summary.go summarize* builders (undecodable payloads)
// ---------------------------------------------------------------------------

// TestCoverageSummarizeEventUndecodablePayloads drives every summarize*
// builder's error branch: an undecodable payload of a KNOWN kind must yield
// ok=false from summarizeEvent, never a fabricated summary. This exercises
// the `return "", time.Time{}, false` paths in events_summary.go.
func TestCoverageSummarizeEventUndecodablePayloads(t *testing.T) {
	kinds := []string{
		eventKindRunCreated,
		eventKindRunStatusChanged,
		eventKindAttemptStarted,
		eventKindAttemptCompleted,
		eventKindAttemptPrompt,
		eventKindAttemptExecution,
		eventKindPanelPhaseSet,
		eventKindLoopIncremented,
		eventKindApprovalCreated,
		eventKindApprovalResolved,
		eventKindDeliveryUpserted,
		eventKindRunDeleted,
		eventKindRunResumed,
	}
	for _, kind := range kinds {
		record, ok := summarizeEvent(storage.Event{Kind: kind, Payload: []byte("not-json")})
		if ok {
			t.Fatalf("summarizeEvent(kind %q, undecodable payload) = ok=true (%+v), want ok=false", kind, record)
		}
		if record != (EventRecord{}) {
			t.Fatalf("summarizeEvent(kind %q, undecodable payload) = %+v, want zero EventRecord", kind, record)
		}
	}
}

// ---------------------------------------------------------------------------
// Coverage for events_summary.go summarize* builders (decodable payloads)
// ---------------------------------------------------------------------------

// coveragePayload marshals a typed payload, failing the test on error.
func coveragePayload(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal coverage payload: %v", err)
	}
	return b
}

// summaryCase is one summarizeEvent expectation (shared by the two
// decodable-summary tests to keep each function under the size gate).
type summaryCase struct {
	name        string
	kind        string
	payload     []byte
	wantSummary string
	wantZeroAt  bool // expect a zero CreatedAt on the record
}

func runSummaryCase(t *testing.T, tc summaryCase) {
	t.Helper()
	t.Run(tc.name, func(t *testing.T) {
		ev := storage.Event{
			ID: "wfe:coverage:" + tc.kind, RunID: "wfr-1",
			Sequence: 7, Kind: tc.kind, Payload: tc.payload,
		}
		record, ok := summarizeEvent(ev)
		if !ok {
			t.Fatalf("summarizeEvent(kind %q, decodable payload) = ok=false, want a summary", tc.kind)
		}
		if record.Summary != tc.wantSummary {
			t.Fatalf("summary = %q, want %q", record.Summary, tc.wantSummary)
		}
		if record.ID != ev.ID || record.Kind != ev.Kind || record.Sequence != ev.Sequence {
			t.Fatalf("record identity = (%q, %q, %d), want (%q, %q, %d)",
				record.ID, record.Kind, record.Sequence, ev.ID, ev.Kind, ev.Sequence)
		}
		if tc.wantZeroAt {
			if !record.CreatedAt.IsZero() {
				t.Fatalf("run_resumed CreatedAt = %v, want zero (no payload timestamp)", record.CreatedAt)
			}
		} else if !record.CreatedAt.Equal(fixedClock) {
			t.Fatalf("CreatedAt = %v, want %v (payload clock)", record.CreatedAt, fixedClock)
		}
		if len(record.Summary) > MaxEventSummaryBytes {
			t.Fatalf("summary = %d bytes, want <= %d", len(record.Summary), MaxEventSummaryBytes)
		}
	})
}

// TestCoverageSummarizeEventRunAndWorkflowCases drives the run/workflow event
// summarize builders' success branches with decodable payloads.
func TestCoverageSummarizeEventRunAndWorkflowCases(t *testing.T) {
	cases := []summaryCase{
		{
			name: "run_created",
			kind: eventKindRunCreated,
			payload: coveragePayload(t, runCreatedPayload{
				Run:          RunSnapshot{RunID: "wfr-1", WorkflowName: "test-wf", WorkflowDigest: "sha256:abc"},
				SnapshotJSON: []byte(`{"steps":["leaked?"]}`),
				CreatedAt:    fixedClock,
			}),
			wantSummary: `run created: workflow "test-wf" digest sha256:abc`,
		},
		{
			name: "run_status_changed",
			kind: eventKindRunStatusChanged,
			payload: coveragePayload(t, runStatusChangedPayload{
				Status: RunStatusRunning, Version: 2, CreatedAt: fixedClock,
			}),
			wantSummary: "status changed: running (version 2)",
		},
		{
			name: "panel_phase_set",
			kind: eventKindPanelPhaseSet,
			payload: coveragePayload(t, panelPhasePayload{
				AttemptID: "wfa-1", Version: 1, Phase: PanelPhaseSynthesisAdmitted, CreatedAt: fixedClock,
			}),
			wantSummary: "panel phase set: attempt wfa-1 -> synthesis_admitted (version 1)",
		},
		{
			name: "loop_incremented",
			kind: eventKindLoopIncremented,
			payload: coveragePayload(t, loopIncrementedPayload{
				LoopName: "repair", Iterations: 3, CreatedAt: fixedClock,
			}),
			wantSummary: "loop incremented: repair -> 3",
		},
		{
			name: "delivery_upserted",
			kind: eventKindDeliveryUpserted,
			payload: coveragePayload(t, deliveryUpsertedPayload{
				Delivery: DeliveryRecord{
					RunID: "wfr-1", IdempotencyKey: "wfdel:key", Mode: "draft",
					BaseRef: "main", HeadRef: "wf/wt", Status: "succeeded",
				},
				CreatedAt: fixedClock,
			}),
			wantSummary: "delivery wfdel:key: succeeded (mode draft, base main)",
		},
		{
			name: "run_deleted",
			kind: eventKindRunDeleted,
			payload: coveragePayload(t, runDeletedPayload{
				RunID: "wfr-1", DeletedAt: fixedClock,
			}),
			wantSummary: "run deleted: wfr-1",
		},
		{
			name: "run_resumed",
			kind: eventKindRunResumed,
			payload: coveragePayload(t, runResumedPayload{
				RunID: "wfr-1",
			}),
			wantSummary: "run re-entered: wfr-1",
			wantZeroAt:  true,
		},
	}
	for _, tc := range cases {
		runSummaryCase(t, tc)
	}
}

// TestCoverageSummarizeEventAttemptAndApprovalCases drives the attempt and
// approval summarize builders, including the reason suffix on
// wf_approval_resolved (the reason-append path in events_summary.go) and its
// no-reason counterpart.
func TestCoverageSummarizeEventAttemptAndApprovalCases(t *testing.T) {
	cases := []summaryCase{
		{
			name: "attempt_started",
			kind: eventKindAttemptStarted,
			payload: coveragePayload(t, attemptStartedPayload{
				Attempt: StepAttempt{
					AttemptID: "wfa-1", RunID: "wfr-1", StepID: "plan",
					AttemptNo: 1, Status: AttemptStatusRunning,
				},
				CreatedAt: fixedClock,
			}),
			wantSummary: `attempt started: step "plan" attempt 1 (wfa-1)`,
		},
		{
			name: "attempt_completed",
			kind: eventKindAttemptCompleted,
			payload: coveragePayload(t, attemptCompletedPayload{
				AttemptID:       "wfa-1",
				Version:         1,
				Status:          AttemptStatusSucceeded,
				OutputRef:       "sha256:out",
				ToStepID:        "success",
				TransitionIndex: 2,
				MatchDigest:     "sha256:abc",
				CreatedAt:       fixedClock,
			}),
			wantSummary: "attempt completed: succeeded -> success (transition 2, match sha256:abc, output sha256:out)",
		},
		{
			name: "attempt_prompt",
			kind: eventKindAttemptPrompt,
			payload: coveragePayload(t, attemptPromptPayload{
				AttemptID: "wfa-1", PromptRef: "sha256:prompt", CreatedAt: fixedClock,
			}),
			wantSummary: "attempt wfa-1 prompt ref sha256:prompt",
		},
		{
			name: "attempt_execution_with_reason",
			kind: eventKindAttemptExecution,
			payload: coveragePayload(t, attemptExecutionPayload{
				AttemptID: "wfa-1", CoordinatorRunID: "run-1", TaskID: "task-1",
				Reason: "rate limited", CreatedAt: fixedClock,
			}),
			wantSummary: "attempt wfa-1 executed by coordinator run run-1 task task-1 reason: rate limited",
		},
		{
			name: "approval_created",
			kind: eventKindApprovalCreated,
			payload: coveragePayload(t, approvalCreatedPayload{
				Approval: ApprovalRecord{
					ApprovalID: "wfa-approval-1", RunID: "wfr-1", StepID: "gate", Status: "pending",
				},
				CreatedAt: fixedClock,
			}),
			wantSummary: `approval created: wfa-approval-1 (step "gate")`,
		},
		{
			name: "approval_resolved_with_reason",
			kind: eventKindApprovalResolved,
			payload: coveragePayload(t, approvalResolvedPayload{
				ApprovalID: "wfa-approval-1", Status: "approved", Actor: "operator",
				Reason: "looks good", CreatedAt: fixedClock,
			}),
			wantSummary: "approval resolved: wfa-approval-1 approved by operator reason: looks good",
		},
	}
	for _, tc := range cases {
		runSummaryCase(t, tc)
	}
}

// TestCoverageSummarizeEventApprovalResolvedNoReason covers the no-reason
// branch of wf_approval_resolved: the reason guard must keep the suffix OFF
// the summary when the payload has none.
func TestCoverageSummarizeEventApprovalResolvedNoReason(t *testing.T) {
	noReason, ok := summarizeEvent(storage.Event{
		Kind: eventKindApprovalResolved,
		Payload: coveragePayload(t, approvalResolvedPayload{
			ApprovalID: "wfa-approval-2", Status: "rejected", Actor: "operator", CreatedAt: fixedClock,
		}),
	})
	if !ok {
		t.Fatal("summarizeEvent(approval_resolved, no reason) = ok=false, want a summary")
	}
	if want := "approval resolved: wfa-approval-2 rejected by operator"; noReason.Summary != want {
		t.Fatalf("no-reason approval summary = %q, want %q", noReason.Summary, want)
	}
	if strings.Contains(noReason.Summary, " reason:") {
		t.Fatalf("no-reason approval summary carries a reason suffix: %q", noReason.Summary)
	}
}

// ---------------------------------------------------------------------------
// Coverage for truncateSummary (UTF-8 rune boundary)
// ---------------------------------------------------------------------------

// TestCoverageTruncateSummaryRuneBoundary exercises the rune-boundary loop in
// truncateSummary: a summary longer than MaxEventSummaryBytes whose byte at
// the cut index is the CONTINUATION byte of a multi-byte rune forces the loop
// to back off to a rune start, so the ellipsis never splits a rune.
func TestCoverageTruncateSummaryRuneBoundary(t *testing.T) {
	// 508 ASCII bytes + "é" (2 bytes at indices 508-509) + padding: byte 509 is
	// the continuation byte, so cut backs off from 509 to 508 (a rune start).
	in := strings.Repeat("a", 508) + "é" + strings.Repeat("b", 32)
	if len(in) <= MaxEventSummaryBytes {
		t.Fatalf("test input = %d bytes, want > %d", len(in), MaxEventSummaryBytes)
	}
	got := truncateSummary(in)
	if want := strings.Repeat("a", 508) + "…"; got != want {
		t.Fatalf("truncateSummary(rune-split input) = %q, want %q", got, want)
	}
	if len(got) > MaxEventSummaryBytes {
		t.Fatalf("truncated summary = %d bytes, want <= %d", len(got), MaxEventSummaryBytes)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncated summary is not valid UTF-8: %q", got)
	}

	// A cut that lands exactly on a rune start: the boundary loop does not run
	// and the summary is cut to exactly the bound (no rune to re-align).
	ascii := strings.Repeat("a", MaxEventSummaryBytes+10)
	if want := strings.Repeat("a", MaxEventSummaryBytes-3) + "…"; truncateSummary(ascii) != want {
		t.Fatalf("truncateSummary(ascii overflow) = %q, want %q", truncateSummary(ascii), want)
	}

	// Short input passes through unchanged.
	if got := truncateSummary("short"); got != "short" {
		t.Fatalf("truncateSummary(short) = %q, want unchanged", got)
	}
}

// ---------------------------------------------------------------------------
// Coverage for RecordRunResumed error path (storage_runs.go)
// ---------------------------------------------------------------------------

// TestCoverageRecordRunResumedClosedRepo covers the ensureBuilt error branch
// of RecordRunResumed: a closed repository must return ErrClosed before any
// run lookup or append happens.
func TestCoverageRecordRunResumedClosedRepo(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			requireErr(t, repo.Close(), nil, "Close")
			if err := repo.RecordRunResumed(ctx, "wfr-any-run"); err != ErrClosed {
				t.Fatalf("RecordRunResumed after Close = %v, want ErrClosed", err)
			}
		})
	}
}

// TestCoverageRecordRunResumedNotFound covers the ErrNotFound branch after the
// open check: an absent run is not resumed and no event is appended.
func TestCoverageRecordRunResumedNotFound(t *testing.T) {
	repo := newMemoryRepo(t)
	if err := repo.RecordRunResumed(context.Background(), "wfr-missing"); err != ErrNotFound {
		t.Fatalf("RecordRunResumed on a missing run = %v, want ErrNotFound", err)
	}
}
