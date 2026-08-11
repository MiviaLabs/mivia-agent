package ledger

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// TestListEventsAuditTrail: the ordered event log produces one bounded summary
// per event kind, ordered by sequence, without raw payload content.
func TestListEventsAuditTrail(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")

			stored, err := repo.GetRun(ctx, run)
			requireErr(t, err, nil, "GetRun")
			requireErr(t, repo.CompareAndSetRunStatus(ctx, run, stored.Version, RunStatusRunning, nil), nil, "CAS running")

			attempt := StepAttempt{
				AttemptID: "wfa-step1-1", RunID: run, StepID: "step1", AttemptNo: 1,
				Status: AttemptStatusRunning,
			}
			requireErr(t, repo.CreateStepAttempt(ctx, attempt), nil, "CreateStepAttempt")
			created, err := repo.GetStepAttempt(ctx, run, attempt.AttemptID)
			requireErr(t, err, nil, "GetStepAttempt")
			requireErr(t, repo.CompleteStepAttempt(ctx, run, attempt.AttemptID, created.Version, AttemptOutcome{
				Status: AttemptStatusSucceeded, ToStepID: "success", TransitionIndex: 2,
				MatchDigest: "sha256:abcdef123456", OutputRef: "sha256:feedface",
			}), nil, "CompleteStepAttempt")

			_, err = repo.IncrementLoopCounter(ctx, run, "repair")
			requireErr(t, err, nil, "IncrementLoopCounter")
			requireErr(t, repo.CreateApproval(ctx, ApprovalRecord{
				ApprovalID: "wfa-approval-gate-1", RunID: run, StepID: "gate", Status: "pending",
			}), nil, "CreateApproval")
			requireErr(t, repo.ResolveApproval(ctx, run, "wfa-approval-gate-1", "operator", "approved", ""), nil, "ResolveApproval")
			requireErr(t, repo.UpsertDelivery(ctx, DeliveryRecord{
				RunID: run, IdempotencyKey: "wfdel:key", Mode: "draft", BaseRef: "main",
				HeadRef: "wf/wt", Status: "pushed",
			}), nil, "UpsertDelivery")

			events, err := repo.ListEvents(ctx, run, 0, 0)
			requireErr(t, err, nil, "ListEvents")
			if len(events) < 8 {
				t.Fatalf("ListEvents = %d events, want at least 8", len(events))
			}
			seen := map[string]bool{}
			prev := -1
			for _, ev := range events {
				if ev.Sequence <= prev {
					t.Fatalf("events out of order: %d after %d", ev.Sequence, prev)
				}
				prev = ev.Sequence
				if ev.ID == "" || ev.Kind == "" || ev.Summary == "" {
					t.Fatalf("event record is incomplete: %+v", ev)
				}
				if len(ev.Summary) > MaxEventSummaryBytes {
					t.Fatalf("event summary exceeds %d bytes: %d", MaxEventSummaryBytes, len(ev.Summary))
				}
				seen[ev.Kind] = true
				switch ev.Kind {
				case eventKindRunCreated:
					if strings.Contains(ev.Summary, "\"steps\"") || strings.Contains(ev.Summary, "snapshot") {
						t.Fatalf("run_created summary leaks snapshot JSON: %q", ev.Summary)
					}
				case eventKindAttemptCompleted:
					if !strings.Contains(ev.Summary, "-> success") || !strings.Contains(ev.Summary, "sha256:") {
						t.Fatalf("attempt summary = %q, want route and refs", ev.Summary)
					}
				}
			}
			for _, kind := range []string{
				eventKindRunCreated, eventKindRunStatusChanged, eventKindAttemptStarted,
				eventKindAttemptCompleted, eventKindLoopIncremented, eventKindApprovalCreated,
				eventKindApprovalResolved, eventKindDeliveryUpserted,
			} {
				if !seen[kind] {
					t.Errorf("audit trail misses event kind %s", kind)
				}
			}
		})
	}
}

// TestListEventsSkipsUnknownKinds: a foreign event must not fail the listing;
// it is skipped like the projection tolerates it. (An undecodable payload of
// a KNOWN kind is store corruption and fails catch-up, so only the unknown
// kind is injectable.)
func TestListEventsSkipsUnknownKinds(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")

			events, err := repo.store.Events(ctx, run)
			requireErr(t, err, nil, "store.Events")
			seq := 0
			for _, ev := range events {
				if ev.Sequence > seq {
					seq = ev.Sequence
				}
			}
			requireErr(t, repo.store.Append(ctx, storage.Event{
				ID: "wfe:custom:1", RunID: run, Sequence: seq + 1,
				Kind: "wf_unknown_kind", Payload: []byte("{}"),
			}), nil, "Append unknown kind")

			listed, err := repo.ListEvents(ctx, run, 0, 0)
			requireErr(t, err, nil, "ListEvents")
			for _, ev := range listed {
				if ev.Kind == "wf_unknown_kind" {
					t.Fatalf("listing included a foreign event: %+v", ev)
				}
			}
		})
	}
}

// TestListEventsPaging: limit and offset page the audit trail.
func TestListEventsPaging(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")
			for i := 0; i < 6; i++ {
				stored, err := repo.GetRun(ctx, run)
				requireErr(t, err, nil, "GetRun")
				if stored.Status == RunStatusPending {
					requireErr(t, repo.CompareAndSetRunStatus(ctx, run, stored.Version, RunStatusRunning, nil), nil, "CAS running")
					continue
				}
				requireErr(t, repo.CompareAndSetRunStatus(ctx, run, stored.Version, RunStatusWaitingApproval, nil), nil, "CAS waiting")
				stored, err = repo.GetRun(ctx, run)
				requireErr(t, err, nil, "GetRun")
				requireErr(t, repo.CompareAndSetRunStatus(ctx, run, stored.Version, RunStatusRunning, nil), nil, "CAS back to running")
			}

			page1, err := repo.ListEvents(ctx, run, 4, 0)
			requireErr(t, err, nil, "ListEvents page 1")
			if len(page1) != 4 {
				t.Fatalf("page 1 = %d events, want 4", len(page1))
			}
			page2, err := repo.ListEvents(ctx, run, 4, 4)
			requireErr(t, err, nil, "ListEvents page 2")
			if len(page2) == 0 || page2[0].Sequence <= page1[3].Sequence {
				t.Fatalf("page 2 does not follow page 1: %d after %d", page2[0].Sequence, page1[3].Sequence)
			}
			total, err := repo.ListEvents(ctx, run, 0, 0)
			requireErr(t, err, nil, "ListEvents total")
			if want := 1 + 1 + 5*2; len(total) != want {
				t.Fatalf("total events = %d, want %d", len(total), want)
			}
		})
	}
}

// TestListEventsHugeLimitDoesNotOverflow: parseWorkflowIntFlag accepts
// --limit/--offset up to MaxInt64 with no upper bound, so start+limit can
// overflow int64 and wrap end negative, which used to slice decodable[start:end]
// with a negative end and panic. A huge limit/offset must return a clamped (or
// empty) page, never a slice-bounds panic.
func TestListEventsHugeLimitDoesNotOverflow(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")

			total, err := repo.ListEvents(ctx, run, 0, 0)
			requireErr(t, err, nil, "ListEvents total")

			// limit=MaxInt64 with offset=1: 1+MaxInt64 wraps negative, which
			// used to produce decodable[1:negative] and a slice-bounds panic.
			// The page must clamp to the remainder after offset 1.
			page, err := repo.ListEvents(ctx, run, math.MaxInt, 1)
			requireErr(t, err, nil, "ListEvents huge limit, offset 1")
			if len(page) != len(total)-1 {
				t.Fatalf("ListEvents(MaxInt, 1) = %d events, want %d (remainder after offset 1)", len(page), len(total)-1)
			}
			if len(page) > 0 && page[0].Sequence != total[1].Sequence {
				t.Fatalf("ListEvents(MaxInt, 1) starts at sequence %d, want %d", page[0].Sequence, total[1].Sequence)
			}

			// offset near MaxInt64 with a huge limit must clamp to an empty
			// page, not panic (start > len(decodable) -> empty).
			page, err = repo.ListEvents(ctx, run, math.MaxInt, math.MaxInt-1)
			requireErr(t, err, nil, "ListEvents huge limit, huge offset")
			if len(page) != 0 {
				t.Fatalf("ListEvents(MaxInt, MaxInt-1) = %d events, want 0 (offset past the trail)", len(page))
			}

			// limit=MaxInt64 with offset=0 must clamp to the full trail.
			page, err = repo.ListEvents(ctx, run, math.MaxInt, 0)
			requireErr(t, err, nil, "ListEvents huge limit, offset 0")
			if len(page) != len(total) {
				t.Fatalf("ListEvents(MaxInt, 0) = %d events, want %d (full trail clamped)", len(page), len(total))
			}
		})
	}
}

// TestListEventsPagesDecodableStream: paging counts DECODABLE events, so a
// page never comes back short while decodable events remain even when unknown
// foreign events are interleaved in the raw stream. This pins the
// filter-before-slice contract: offset pages over the decodable stream, not
// over raw events.
func TestListEventsPagesDecodableStream(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			runPagingCase(t, repo, ctx)
		})
	}
}

// runPagingCase drives the decodable-stream paging scenario for one backend:
// it interleaves 5 foreign wf_unknown_kind events with 10 decodable events
// (15 raw events), then asserts ListEvents pages them as 4 + 4 + 2 decodable
// events and that the paged concatenation matches the unpaged listing.
func runPagingCase(t *testing.T, repo *StorageRepository, ctx context.Context) {
	t.Helper()
	run := runID(t)
	snap, json := newRun(t, run)
	requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")
	seedInterleavedStream(t, repo, ctx, run)
	assertPagedDecodableStream(t, repo, ctx, run)
}

// seedInterleavedStream appends the 15-event raw stream (10 decodable
// run_status_changed events interleaved with 5 foreign wf_unknown_kind
// events) in sequence order: D U D D U D D D U D U D D U D.
func seedInterleavedStream(t *testing.T, repo *StorageRepository, ctx context.Context, run string) {
	t.Helper()
	// injectUnknown appends a foreign wf_unknown_kind event at the next
	// free sequence, interleaving it in sequence order on both backends.
	injectUnknown := func() {
		events, err := repo.store.Events(ctx, run)
		requireErr(t, err, nil, "store.Events before inject")
		seq := 0
		for _, ev := range events {
			if ev.Sequence > seq {
				seq = ev.Sequence
			}
		}
		requireErr(t, repo.store.Append(ctx, storage.Event{
			ID: fmt.Sprintf("wfe:foreign:%d", seq+1), RunID: run, Sequence: seq + 1,
			Kind: "wf_unknown_kind", Payload: []byte("{}"),
		}), nil, "Append unknown kind")
	}

	// oneStatusEvent appends exactly ONE run_status_changed event by
	// stepping the run through pending -> running -> waiting_approval ->
	// running, so the test controls the decodable count precisely.
	status := RunStatusPending
	oneStatusEvent := func() {
		stored, err := repo.GetRun(ctx, run)
		requireErr(t, err, nil, "GetRun")
		switch status {
		case RunStatusPending:
			requireErr(t, repo.CompareAndSetRunStatus(ctx, run, stored.Version, RunStatusRunning, nil), nil, "CAS pending->running")
			status = RunStatusRunning
		case RunStatusRunning:
			requireErr(t, repo.CompareAndSetRunStatus(ctx, run, stored.Version, RunStatusWaitingApproval, nil), nil, "CAS running->waiting")
			status = RunStatusWaitingApproval
		case RunStatusWaitingApproval:
			requireErr(t, repo.CompareAndSetRunStatus(ctx, run, stored.Version, RunStatusRunning, nil), nil, "CAS waiting->running")
			status = RunStatusRunning
		}
	}

	injectUnknown()
	oneStatusEvent() // D
	oneStatusEvent() // D
	injectUnknown()
	oneStatusEvent() // D
	oneStatusEvent() // D
	oneStatusEvent() // D
	injectUnknown()
	oneStatusEvent() // D
	injectUnknown()
	oneStatusEvent() // D
	oneStatusEvent() // D
	injectUnknown()
	oneStatusEvent() // D
}

// assertPagedDecodableStream asserts ListEvents pages the 10 decodable
// events as 4 + 4 + 2 and that the paged concatenation matches the unpaged
// listing exactly, in sequence order, with no foreign events.
func assertPagedDecodableStream(t *testing.T, repo *StorageRepository, ctx context.Context, run string) {
	t.Helper()
	page1, err := repo.ListEvents(ctx, run, 4, 0)
	requireErr(t, err, nil, "ListEvents page 1")
	if len(page1) != 4 {
		t.Fatalf("page 1 = %d decodable events, want 4 (short page while more decodable remain)", len(page1))
	}
	page2, err := repo.ListEvents(ctx, run, 4, 4)
	requireErr(t, err, nil, "ListEvents page 2")
	if len(page2) != 4 {
		t.Fatalf("page 2 = %d decodable events, want 4 (short page while more decodable remain)", len(page2))
	}
	page3, err := repo.ListEvents(ctx, run, 4, 8)
	requireErr(t, err, nil, "ListEvents page 3")
	if len(page3) != 2 {
		t.Fatalf("final page = %d decodable events, want 2 (10 decodable in pages of 4)", len(page3))
	}

	total, err := repo.ListEvents(ctx, run, 0, 0)
	requireErr(t, err, nil, "ListEvents total")
	if len(total) != 10 {
		t.Fatalf("total decodable events = %d, want 10", len(total))
	}

	all := append(append(append([]EventRecord{}, page1...), page2...), page3...)
	if len(all) != len(total) {
		t.Fatalf("paged concatenation = %d events, total = %d", len(all), len(total))
	}
	prev := -1
	for i, ev := range all {
		if ev.Kind == "wf_unknown_kind" {
			t.Fatalf("paged listing included a foreign event: %+v", ev)
		}
		if ev.Sequence <= prev {
			t.Fatalf("paged events out of order: %d after %d", ev.Sequence, prev)
		}
		prev = ev.Sequence
		if ev.Sequence != total[i].Sequence {
			t.Fatalf("paged concatenation diverges from the total listing at %d: %d != %d", i, ev.Sequence, total[i].Sequence)
		}
	}
}

// TestListEventsUnknownRun: an absent run is ErrNotFound.
func TestListEventsUnknownRun(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	if _, err := repo.ListEvents(ctx, "wfr-missing", 0, 0); err != ErrNotFound {
		t.Fatalf("ListEvents on a missing run = %v, want ErrNotFound", err)
	}
}

// TestListEventsBoundedDeliverySummary: a delivery record with a very long
// URL still yields a summary within the display bound.
func TestListEventsBoundedDeliverySummary(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	run := runID(t)
	snap, json := newRun(t, run)
	requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")
	longURL := "https://github.com/example/example/pull/" + strings.Repeat("x", 4096)
	requireErr(t, repo.UpsertDelivery(ctx, DeliveryRecord{
		RunID: run, IdempotencyKey: "wfdel:key", Mode: "draft", BaseRef: "main",
		HeadRef: "wf/wt", Status: "succeeded", URL: longURL,
	}), nil, "UpsertDelivery")
	events, err := repo.ListEvents(ctx, run, 0, 0)
	requireErr(t, err, nil, "ListEvents")
	var found bool
	for _, ev := range events {
		if ev.Kind == eventKindDeliveryUpserted {
			found = true
			if len(ev.Summary) > MaxEventSummaryBytes {
				t.Fatalf("delivery summary = %d bytes, want <= %d", len(ev.Summary), MaxEventSummaryBytes)
			}
		}
	}
	if !found {
		t.Fatal("delivery upsert event missing from the listing")
	}
}

// TestSummarizeEventAttemptPromptRefOnly: a wf_attempt_prompt event yields a
// bounded REF-ONLY summary carrying attempt_id and prompt_ref, and NEVER any
// prompt content — however large the body and however it is named in the
// payload (prompt, content, messages, ...), it must stay out of the summary.
func TestSummarizeEventAttemptPromptRefOnly(t *testing.T) {
	fakeBody := "top-secret-prompt-body-" + strings.Repeat("x", 4096)
	payload, err := json.Marshal(map[string]any{
		"attempt_id": "wfa-step1-1",
		"prompt_ref": "sha256:deadbeef",
		"prompt":     fakeBody,
		"content":    fakeBody,
		"messages":   []any{map[string]any{"role": "user", "content": fakeBody}},
	})
	requireErr(t, err, nil, "marshal attempt_prompt payload")

	record, ok := summarizeEvent(storage.Event{Kind: eventKindAttemptPrompt, Payload: payload})
	if !ok {
		t.Fatal("summarizeEvent(wf_attempt_prompt) = ok=false, want a summary")
	}
	if want := "attempt wfa-step1-1 prompt ref sha256:deadbeef"; record.Summary != want {
		t.Fatalf("attempt_prompt summary = %q, want %q (ref-only, no content)", record.Summary, want)
	}
	if strings.Contains(record.Summary, fakeBody) {
		t.Fatalf("attempt_prompt summary leaks the raw prompt body: %q", record.Summary)
	}
	if strings.Contains(record.Summary, `{"`) {
		t.Fatalf("attempt_prompt summary echoes raw payload JSON: %q", record.Summary)
	}
	if len(record.Summary) > MaxEventSummaryBytes {
		t.Fatalf("attempt_prompt summary = %d bytes, want <= %d", len(record.Summary), MaxEventSummaryBytes)
	}
}

// TestSummarizeEventUnknownKind: a foreign or unknown kind is not displayable:
// summarizeEvent must answer ok=false and never fabricate a summary. An
// undecodable payload of the NEW known kind is store corruption, not display.
func TestSummarizeEventUnknownKind(t *testing.T) {
	for _, kind := range []string{"wf_unknown_kind", "run_created", ""} {
		record, ok := summarizeEvent(storage.Event{Kind: kind, Payload: []byte(`{}`)})
		if ok {
			t.Fatalf("summarizeEvent(kind %q) = ok=true (%+v), want ok=false", kind, record)
		}
	}
	if _, ok := summarizeEvent(storage.Event{Kind: eventKindAttemptPrompt, Payload: []byte("not-json")}); ok {
		t.Fatal("summarizeEvent(wf_attempt_prompt, invalid payload) = ok=true, want ok=false")
	}
	if _, ok := summarizeEvent(storage.Event{Kind: eventKindAttemptPrompt, Payload: []byte(`{"attempt_id":"wfa-x"}`)}); ok {
		t.Fatal("summarizeEvent(wf_attempt_prompt, missing prompt_ref) = ok=true, want ok=false")
	}
}

// TestListEventsAttemptPromptRefOnly: a wf_attempt_prompt event lands in the
// audit trail with the same ref-only summary; the long prompt body stays out
// of the listing end to end.
func TestListEventsAttemptPromptRefOnly(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, snapJSON := newRun(t, run)
			requireErr(t, repo.CreateRun(ctx, snap, snapJSON), nil, "CreateRun")

			fakeBody := "secret-prompt-" + strings.Repeat("y", 4096)
			payload, err := json.Marshal(map[string]any{
				"attempt_id": "wfa-prompt-1",
				"prompt_ref": "sha256:feedface",
				"prompt":     fakeBody,
			})
			requireErr(t, err, nil, "marshal attempt_prompt payload")

			events, err := repo.store.Events(ctx, run)
			requireErr(t, err, nil, "store.Events")
			seq := 0
			for _, ev := range events {
				if ev.Sequence > seq {
					seq = ev.Sequence
				}
			}
			requireErr(t, repo.store.Append(ctx, storage.Event{
				ID: "wfe:attempt-prompt:1", RunID: run, Sequence: seq + 1,
				Kind: eventKindAttemptPrompt, Payload: payload,
			}), nil, "Append wf_attempt_prompt")

			listed, err := repo.ListEvents(ctx, run, 0, 0)
			requireErr(t, err, nil, "ListEvents")
			var found *EventRecord
			for i := range listed {
				if listed[i].Kind == eventKindAttemptPrompt {
					found = &listed[i]
				}
			}
			if found == nil {
				t.Fatal("wf_attempt_prompt event missing from the listing")
			}
			if want := "attempt wfa-prompt-1 prompt ref sha256:feedface"; found.Summary != want {
				t.Fatalf("attempt_prompt summary = %q, want %q (ref-only)", found.Summary, want)
			}
			if strings.Contains(found.Summary, fakeBody) {
				t.Fatalf("attempt_prompt summary leaks the prompt body: %q", found.Summary)
			}
			if len(found.Summary) > MaxEventSummaryBytes {
				t.Fatalf("attempt_prompt summary = %d bytes, want <= %d", len(found.Summary), MaxEventSummaryBytes)
			}
		})
	}
}

// TestListEventsAttemptExecutionSummary: a wf_attempt_execution event lands in
// the audit trail with a summary carrying the attempt id, the coordinator run
// id, the task id, and the transient-retry reason when one was recorded.
func TestListEventsAttemptExecutionSummary(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")
			requireErr(t, repo.CreateStepAttempt(ctx, StepAttempt{AttemptID: "att-1", RunID: run, StepID: "plan", AttemptNo: 1}), nil, "CreateStepAttempt")
			requireErr(t, repo.SetStepAttemptExecution(ctx, run, "att-1", "coord-retry", "task-retry", "provider overloaded: rate limit"), nil, "SetStepAttemptExecution")

			events, err := repo.ListEvents(ctx, run, 0, 0)
			requireErr(t, err, nil, "ListEvents")
			var found *EventRecord
			for i := range events {
				if events[i].Kind == eventKindAttemptExecution {
					found = &events[i]
				}
			}
			if found == nil {
				t.Fatal("wf_attempt_execution event missing from the listing")
			}
			for _, want := range []string{"att-1", "coord-retry", "task-retry", "provider overloaded: rate limit"} {
				if !strings.Contains(found.Summary, want) {
					t.Fatalf("attempt_execution summary = %q, want it to contain %q", found.Summary, want)
				}
			}
		})
	}
}

// TestListEventsPanelPhaseSetSummary: a wf_panel_phase_set event lands in the
// audit trail with a summary carrying the attempt id, the phase reached and
// the version.
func TestListEventsPanelPhaseSetSummary(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, raw := newRun(t, run)
			requireErr(t, repo.CreateRun(ctx, snap, raw), nil, "CreateRun")
			attempt := StepAttempt{AttemptID: "att-1", RunID: run, StepID: "review", AttemptNo: 1, PanelExecution: validPanelExecution(t, run, "att-1")}
			storePanelExecution(t, repo, attempt.PanelExecution)
			requireErr(t, repo.CreateStepAttempt(ctx, attempt), nil, "CreateStepAttempt")
			synthesis := &PanelSynthesisExecution{Work: validSynthesisTask(t, run, attempt.AttemptID)}
			storePanelTask(t, repo, synthesis.Work)
			requireErr(t, repo.ClaimRun(ctx, run, "holder"), nil, "ClaimRun")
			requireErr(t, repo.CompareAndSetPanelPhase(ContextWithClaimHolder(ctx, "holder"), run, attempt.AttemptID, 1, PanelPhaseMembersAdmitted, PanelPhaseSynthesisAdmitted, synthesis), nil, "CompareAndSetPanelPhase")

			events, err := repo.ListEvents(ctx, run, 0, 0)
			requireErr(t, err, nil, "ListEvents")
			var found *EventRecord
			for i := range events {
				if events[i].Kind == eventKindPanelPhaseSet {
					found = &events[i]
				}
			}
			if found == nil {
				t.Fatal("wf_panel_phase_set event missing from the listing")
			}
			if found.CreatedAt.IsZero() {
				t.Fatal("wf_panel_phase_set CreatedAt is the zero timestamp; 'mivia workflow events' must not print epoch")
			}
			if !found.CreatedAt.Equal(fixedClock) {
				t.Fatalf("wf_panel_phase_set CreatedAt = %v, want %v (clock-stamped from the payload)", found.CreatedAt, fixedClock)
			}
			for _, want := range []string{"att-1", string(PanelPhaseSynthesisAdmitted)} {
				if !strings.Contains(found.Summary, want) {
					t.Fatalf("panel_phase_set summary = %q, want it to contain %q", found.Summary, want)
				}
			}
		})
	}
}

// TestListEventsRunDeletedTombstone: the wf_run_deleted tombstone of a deleted
// run becomes listable when the same run ID is re-admitted (a tombstone always
// precedes a later run_created that reuses the idempotency key), so the audit
// trail shows the deletion and the reincarnation.
func TestListEventsRunDeletedTombstone(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")
			requireErr(t, repo.DeleteRun(ctx, run), nil, "DeleteRun")
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "re-CreateRun")

			events, err := repo.ListEvents(ctx, run, 0, 0)
			requireErr(t, err, nil, "ListEvents")
			var found *EventRecord
			for i := range events {
				if events[i].Kind == eventKindRunDeleted {
					found = &events[i]
				}
			}
			if found == nil {
				t.Fatal("wf_run_deleted tombstone missing from the reincarnated listing")
			}
			if !strings.Contains(found.Summary, run) {
				t.Fatalf("run_deleted summary = %q, want it to contain the run id %q", found.Summary, run)
			}
		})
	}
}

// TestListEventsRunResumedSummary: a wf_run_resumed event lands in the audit
// trail with a summary carrying the run id.
func TestListEventsRunResumedSummary(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")
			requireErr(t, repo.RecordRunResumed(ctx, run), nil, "RecordRunResumed")

			events, err := repo.ListEvents(ctx, run, 0, 0)
			requireErr(t, err, nil, "ListEvents")
			var found *EventRecord
			for i := range events {
				if events[i].Kind == eventKindRunResumed {
					found = &events[i]
				}
			}
			if found == nil {
				t.Fatal("wf_run_resumed event missing from the listing")
			}
			if !strings.Contains(found.Summary, run) {
				t.Fatalf("run_resumed summary = %q, want it to contain the run id %q", found.Summary, run)
			}
		})
	}
}

// TestListEventsAttemptHeartbeatSummary: a wf_attempt_heartbeat event lands in
// the audit trail with a bounded summary carrying the attempt id and the
// heartbeat instant, one event per tick.
func TestListEventsAttemptHeartbeatSummary(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, raw := newRun(t, run)
			requireErr(t, repo.CreateRun(ctx, snap, raw), nil, "CreateRun")
			requireErr(t, repo.CreateStepAttempt(ctx, StepAttempt{AttemptID: "att-hb", RunID: run, StepID: "plan", AttemptNo: 1}), nil, "CreateStepAttempt")
			requireErr(t, repo.SetStepAttemptHeartbeat(ctx, run, "att-hb", fixedClock), nil, "heartbeat 1")
			requireErr(t, repo.SetStepAttemptHeartbeat(ctx, run, "att-hb", fixedClock.Add(30*time.Second)), nil, "heartbeat 2")

			events, err := repo.ListEvents(ctx, run, 0, 0)
			requireErr(t, err, nil, "ListEvents")
			var heartbeats []EventRecord
			for _, ev := range events {
				if ev.Kind == eventKindAttemptHeartbeat {
					heartbeats = append(heartbeats, ev)
				}
			}
			if len(heartbeats) != 2 {
				t.Fatalf("wf_attempt_heartbeat events = %d, want 2", len(heartbeats))
			}
			for _, hb := range heartbeats {
				if !strings.Contains(hb.Summary, "att-hb") {
					t.Fatalf("heartbeat summary = %q, want it to contain the attempt id", hb.Summary)
				}
				if !strings.Contains(hb.Summary, "heartbeat at") {
					t.Fatalf("heartbeat summary = %q, want it to name the heartbeat instant", hb.Summary)
				}
				if len(hb.Summary) > MaxEventSummaryBytes {
					t.Fatalf("heartbeat summary = %d bytes, want <= %d", len(hb.Summary), MaxEventSummaryBytes)
				}
				if hb.CreatedAt.IsZero() {
					t.Fatal("wf_attempt_heartbeat CreatedAt is the zero timestamp")
				}
			}
			if heartbeats[0].Sequence >= heartbeats[1].Sequence {
				t.Fatalf("heartbeat sequences = %d then %d, want strictly increasing", heartbeats[0].Sequence, heartbeats[1].Sequence)
			}
		})
	}
}
