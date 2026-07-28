# Code Review: Subagent Orchestration & Extensibility Plan vs. Implementation

**Plan:** `.ai/plans/subagent-orchestration-extensibility.md` (Status: Validated draft / Phase 0)
**Review date:** 2026-07-28
**Source files audited:** `internal/ledger/*.go`, `internal/coordinator/*.go`, `internal/cli/orchestrate*.go`, `internal/cli/delegate.go`, `internal/cli/dispatch.go`, `internal/cli/dispatcher.go`, `internal/subagents/*.go`, `internal/config/types.go`, `mivia.toml.example`

---

## 1. IMPLEMENTED — matches the plan

| Plan requirement | Where implemented | Confidence |
|---|---|---|
| **LedgerRepository interface** (`CreateRun`, `GetRun`, `ListRuns`, `CreateTask`, `GetTask`, `ListTasks`, `AppendEvent`, `ListEvents`, `CompareAndSetTaskStatus`, `SetTaskOutput`, `SetTaskAttempt`, `CloseRun`, `DeleteRun`) | `internal/ledger/repository.go:17-81` | Full match |
| **RunSnapshot / TaskSnapshot / AttemptSnapshot / LifecycleEvent types** with `Clone()` methods for defensive copies | `internal/ledger/types.go:1-155` | Full match |
| **RunStatus vocabulary** (`created`, `queued`, `running`, `completed`, `failed`, `canceled`) | `internal/ledger/types.go:21-29` | Full match |
| **TaskStatus vocabulary** (`queued`, `running`, `completed`, `failed`, `timed_out`, `canceled`, `blocked`, `cancel_requested`, `retry_pending`) | `internal/ledger/types.go:33-44` | Full match (blocked and retry_pending added beyond plan) |
| **ValidTaskTransition / ValidRunTransitions** state machines | `internal/ledger/transition.go:1-62` | Full match |
| **MemoryLedgerRepository** — in-memory impl with RWMutex, defensive copies, fullSnapshot projection | `internal/ledger/memory.go:1-249` | Full match |
| **DisplayNameGenerator** — unique human-readable names, collision-safe, concurrent | `internal/ledger/displayname.go:1-53` | Full match |
| **StorageLedgerRepository** — wraps `storage.Store`, append-only events + in-memory projection, lazy rebuild on first access | `internal/ledger/storage.go:1-249` | Full match |
| **RebuildProjection** — deterministic event replay to reconstruct run state | `internal/ledger/storage_schema.go:124-226` | Full match |
| **Recover** — scans store for runs, marks non-terminal as `WasInterrupted` | `internal/ledger/storage.go:206-243` | Full match |
| **Coordinator** (Spawn/Inspect/Join/Cancel) — DAG validation, async execution, idempotency | `internal/coordinator/coordinator.go`, `cancel.go`, `record_results.go`, `handle_lifecycle.go` | Full match |
| **spawn_agent** tool — accepts tasks array with `id`, `name`, `depends_on`, `prompt`, `timeout_seconds`, `budget`; supports `idempotency_key`, `wait` (none/task/run), `wait_task_id` | `internal/cli/orchestrate.go:116-225` | Full match |
| **inspect_agents** tool — returns run snapshot with status, task states, output/error refs | `internal/cli/orchestrate.go:227-307` | Full match |
| **join_run** tool — blocks until run completes, returns final results with output/error refs | `internal/cli/orchestrate_lifecycle.go:33-121` | Full match |
| **cancel_run** tool — cancels via two-phase (cancel_requested → canceled), handles recovered runs | `internal/cli/orchestrate_lifecycle.go:126-193` | Full match |
| **delegate** compatibility adapter — routes through `runThroughCoordinator`, supports oneshot and multi_step | `internal/cli/delegate.go:1-131` | Full match |
| **dispatch_tasks** compatibility adapter — routes through coordinator, supports DAG, partial results | `internal/cli/dispatch.go:1-215` | Full match |
| **DAG scheduling** in coordinator — `runDAG` processes dependency-ready tasks in batches, marks blocked tasks | `internal/coordinator/coordinator.go:326-395` | Full match |
| **Partial results** — `RunWithPartial` and coordinator's `partial` flag | `internal/subagents/subagents.go:132-138` | Full match |
| **Heartbeat** for long-running multi_step agents (30s interval, elapsed+steps) | `internal/subagents/multi_step.go:240-259` | Full match |
| **Restricted registry** — delegation/orchestration tools removed from child agent registry | `internal/subagents/multi_step.go:272-283` | Full match |
| **Redacted references** — output/error refs use sha256 hashes, never raw content | `internal/cli/orchestrate.go:18-23`, `internal/coordinator/record_results.go:62-71` | Full match |
| **Context-based cancellation & timeout** — bounded workers, task-level and pool-level timeouts | `internal/subagents/subagents.go:93-123` | Full match |
| **Idempotency** — `idempotency_key` support on Spawn, dedup of existing runs, fingerprint conflict detection | `internal/coordinator/coordinator.go:129-162` | Full match |
| **Config types** — `StoreBackend`, `StorePath` fields in `SubagentConfig` | `internal/config/types.go:60-70` | Full match |
| **mivia.toml.example** — documents `store_backend` (memory/sqlite) and `store_path` | `mivia.toml.example:58-66` | Full match |
| **Benchmarks** — Memory and Storage ledger create-run and task-lifecycle benchmarks | `internal/ledger/bench_test.go:11-68` | Full match |
| **Tests for:** name uniqueness, DAG validation, status transitions, dependency blocking, idempotency, cancellation, timeout, redaction, concurrent safety, defensive copy isolation, projection rebuild, recovery semantics | `internal/ledger/ledger_test.go`, `storage_test.go`, `internal/coordinator/coordinator_test.go`, `integration_test.go`, `internal/cli/orchestrate_lifecycle_test.go` | Full match |

---

## 2. PARTIALLY IMPLEMENTED

| Plan requirement | Status | Details |
|---|---|---|
| **Retry classification & backoff** | ⚠️ States exist, no execution | `TaskStatusRetryPending` and valid transitions (`failed`→`retry_pending`→`queued`) exist in `transition.go`, but the Coordinator's `runDAG` never triggers retries — failed tasks go straight to `completed`/`failed`. No retry backoff, max-retries counter, or retry scheduler. |
| **Durable restart/resume from checkpoints** | ⚠️ Recovery marks, no resume | `StorageLedgerRepository.Recover()` correctly detects interrupted runs and sets `WasInterrupted`. However, the implementation explicitly refuses to resume running tasks: `cancelRecovered` returns error for `running`/`cancel_requested` tasks (cancel.go:145-148). Non-terminal queries `Join` via recovered handle fail closed. This is correct per Phase 1 scope but does NOT implement the Phase 3 goal of "Resume only from durable checkpoints/step boundaries that are proven safe." |
| **TUI / live views for orchestration runs** | ⚠️ Event forwarding exists, no dedicated run dashboard | `OnEvent` forwarding works for subagent heartbeat/step events (`dispatcher.go:118-152`), and `emitSubagentProgress` exists. But there is no dedicated TUI panel/view showing active runs, task DAGs, or cancellation controls — the plan's Phase 4 goal. |
| **Leases / heartbeat-lease semantics** | ⚠️ Deferred per plan | Heartbeats exist for multi_step visibility (30s ticker), but no lease-based ownership or orphan detection. The plan explicitly deferred leases to Phase 3 (multi-process scope). Correct for Phase 1. |
| **Storage backend as contract oracle** | ⚠️ Both impls exist, no formal equivalence test | Both `MemoryLedgerRepository` and `StorageLedgerRepository` implement the same interface, and the storage tests use memory store. The plan wanted to "align Memory and SQLite ordering/duplicate semantics before using Memory as a contract oracle" — this has not been formally proven. |
| **Stale-attempt fencing** | ⚠️ Partial | The plan wanted "a test proving an expired/stale in-process attempt cannot publish a terminal result after cancellation or replacement." The CAS version check (`CompareAndSetTaskStatus`) provides the mechanism, but there is no explicit test proving stale-attempt fencing after cancellation in the test suite. |

---

## 3. NOT IMPLEMENTED

| Plan requirement | Where it would go | Notes |
|---|---|---|
| **Retry backoff/scheduling** | `internal/coordinator/retry.go` (new) | No retry loop. The plan said "retry/backoff" and "retry classification" as Phase 1 scope items. Only the state types and valid transitions were implemented. |
| **Auto-retry of failed/timed_out tasks** | `internal/coordinator/coordinator.go` (runDAG) | `runDAG` never retries. Failed/timed-out tasks are marked terminal. |
| **OpenTelemetry metrics/traces** | New package or `internal/telemetry` | Phase 4 goal. No OTel exporter, context propagation, bounded metrics, or trace integration exists. |
| **Operator diagnostics / admin API** | `internal/cli` or new package | Phase 4 goal. No operator-facing diagnostics, metrics endpoint, or admin surface. |
| **Crash-recovery tests** | `internal/ledger/storage_test.go` | No kill/restart recovery tests proving exactly which tasks are resumed, skipped, retried, or blocked. The plan required this for Phase 3 gate. |
| **Process-farm / multi-process durability** | New package | Explicit non-goal for Phase 1. Remains not implemented. |
| **`coordinator` as an interface (not concrete struct)** | `internal/coordinator/coordinator.go` | Plan said "coordinator interface." Current implementation is a concrete struct `Coordinator`. Callers depend on the concrete type, not an interface. |
| **Retention/deletion policy config** | `internal/config/types.go` | Plan said "Define retention and deletion behavior before enabling persistent history." Currently hardcoded at 10 minutes (`orchestrationHandleRetention`). No configurable retention. |
| **Human security/privacy review gate** | Documentation/process | Plan required human security/privacy review for any ledger that stores workspace/user data. No evidence of this review in code. |

---

## 4. DEVIATIONS

| Deviation | Plan expectation | Actual implementation | Impact |
|---|---|---|---|
| **Package placement** | "new package under `internal/orchestration` or `internal/subagents`" | Implementation split across `internal/coordinator/`, `internal/ledger/`, and `internal/cli/` | Minor. The coordinator lives in its own package as intended, but the ledger types/repo are in a separate `internal/ledger/` package. Cleaner separation — no issue. |
| **Coordinator is a concrete struct, not interface** | Plan diagram shows "orchestration API → validated DAG + coordinator → bounded worker pool" implying an abstract boundary | `Coordinator` is a concrete struct. No `Coordinator` interface exists. Tests depend on concrete type. | Low risk for in-process use. Makes mocking in tests harder (tests must create real coordinators) but the code is testable. |
| **`runDAG` is a method on Coordinator, not a separate scheduler** | Plan implies DAG scheduler could be its own component | DAG scheduling is a method (`runDAG`) inside `coordinator.go`, interleaved with state transitions | Minor. Coupling is acceptable for Phase 1. Could be extracted later. |
| **Handle lifecycle management via `sync.Map` in CLI package** | Plan didn't specify handle storage | `runHandles` (sync.Map) + `orchestrationHandle` struct in `internal/cli/orchestrate.go`. Handles are scoped to `(dispatcher, repo)` tuple. | Reasonable. Cross-session handle protection is enforced (`orchestrationHandleAccessible`). |
| **`StorageLedgerRepository.CloseRun` writes two events** | Plan didn't specify | `CloseRun` writes a `run_closed` event AND a `run_status_changed` event with `RunStatusCanceled`. The `CloseRun` semantics conflate "close" with "cancel." | Minor semantic ambiguity. `CloseRun` means cancel+close. Could confuse operators reading the event stream. |
| **`DisplayNameGenerator` is global per Coordinator, not per-run** | Plan implies per-run name scoping | Names are unique per `Coordinator` instance (which is typically per-session). Multiple runs in the same session get sequentially unique names. | Acceptable for in-process. Names like `agent-1`, `agent-2` are unique per session, not globally. Plan's "human-readable unique names" requirement is met. |
| **`SetTaskAttempt` receives `finishedAt` for terminal status but no `startedAt`** | Plan didn't specify | Attempts record `StartedAt` at creation time, `FinishedAt` on terminal transition via `SetTaskAttempt`. | Good — matches event-sourcing pattern. |
| **Recovered non-terminal runs fail closed for both Join and Cancel (if running)** | Plan: "fail closed as `interrupted_unrecoverable`" | ✅ Aligned. `cancelRecovered` refuses to cancel running/recovering tasks. `Join` via recovered handle for non-terminal runs returns error. | Correct implementation of the plan's intent. |
| **No `Listener` pattern for run events** | Plan mentioned "TUI, future API" as downstream consumers | No pub/sub or listener pattern for `LifecycleEvent` delivery — only direct reads via `ListEvents`. | Acceptable for Phase 1. TUI integration would require adding a listener bus. |
| **`OneShotHandler` returns structured JSON with `output` + `task` keys** | Plan didn't specify handler return format | Returns `{"output": reply, "task": taskPrompt}` | Minor. The `delegate` tool then redacts the output to a `ref:output:...` hash in the response. |

---

## Summary

| Category | Count | Key items |
|---|---|---|
| **✅ IMPLEMENTED** | ~40+ | All Phase 1 scope: LedgerRepository interface + Memory impl + Storage impl, Coordinator with Spawn/Inspect/Join/Cancel, spawn_agent/inspect_agents/join_run/cancel_run tools, delegate/dispatch_tasks compatibility adapters, DisplayNameGenerator, DAG execution, state machine, event projection rebuild, recovery detection, idempotency, redacted refs, tests, benchmarks |
| **⚠️ PARTIALLY** | 6 | Retry scheduling (states only, no execution), durable checkpoint resume (recovery marks but refuses resume), TUI run dashboard (events forwarded, no dashboard), lease semantics (deferred by plan), storage oracle alignment (no formal equivalence proof), stale-attempt fencing test (CAS exists, test missing) |
| **❌ NOT IMPLEMENTED** | 9 | Retry backoff, auto-retry, OpenTelemetry metrics, operator diagnostics, crash-recovery tests, multi-process durability, Coordinator interface, configurable retention policy, security/privacy review gate |
| **🔄 DEVIATIONS** | 10 | All minor or neutral. Package placement, concrete struct vs interface, `runDAG` in coordinator, handle lifecycle in cli, CloseRun dual events, name scoping, attempt timestamps, recovered run semantics, no listener pattern, handler return format |

**Overall assessment:** The implementation delivers the full Phase 1 scope (identity, ledger, coordinator seams) plus significant Phase 2 scope (unified spawn, compatibility adapters). Phase 3 (durable persistence and recovery) has the StorageLedgerRepository backend and RebuildProjection but _explicitly refuses to resume running tasks_, which is the correct conservative stance for Phase 1. Phase 4 (observability, TUI, metrics) is largely not started. The plan's most important architectural goals — single `spawn_agent` boundary, async spawn/inspect/join/cancel, unique human-readable names, DAG execution, live queryable ledger, compatibility adapters — are all successfully implemented.
