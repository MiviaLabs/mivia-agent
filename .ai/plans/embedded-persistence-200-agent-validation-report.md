# 200-agent embedded persistence validation report

Status: Incomplete; no backend approval

Date: 2026-07-27

## Environment

- Repository: `github.com/MiviaLabs/mivia-agent`
- Host: WSL Ubuntu, Linux amd64
- Go: `go1.26.3 linux/amd64`
- Candidate: `modernc.org/sqlite v1.54.0`
- SQLite mode: WAL, `synchronous=FULL`, `busy_timeout=5000`, one serialized writer mutex, eight pooled connections

## Implemented validation surface

- `internal/storage/store.go`: testable Store interface, in-memory contract implementation, SQLite event store.
- `internal/storage/store_test.go`: idempotency, ordering, and 200 logical-agent concurrent write contract tests.
- `internal/storage/store_bench_test.go`: 1/10/50/100/200 logical-agent Go benchmarks.
- `internal/storage/validation_test.go`: reopen durability, WAL/checkpoint observability, 200-agent percentile logging.
- `internal/storage/queue.go`: bounded validation writer with queue wait metrics and backpressure.

This is validation infrastructure only. It is not integrated into `internal/chat` or production session persistence.

## Verification executed

```text
go test ./internal/storage -count=1                 PASS
go test -race ./internal/storage -count=1           PASS
go test ./internal/storage -bench BenchmarkSQLiteLogicalAgents -benchmem -benchtime=100ms -count=1 PASS
go test ./internal/storage -count=1 -v                  PASS
go test -race ./... -count=1                            PASS
make verify                                             PASS
go test ./internal/storage -run TestQueuedWriter_200AgentQueueEvidence -count=1 -v PASS
```

Observed repeated benchmark output (`-benchtime=100ms -count=5`; values vary with host load):

| Logical agents | Result |
|---:|---:|
| 1 | 1.61–2.13 ms/op |
| 10 | 15.77–17.23 ms/op |
| 50 | 383–709 ms/op |
| 100 | 822–1,188 ms/op |
| 200 | 1,839–2,615 ms/op |

The benchmark operation is one event per logical agent per iteration. It is a contention probe, not a production workload model.

Observed percentile test for 200 agents × 2 events:

- p50: 352 ms (single run)
- p95: 409 ms (single run)
- p99: 411 ms (single run)
- max: 413 ms (single run)

Bounded writer evidence for 200 agents × 2 events, queue capacity 64:

- submitted/committed: 400/400
- average enqueue-to-commit wait: 188.83 ms
- maximum enqueue-to-commit wait: 291.24 ms

## Evidence interpretation

SQLite handled the test without corruption, duplicate events, race failures, or `SQLITE_BUSY` errors. Reopen, integrity check, checkpoint, long-reader release, backup/restore after writes, backup during active writes, bounded page-limit disk pressure, and committed/uncommitted child-process recovery tests pass. The bounded writer accepted and committed all 400 events, but queue wait reached 291 ms. The write mutex makes the single-writer constraint explicit; repeated 200-agent batches ranged from 1.84–2.62 seconds, so the result is a contention baseline, not a capacity approval.

## Not run

- `PARTIAL`: child-process crash injection covers an uncommitted transaction; enqueue/commit/checkpoint/artifact boundary matrix is not covered.
- `NOT_RUN`: real power-loss durability test.
- `PARTIAL`: bounded SQLite page-limit disk pressure is tested; real filesystem/quota exhaustion and space-restoration recovery are not.
- `PASS`: backup/restore is tested after writes and while writes are active; backup under real process failure is not.
- `PARTIAL`: long-reader release and checkpoint are tested; sustained WAL-growth limit measurement is not.
- `DEFERRED`: Badger comparison; explicitly skipped by scope decision. SQLite is the only candidate in this validation cycle.
- `NOT_RUN`: production chat/session integration.

## Decision

No-go for backend selection and production integration. The current result is a useful baseline showing that naive serialized SQLite writes do not establish 200-agent readiness. Before implementation authorization, define the event-rate/payload workload and latency/error thresholds, then complete crash, disk-pressure, backup/restore, and WAL-starvation tests. Badger is explicitly deferred.
