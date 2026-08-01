# 41.06 — Summary adapter, privacy, and transactional publication

Status: blocked pending `05`; lands atomically with phase 07 or remains disabled.

Goal: add semantic compression without making model output an authority or a
durable privacy escape hatch.

Exact scope:

- `internal/contextmgr/summary.go` and `_test.go`: versioned bounded fields,
  typed untrusted provenance, duplicate/invalid/oversized rejection, and final
  budget accounting.
- `internal/contextmgr/summarizer.go` and `_test.go`: injected
  `SummaryProvider`, active provider binding, shared context cancellation,
  explicit output cap, and fake-provider/no-network tests.
- `internal/contextmgr/commit.go` and `_test.go`: call storage CAS only after
  successful summary validation; failed summary/provider/persistence calls leave
  active state, source records, checkpoints, and autosaves unchanged.

The summary is framed as untrusted state data, never a system/developer message.
It cannot carry tool calls or authority fields. With no configured redaction,
content-bearing summaries are ephemeral and only structural checkpoint metadata
is persisted. Numeric limits from phase 01 are enforced before provider use and
before persistence.

ADLC micro-tasks:

| Wave | Type | File | Task / verification |
|---|---|---|---|
| 1 | RED | `internal/contextmgr/summary_test.go` | Malicious credentials/PII/prompts/tool injection, malformed schema, limits. |
| 2 | GREEN | `internal/contextmgr/summary.go` | Implement schema/provenance validator and bounded framing. |
| 2 | RED | `internal/contextmgr/summarizer_test.go` | Provider binding, timeout, cancellation, missing credential, and no-network tests. |
| 3 | GREEN | `internal/contextmgr/summarizer.go` | Implement injected adapter and final-context budget accounting. |
| 3 | RED | `internal/contextmgr/commit_test.go` | Provider/summary/persistence failure rollback assertions. |
| 4 | GREEN | `internal/contextmgr/commit.go` | Implement token-bound transactional publication. |
| 5 | review | security + persistence | Verify no raw content reaches durable metadata or nested authority. |

Gate: security review PASS; `go test -race ./internal/contextmgr`; feature remains
disabled until phase 07 integration is ready.
