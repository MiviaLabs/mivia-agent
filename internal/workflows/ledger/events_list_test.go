package ledger

import (
	"context"
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
