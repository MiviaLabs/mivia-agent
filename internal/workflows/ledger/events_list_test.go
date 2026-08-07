package ledger

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"

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
