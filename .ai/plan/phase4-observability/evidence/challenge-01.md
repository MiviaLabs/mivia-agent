### Finding 1: ActiveHandles via repo status filter
- **Severity**: MEDIUM
- **What's wrong**: `ActiveHandles()` derives from `repo.ListRuns(ctx, status...)` to count non-terminal runs. But `LedgerRepository.ListRuns` signature is `ListRuns(ctx context.Context, status ...RunStatus) ([]RunSnapshot, error)` — it filters BY status (returns runs matching the given statuses). The plan says "filter by status" but the interface returns matching, not excluding. To get active runs, you need to list all runs and filter client-side, or pass all non-terminal statuses. This is feasible but should be explicit.
- **What should change**: Document the exact filtering logic in the plan (e.g., call ListRuns with RunStatusRunning, RunStatusQueued, RunStatusCreated and sum them).

### Finding 2: RED test can't compile against non-existent API
- **Severity**: HIGH
- **What's wrong**: t1 (Wave 1) writes a RED test for MetricsAdapter BEFORE the production code exists (t2, Wave 2). The test references `NewMetricsAdapter()` which doesn't exist yet. The RED phase requires the test to COMPILE and fail an ASSERTION — but it won't compile at all. This violates the ADLC TDD rule. The workaround: t1 and t2 must be in the SAME wave with t1 running first, OR t1 writes tests against types that exist via a minimal stub.
- **What should change**: Merge Wave 1 and Wave 2 into a single wave. t1 (RED test) runs first, then t2 (GREEN impl) runs second. The orchestrator should not advance to t2 until t1 has written the test (even if it doesn't compile yet — save the RED test as text). Then t2 creates the types, and t1's test becomes compilable and failable.

### Finding 3: Close() needs to unsubscribe from 17 event kinds
- **Severity**: MEDIUM
- **What's wrong**: `MetricsAdapter.Close()` must call `bus.Unsubscribe(kind, handler)` for each of the 17 event kinds. The Bus only supports unsubscribe by specific kind+handler pointer. Close() needs to iterate all 17 kinds. This is verbose but correct. The plan's API surface says "Close unsubscribes from the bus" without specifying how.
- **What should change**: Document that Close() iterates all known Kind constants and unsubscribes the adapter's HandleEvent from each. Or add a `bus.UnsubscribeAll(handler)` method to the Bus (but that's scope creep).

### Finding 4: Timing measures handler duration, not end-to-end latency
- **Severity**: LOW
- **What's wrong**: HandleEvent wraps the handler call with time.Now() before/after. But the Bus calls handlers INLINE in Publish's goroutine. The measured time is the handler processing time, not the end-to-end time from event creation to handler completion. For a sync bus, these are effectively the same — there's no queuing delay.
- **What should change**: No change needed. Document that timing is handler processing time, which equals end-to-end for a sync bus.

### Finding 5: No integration test for StorageLedgerRepository + Diagnostics
- **Severity**: LOW
- **What's wrong**: The plan says "no new integration test needed". But Diagnostics queries a LedgerRepository. If it's only tested with MemoryLedgerRepository (unit tests), we don't know if it works with StorageLedgerRepository (SQLite). The risk is low since Diagnostics just delegates to the repo interface.
- **What should change**: Add a simple integration test that creates a StorageLedgerRepository with SQLite :memory:, creates a run, and verifies Diagnostics sees it. But this can be deferred to a follow-up.

### Finding 6: Wave 3 test+prod parallel — same RED compilation problem
- **Severity**: MEDIUM
- **What's wrong**: t4 (Diagnostics test) writes tests against `NewDiagnostics()` which doesn't exist yet (t5). Same problem as Finding 2: test won't compile. The workaround is same — merge t4 and t5 into the same wave with t4 first, then t5, and the orchestrator doesn't attempt to compile t4's test until t5 has provided the types.
- **What should change**: Same as Finding 2: or allow the RED phase test to be saved as pending (not compiled yet) until the GREEN phase creates the types. Then both compile and the test fails on assertion.
