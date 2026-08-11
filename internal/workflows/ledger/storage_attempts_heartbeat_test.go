package ledger

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestStorageRepository_SetStepAttemptHeartbeat pins the durable heartbeat
// contract: successive ticks append DISTINCT wf_attempt_heartbeat events (the
// event ID embeds the heartbeat timestamp, so a later tick never conflicts
// with an earlier one), the projection keeps the LATEST LastHeartbeatAt, a
// retried append of the same heartbeat is an idempotent no-op (nil, no extra
// event), an out-of-order heartbeat never regresses the timestamp, and the
// rebuild replay restores it from the event log.
func TestStorageRepository_SetStepAttemptHeartbeat(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")
			requireErr(t, repo.CreateStepAttempt(ctx, StepAttempt{AttemptID: "att-1", RunID: run, StepID: "plan", AttemptNo: 1}), nil, "create attempt")

			t1 := fixedClock
			t2 := fixedClock.Add(30 * time.Second)

			// Successive ticks: two DISTINCT events, never a conflict.
			requireErr(t, repo.SetStepAttemptHeartbeat(ctx, run, "att-1", t1), nil, "heartbeat t1")
			requireErr(t, repo.SetStepAttemptHeartbeat(ctx, run, "att-1", t2), nil, "heartbeat t2")

			// A retried append of the SAME heartbeat is idempotent: nil and
			// no extra event.
			requireErr(t, repo.SetStepAttemptHeartbeat(ctx, run, "att-1", t2), nil, "heartbeat t2 retry")

			// An out-of-order heartbeat must never regress the projection.
			requireErr(t, repo.SetStepAttemptHeartbeat(ctx, run, "att-1", t1.Add(5*time.Second)), nil, "heartbeat t1+5s (out of order)")

			// Unknown attempt / run -> ErrNotFound.
			requireErr(t, repo.SetStepAttemptHeartbeat(ctx, run, "att-missing", t1), ErrNotFound, "heartbeat unknown attempt")
			requireErr(t, repo.SetStepAttemptHeartbeat(ctx, "wfr-missing", "att-1", t1), ErrNotFound, "heartbeat unknown run")

			// Projection keeps the latest timestamp and never mutates status.
			got, err := repo.GetStepAttempt(ctx, run, "att-1")
			if err != nil {
				t.Fatalf("GetStepAttempt: %v", err)
			}
			if !got.LastHeartbeatAt.Equal(t2) {
				t.Fatalf("LastHeartbeatAt = %v, want %v (latest wins)", got.LastHeartbeatAt, t2)
			}
			if got.Status != AttemptStatusRunning {
				t.Fatalf("heartbeat mutated attempt status to %q, want running", got.Status)
			}
		})
	}
}

// TestStorageRepository_SetStepAttemptHeartbeatAuditTrail pins the durable
// audit trail: successive ticks append DISTINCT events with distinct IDs,
// same-timestamp retries dedupe, summaries name the attempt and stay bounded,
// and the rebuild replay restores the latest heartbeat from the event log.
func TestStorageRepository_SetStepAttemptHeartbeatAuditTrail(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")
			requireErr(t, repo.CreateStepAttempt(ctx, StepAttempt{AttemptID: "att-1", RunID: run, StepID: "plan", AttemptNo: 1}), nil, "create attempt")

			t1 := fixedClock
			t2 := fixedClock.Add(30 * time.Second)
			requireErr(t, repo.SetStepAttemptHeartbeat(ctx, run, "att-1", t1), nil, "heartbeat t1")
			requireErr(t, repo.SetStepAttemptHeartbeat(ctx, run, "att-1", t2), nil, "heartbeat t2")
			requireErr(t, repo.SetStepAttemptHeartbeat(ctx, run, "att-1", t2), nil, "heartbeat t2 retry")
			requireErr(t, repo.SetStepAttemptHeartbeat(ctx, run, "att-1", t1.Add(5*time.Second)), nil, "heartbeat t1+5s (out of order)")

			// The audit trail carries exactly THREE heartbeat events (t1, t2,
			// and the out-of-order t1+5s — each a distinct timestamp appends a
			// distinct event; only the same-timestamp retry dedupes), with
			// DISTINCT IDs in sequence order.
			events, err := repo.ListEvents(ctx, run, 0, 0)
			if err != nil {
				t.Fatalf("ListEvents: %v", err)
			}
			var heartbeats []EventRecord
			for _, ev := range events {
				if ev.Kind == eventKindAttemptHeartbeat {
					heartbeats = append(heartbeats, ev)
				}
			}
			if len(heartbeats) != 3 {
				t.Fatalf("ListEvents has %d wf_attempt_heartbeat events, want 3", len(heartbeats))
			}
			seen := map[string]bool{}
			for _, ev := range heartbeats {
				if seen[ev.ID] {
					t.Fatalf("heartbeat event ID %q appended twice", ev.ID)
				}
				seen[ev.ID] = true
			}
			if !strings.Contains(heartbeats[2].Summary, "att-1") {
				t.Fatalf("heartbeat summary = %q, want it to name the attempt", heartbeats[2].Summary)
			}
			if len(heartbeats[2].Summary) > MaxEventSummaryBytes {
				t.Fatalf("heartbeat summary = %d bytes, want <= %d", len(heartbeats[2].Summary), MaxEventSummaryBytes)
			}

			// Rebuild replay restores the latest LastHeartbeatAt.
			raw, err := repo.store.Events(ctx, run)
			if err != nil {
				t.Fatalf("store.Events: %v", err)
			}
			rebuilt, err := RebuildProjection(raw)
			if err != nil {
				t.Fatalf("RebuildProjection: %v", err)
			}
			var replayed *StepAttempt
			for i := range rebuilt.Attempts {
				if rebuilt.Attempts[i].AttemptID == "att-1" {
					replayed = &rebuilt.Attempts[i]
				}
			}
			if replayed == nil {
				t.Fatal("replayed projection lost the attempt")
			}
			if !replayed.LastHeartbeatAt.Equal(t2) {
				t.Fatalf("replayed LastHeartbeatAt = %v, want %v", replayed.LastHeartbeatAt, t2)
			}
		})
	}
}
