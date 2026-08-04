package ledger

import (
	"context"
	"testing"
)

// ---------------------------------------------------------------------------
// 8. Loop counters
// ---------------------------------------------------------------------------

func TestStorageRepository_LoopCounters(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")

			// Successive increments mint 1, 2, 3.
			for want := 1; want <= 3; want++ {
				n, err := repo.IncrementLoopCounter(ctx, run, "main")
				if err != nil {
					t.Fatalf("IncrementLoopCounter: %v", err)
				}
				if n != want {
					t.Fatalf("IncrementLoopCounter = %d, want %d", n, want)
				}
			}

			counters, err := repo.GetLoopCounters(ctx, run)
			if err != nil {
				t.Fatalf("GetLoopCounters: %v", err)
			}
			if len(counters) != 1 {
				t.Fatalf("GetLoopCounters = %d entries, want 1", len(counters))
			}
			if counters[0].RunID != run || counters[0].LoopName != "main" || counters[0].Iterations != 3 {
				t.Fatalf("loop counter = %+v, want {run=%q main 3}", counters[0], run)
			}

			// A second loop name starts its own counter.
			n, err := repo.IncrementLoopCounter(ctx, run, "retry")
			if err != nil {
				t.Fatalf("IncrementLoopCounter(retry): %v", err)
			}
			if n != 1 {
				t.Fatalf("IncrementLoopCounter(retry) = %d, want 1", n)
			}
			counters, err = repo.GetLoopCounters(ctx, run)
			if err != nil {
				t.Fatalf("GetLoopCounters: %v", err)
			}
			if len(counters) != 2 {
				t.Fatalf("GetLoopCounters = %d entries, want 2", len(counters))
			}

			// Unknown run -> ErrNotFound.
			_, err = repo.IncrementLoopCounter(ctx, "wfr-unknown-"+run, "main")
			requireErr(t, err, ErrNotFound, "IncrementLoopCounter unknown run")
		})
	}
}

// ---------------------------------------------------------------------------
// 9. Approvals
// ---------------------------------------------------------------------------

func TestStorageRepository_Approvals(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")

			a1 := ApprovalRecord{ApprovalID: "ap-1", RunID: run, StepID: "review", Status: "pending", EvidenceJSON: []byte(`{}`)}
			a2 := ApprovalRecord{ApprovalID: "ap-2", RunID: run, StepID: "review", Status: "pending"}
			requireErr(t, repo.CreateApproval(ctx, a1), nil, "create approval 1")
			requireErr(t, repo.CreateApproval(ctx, a2), nil, "create approval 2")

			// Resolve ap-1: approved with actor and reason.
			requireErr(t, repo.ResolveApproval(ctx, run, "ap-1", "alice", "approved", "looks good"),
				nil, "resolve approval")

			list, err := repo.ListApprovals(ctx, run)
			if err != nil {
				t.Fatalf("ListApprovals: %v", err)
			}
			if len(list) != 2 {
				t.Fatalf("ListApprovals = %d records, want 2", len(list))
			}
			// Ordered by creation.
			if list[0].ApprovalID != "ap-1" || list[1].ApprovalID != "ap-2" {
				t.Fatalf("ListApprovals order = [%s, %s], want [ap-1, ap-2]",
					list[0].ApprovalID, list[1].ApprovalID)
			}

			res := list[0]
			if res.Status != "approved" {
				t.Fatalf("resolved approval Status = %q, want approved", res.Status)
			}
			if res.Actor != "alice" || res.Reason != "looks good" {
				t.Fatalf("resolved approval actor/reason = (%q, %q), want (alice, looks good)", res.Actor, res.Reason)
			}
			if res.ResolvedAt == nil {
				t.Fatalf("resolved approval ResolvedAt not stamped")
			}
			if !res.ResolvedAt.Equal(fixedClock) {
				t.Fatalf("resolved approval ResolvedAt = %v, want %v", *res.ResolvedAt, fixedClock)
			}
			if !res.CreatedAt.Equal(fixedClock) {
				t.Fatalf("approval CreatedAt = %v, want %v", res.CreatedAt, fixedClock)
			}

			pending := list[1]
			if pending.Status != "pending" {
				t.Fatalf("unresolved approval Status = %q, want pending", pending.Status)
			}
			if pending.ResolvedAt != nil {
				t.Fatalf("unresolved approval ResolvedAt = %v, want nil", *pending.ResolvedAt)
			}

			// Resolving again is an invalid transition.
			requireErr(t, repo.ResolveApproval(ctx, run, "ap-1", "bob", "rejected", "nope"),
				ErrInvalidTransition, "resolve already-resolved approval")

			// Resolving an unknown approval is not found.
			requireErr(t, repo.ResolveApproval(ctx, run, "ap-missing", "alice", "approved", ""),
				ErrNotFound, "resolve unknown approval")
		})
	}
}

// ---------------------------------------------------------------------------
// 10. Deliveries
// ---------------------------------------------------------------------------

func TestStorageRepository_Deliveries(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")

			d := DeliveryRecord{
				RunID:          run,
				IdempotencyKey: "dlv-1",
				Mode:           "pr",
				BaseRef:        "main",
				HeadRef:        "feature-x",
				Status:         "pending",
			}
			requireErr(t, repo.UpsertDelivery(ctx, d), nil, "upsert delivery")

			got, err := repo.GetDeliveryByIdempotencyKey(ctx, "dlv-1")
			if err != nil {
				t.Fatalf("GetDeliveryByIdempotencyKey: %v", err)
			}
			if got.RunID != run || got.IdempotencyKey != "dlv-1" || got.Mode != "pr" ||
				got.BaseRef != "main" || got.HeadRef != "feature-x" || got.Status != "pending" {
				t.Fatalf("delivery = %+v, want pending pr delivery", got)
			}

			// Re-upserting the same key replaces the record (upsert semantics).
			d.Status = "published"
			d.URL = "https://example.com/pr/1"
			requireErr(t, repo.UpsertDelivery(ctx, d), nil, "re-upsert delivery")

			got, err = repo.GetDeliveryByIdempotencyKey(ctx, "dlv-1")
			if err != nil {
				t.Fatalf("GetDeliveryByIdempotencyKey: %v", err)
			}
			if got.Status != "published" || got.URL != "https://example.com/pr/1" {
				t.Fatalf("re-upserted delivery = %+v, want published with URL", got)
			}

			list, err := repo.ListDeliveries(ctx, run)
			if err != nil {
				t.Fatalf("ListDeliveries: %v", err)
			}
			if len(list) != 1 {
				t.Fatalf("ListDeliveries = %d records, want 1 (upsert replaced)", len(list))
			}
			if list[0].IdempotencyKey != "dlv-1" {
				t.Fatalf("ListDeliveries[0].IdempotencyKey = %q, want dlv-1", list[0].IdempotencyKey)
			}

			// Unknown idempotency key -> ErrNotFound.
			_, err = repo.GetDeliveryByIdempotencyKey(ctx, "dlv-missing")
			requireErr(t, err, ErrNotFound, "GetDeliveryByIdempotencyKey unknown")
		})
	}
}

// ---------------------------------------------------------------------------
// 24. Delivery upsert is idempotent across repository instances (sqlite)
// ---------------------------------------------------------------------------

func TestStorageRepository_DeliveryUpsertIdempotent(t *testing.T) {
	ctx := context.Background()
	repoA, repoB, done := newSQLitePair(t)
	defer done()

	run := runID(t)
	snap, json := newRun(t, run)
	requireErr(t, repoA.CreateRun(ctx, snap, json), nil, "CreateRun via repoA")

	key := "dlv-idem-1"
	rec := DeliveryRecord{
		RunID:          run,
		IdempotencyKey: key,
		Mode:           "pr",
		BaseRef:        "main",
		HeadRef:        "feature-x",
		CommitSHA:      "abc123",
		Provider:       "github",
		RemoteID:       "remote-1",
		URL:            "https://example.com/pr/1",
		Status:         "pending",
	}
	requireErr(t, repoA.UpsertDelivery(ctx, rec), nil, "repoA upsert")

	countUpserts := func() int {
		t.Helper()
		events, err := repoA.store.Events(ctx, run)
		if err != nil {
			t.Fatalf("store.Events: %v", err)
		}
		n := 0
		for _, e := range events {
			if e.Kind != eventKindDeliveryUpserted {
				continue
			}
			p, err := unmarshalDeliveryUpserted(e.Payload)
			if err != nil {
				t.Fatalf("decode wf_delivery_upserted: %v", err)
			}
			if p.Delivery.IdempotencyKey == key {
				n++
			}
		}
		return n
	}

	// repoB retries the SAME record with the same key and the same clock:
	// the retry is ABSORBED — nil, and no duplicate wf_delivery_upserted.
	requireErr(t, repoB.UpsertDelivery(ctx, rec), nil, "repoB idempotent retry")
	if n := countUpserts(); n != 1 {
		t.Fatalf("wf_delivery_upserted events for key = %d, want exactly 1 after absorbed retry", n)
	}

	// A CHANGED caller field (Status pending->pushed) still records a new
	// event; the projection's latest-wins merge surfaces the pushed record.
	updated := rec
	updated.Status = "pushed"
	requireErr(t, repoB.UpsertDelivery(ctx, updated), nil, "repoB status-change upsert")
	if n := countUpserts(); n != 2 {
		t.Fatalf("wf_delivery_upserted events for key = %d, want exactly 2 after status change", n)
	}

	got, err := repoB.GetDeliveryByIdempotencyKey(ctx, key)
	if err != nil {
		t.Fatalf("GetDeliveryByIdempotencyKey: %v", err)
	}
	if got.Status != "pushed" {
		t.Fatalf("delivery Status = %q, want %q (latest wins)", got.Status, "pushed")
	}
}
