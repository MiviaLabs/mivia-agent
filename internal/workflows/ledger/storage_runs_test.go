package ledger

import (
	"context"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// RecordRunResumed
// ---------------------------------------------------------------------------

// TestStorageRepository_RecordRunResumed covers the happy path: RecordRunResumed
// appends a wf_run_resumed audit event readable via ListEvents with a summary
// carrying the run id. It mutates no run state: the run snapshot stays pending.
func TestStorageRepository_RecordRunResumed(t *testing.T) {
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

			// The resume is purely observational: the run state is untouched.
			got, err := repo.GetRun(ctx, run)
			requireErr(t, err, nil, "GetRun")
			if got.Status != RunStatusPending || got.Version != 1 {
				t.Fatalf("run after resume = (%q, v%d), want (pending, v1) untouched", got.Status, got.Version)
			}
		})
	}
}

// TestStorageRepository_RecordRunResumedUnknownRun covers ErrNotFound for an
// absent run: no event is appended and no state is created.
func TestStorageRepository_RecordRunResumedUnknownRun(t *testing.T) {
	repo := newMemoryRepo(t)
	if err := repo.RecordRunResumed(context.Background(), "wfr-missing"); err != ErrNotFound {
		t.Fatalf("RecordRunResumed on a missing run = %v, want ErrNotFound", err)
	}
}

// TestStorageRepository_RecordRunResumedIdempotent covers the deterministic
// event ID under the REAL clock: recording a resume twice at different
// wall-clock times appends only one wf_run_resumed event, and both calls
// return nil (the second write is the idempotent retry path of appendEvent).
// The run_resumed payload carries ONLY the run id, so a retried resume
// marshals byte-identical bytes; a payload that stamped the resume instant
// would marshal differently and the retry would fail with ErrConflict.
func TestStorageRepository_RecordRunResumedIdempotent(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			// REAL clock: remove the deterministic pin (fixedClock) so the two
			// resume calls happen under the real wall clock. The payload carries
			// ONLY the run id, so no sleep is needed to prove idempotency: the
			// second call marshals byte-identical bytes regardless of time.
			repo.SetTimeSource(time.Now)
			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")
			requireErr(t, repo.RecordRunResumed(ctx, run), nil, "first resume")
			requireErr(t, repo.RecordRunResumed(ctx, run), nil, "second resume")

			events, err := repo.ListEvents(ctx, run, 0, 0)
			requireErr(t, err, nil, "ListEvents")
			n := 0
			for _, ev := range events {
				if ev.Kind == eventKindRunResumed {
					n++
				}
			}
			if n != 1 {
				t.Fatalf("ListEvents has %d wf_run_resumed events after an idempotent retry, want 1", n)
			}
		})
	}
}
