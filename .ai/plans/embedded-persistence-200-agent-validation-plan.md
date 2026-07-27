# 200-agent embedded persistence validation plan

Status: Phase 4 decision report; production integration not authorized

Current phase: Phase 4 — measured no-go report with residual validation gaps

Last verified: 2026-07-27

Next action: Obtain approved workload/latency/durability thresholds and, if required, complete real power-loss and filesystem-quota validation before any production integration.

## Objective

Determine, with reproducible evidence, whether an embedded open-source Go store can support 200 logical Mivia agents performing concurrent reads and writes for durable sessions, agent/tool/subagent history, and future cache/context-pack metadata.

The result must be a measured capacity decision, not an assumption based on WAL or vendor benchmarks. The first candidate is SQLite with a single bounded writer path. Badger remains the pure-Go high-write fallback. The benchmark must be able to reject SQLite.

## Current source evidence

- `internal/chat/session.go`: `Session` owns in-memory `Messages`, a mutex, and last-writer-wins `turnID`; no storage interface exists.
- `internal/chat/persistence.go`: sessions are `meta.json` plus chunked JSONL under `.mivia/sessions`; save deletes/re-writes chunks and is not a cross-record transaction.
- `internal/agent/loop.go`: tool calls fan out into goroutines; current code does not establish a bounded persistence writer or a 200-agent capacity contract.
- `internal/chat/concurrency_test.go`: current tests cover concurrent filesystem save/load/list/delete, but not crash recovery, WAL growth, queue latency, disk pressure, or 200-agent workload behavior.
- `docs/architecture/embedded-persistence.md`: conditional SQLite recommendation and package/license comparison.
- `.ai/rules/50-concurrency-subagents.md`: in-process tasks, context cancellation, and bounded fan-out are required.

## Scope

In scope:

- A deterministic storage interface test harness.
- Concurrent logical-agent workload generation without 200 OS processes.
- Read/write contention, queue latency, throughput, tail latency, errors, WAL growth, checkpoint behavior, crash recovery, and disk-pressure tests.
- SQLite candidate validation. Pure-Go KV fallback comparison is explicitly deferred.
- Test-first acceptance criteria and a go/no-go report before production integration.

Out of scope:

- Production chat/session persistence integration during the validation phase.
- Context embeddings, vector search, model-quality evaluation, or API-cost measurement.
- Network-filesystem or multi-host SQLite support.
- Paid SQLite extensions or proprietary/commercial storage packages.
- Commit, push, or dependency publication before the validation gate is approved.

## Test-first contract

Write failing or contract-enforcing tests before adding the production store implementation. Tests must prove observable behavior and be deterministic; they must not reproduce the implementation's internal algorithm.

### Unit and contract tests

Create a storage test contract usable by each candidate backend:

- append events with stable IDs and per-run sequence;
- reject duplicate event IDs/idempotency keys without duplicate history;
- preserve ordering per run and parent/child relationships;
- atomically append an event and update the run/session projection;
- rollback all changes on injected transaction failure;
- read concurrently without data races;
- enforce payload-size, retention-class, and redaction policies;
- expire and delete cache/artifact records;
- reopen after clean close and recover after an interrupted write;
- return bounded, typed errors for contention, disk-full, corruption, and cancellation.

### Concurrency workload tests

Use logical agents as goroutines/tasks with a fixed seed and a bounded scheduler. Test at least:

- 1, 10, 50, 100, and 200 logical agents;
- reader-only, writer-only, and mixed read/write ratios;
- short event writes, batched turn writes, and large artifact references;
- duplicate/retry/late completion behavior;
- cancellation and backpressure when the writer queue is full.

Record operations completed, errors by class, queue depth, enqueue-to-commit latency, p50/p95/p99 commit latency, read latency, and fairness/starvation indicators.

### Crash-recovery tests

Run the workload in a child process or controlled test binary. Inject termination at deterministic lifecycle points: before enqueue, after enqueue, during transaction, after commit, during checkpoint, and during artifact publication. Reopen the store and assert:

- no partial committed transaction is visible;
- committed events remain queryable;
- duplicate retries are idempotent;
- started-but-not-terminal work is classified as unknown/abandoned;
- projections can be rebuilt from immutable events;
- database integrity checks pass.

Do not claim power-loss durability from process-kill tests alone; mark real power-loss testing as an environment-dependent gap.

### WAL and disk-pressure tests

- Measure WAL size and checkpoint duration at fixed intervals.
- Hold controlled long readers to prove checkpoint starvation is observable and bounded by policy.
- Test normal checkpointing and manual checkpoint recovery.
- Fill a test filesystem or quota-limited directory until writes fail.
- Assert typed failure, no silent data loss, no infinite retry loop, and recovery after space is restored.
- Test backup/restore while reads and writes are active; keep the WAL and database state together.

## Candidate matrix

### SQLite

Primary candidate for structured system-of-record data. Validate SQLite version availability in the selected Go driver, requiring the 2026 WAL-reset fix (SQLite 3.51.3 or later, or an equivalent patched bundle). Measure `synchronous=FULL` and the explicitly accepted lower-durability mode separately.

### Badger

Pure-Go high-write fallback. Run the same event workload, but map relational projections and idempotency indexes explicitly. Include value-log garbage collection, disk growth, reopen/recovery, and write-amplification measurements.

### bbolt and Pebble

Run only a bounded comparison suite if time permits. bbolt is a one-writer KV store; Pebble is a storage engine without general application transactions. They are not expected to win as the authoritative structured store, but may inform a future cache design.

## Phase gates

### Phase 0 — Baseline and harness

Read first: `AGENTS.md`, `.ai/rules/50-concurrency-subagents.md`, `internal/chat/concurrency_test.go`, `docs/architecture/embedded-persistence.md`.

Expected changes: new test/benchmark files only, preferably under a new storage package test surface; no production dependency yet.

Acceptance:

- Fixed-seed workload is reproducible.
- Tests can run against an in-memory fake and a temporary filesystem.
- Metrics and artifacts contain no raw prompts, secrets, PII, or full model payloads.

Stop if: the harness requires production storage code, creates unbounded goroutines, or persists sensitive fixture content.

### Phase 1 — SQLite contract and contention tests

Expected changes: SQLite test adapter, schema fixture, contract tests, benchmarks.

Acceptance:

- All contract tests pass.
- 200-agent mixed workload completes without corruption, duplicate events, data races, or unbounded queue growth.
- p99 enqueue-to-commit latency and error rate meet thresholds approved before running the benchmark.

Stop if: writes require unbounded retries, readers starve, WAL grows without bounded recovery, or integrity checks fail.

### Phase 2 — Crash, WAL, and disk-pressure validation

Acceptance:

- Every injected interruption point has deterministic recovery expectations.
- WAL checkpoint and recovery behavior is recorded.
- Disk-full behavior is explicit and recoverable.
- Backup/restore is consistent.

Stop if: committed data is lost outside the documented durability mode, recovery needs manual file surgery, or the test cannot distinguish committed from abandoned work.

### Phase 3 — Badger fallback comparison (deferred)

Skipped by explicit scope decision. Do not add Badger dependencies or run a fallback comparison in this validation cycle. Reopen this phase only if SQLite is rejected and a pure-Go KV fallback is separately authorized.

### Phase 4 — Decision report and implementation authorization

Produce one measured report containing hardware/OS, Go version, driver/package versions, schema/configuration, workload distribution, raw metric summaries, failures, and exact commands. Mark unrun tests `NOT_RUN`.

Human approval required before production implementation:

- selected backend and driver/package versions;
- acceptable p95/p99 latency and error thresholds;
- durability mode (`FULL` versus lower durability);
- retention, deletion, redaction, and artifact-size policy;
- maximum logical agents and writer queue capacity;
- rollback and operational backup procedure.

## Verification commands

Run in order:

```text
go test ./path/to/storage/... -count=1
go test -race ./path/to/storage/... -count=1
go test ./path/to/storage/... -run 'Crash|Recovery|Disk|WAL' -count=1
go test ./path/to/storage/... -bench . -benchmem -count=5
go test ./... -count=1
go test -race ./internal/chat ./internal/agent ./internal/provider -count=1
make verify
```

The exact package path is intentionally unresolved until Phase 0 establishes the repository-owned storage package. Do not substitute a broad command and report the focused gate as passed.

## Rollback and safety

- This validation phase must not change current chat persistence behavior.
- Do not delete `.mivia` data or existing JSONL sessions.
- Do not commit benchmark databases, WAL files, raw fixtures, or captured model/tool payloads.
- Keep benchmark artifacts outside the repository or scrub them before publication.
- A failed SQLite benchmark is a valid result; move to the fallback decision gate rather than weakening thresholds.

## Open questions

- What exact event rate and payload-size distribution represents a production 200-agent run?
- Is `synchronous=FULL` required for acknowledged history, or is loss of the latest uncheckpointed work acceptable?
- What is the maximum acceptable writer-queue wait before an agent run is cancelled or degraded?
- Are context-pack artifacts stored in SQLite, separate local files, or a content-addressed blob layer?
- Which redaction and retention policy is approved for prompts, tool results, and PII?

## Required human review

Performance and architecture review are required before Phase 1 implementation. Security/privacy review is required before any test fixture or production path persists prompts, tool output, personal data, or credentials. No production implementation or commit is authorized by this plan until Phase 4 produces a passing, source- and benchmark-grounded decision.
