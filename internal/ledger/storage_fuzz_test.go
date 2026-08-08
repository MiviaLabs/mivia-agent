package ledger

// Deterministic hermetic fuzz target for the projection apply paths that decode
// untrusted store rows: RebuildProjection and the incremental catch-up of a
// fresh StorageLedgerRepository. Random rows (known and unknown kinds, random
// payload bytes, including empty, malformed, wrong-shape and oversized) must
// never panic or hang; malformed payloads must fail with a clean error rather
// than corrupting state. The rotation covers the run-level kinds too
// (run_closed and run_status_changed): run_closed decode is deliberately
// lenient (a malformed payload still closes via closeRebuiltRun), so a hostile
// row must never panic or wedge catch-up. The target is hermetic: no network,
// no filesystem, no wall-clock assertion, so time-stamping nondeterminism
// cannot flake it.

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

func FuzzLedgerProjectionApply(f *testing.F) {
	f.Add([]byte("run_created\x00\x7b\x7d"))
	f.Add([]byte(storageKindTaskStatusChanged + "\x00" + `{"task_id":"t1","status":"running","version":1}`))
	f.Add([]byte(storageKindTaskCreated + "\x00" + `{"RunID":"fuzz-run","TaskID":"t1"}`))
	f.Add([]byte(storageKindLifecycleEvent + "\x00" + string(bytes.Repeat([]byte("x"), 2048))))
	f.Add([]byte("unknown_kind\x00not-json"))
	f.Add([]byte(""))
	// run_closed and run_status_changed seeds: legacy '{}', the new shape with
	// status + completed_at, and a malformed payload. The run_closed decode is
	// the changed surface, so it gets direct seeds plus the rotation below.
	f.Add([]byte(storageKindRunClosed + "\x00" + `{"status":"canceled","completed_at":"2026-01-01T00:00:00Z"}`))
	f.Add([]byte(storageKindRunClosed + "\x00" + `{}`))
	f.Add([]byte(storageKindRunClosed + "\x00not-json"))
	f.Add([]byte(storageKindRunStatusChanged + "\x00" + `{"status":"canceled","completed_at":"2026-01-01T00:00:00Z"}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		segments := bytes.Split(data, []byte{0})
		store := storage.NewMemory()
		ctx := context.Background()
		const runID = "fuzz-run"

		// Seed a run_created row so the projection has a run to fold into.
		_ = store.Append(ctx, storage.Event{
			ID: "se-1", RunID: runID, Sequence: 1,
			Kind: storageKindRunCreated, Payload: []byte(`{"RunID":"fuzz-run","Status":"created"}`),
		})

		// Fold every NUL-delimited segment in as one row of a rotating kind,
		// including empty segments (the store refuses them, which is also a
		// valid clean path).
		seq := 2
		for i, seg := range segments {
			kind := storageKindLifecycleEvent
			switch i % 5 {
			case 0:
				kind = storageKindTaskStatusChanged
			case 1:
				kind = storageKindTaskCreated
			case 2:
				kind = storageKindRunClosed
			case 3:
				kind = storageKindRunStatusChanged
			}
			_ = store.Append(ctx, storage.Event{
				ID: fmt.Sprintf("se-%d", seq), RunID: runID, Sequence: seq, Kind: kind, Payload: seg,
			})
			seq++
		}

		events, err := store.Events(ctx, runID)
		if err != nil {
			t.Skipf("store read failed: %v", err)
		}
		// RebuildProjection must never panic on hostile rows; a malformed
		// payload is a clean rejection, not a crash.
		if _, _, _, err := RebuildProjection(events); err != nil {
			return
		}
		// A fresh repository catch-up over the same store must also never
		// panic or hang; errors are the documented clean rejections.
		repo := NewStorageLedgerRepository(store)
		_, _ = repo.ListRuns(ctx)
		_, _ = repo.ListTasks(ctx, runID)
	})
}
