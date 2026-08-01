# 41.08 — Events, audit, and closeout

Status: blocked pending `07`.

Exact scope:

- `internal/events/event.go` and `_test.go`: add a typed `CompactionEvent`
  constructor with only trigger, bounded before/after estimates, source-range
  IDs, and summary version; no content-bearing fields or raw errors.
- `internal/agent/emit.go`, `internal/cli/tui_events.go`, and UI tests: publish
  and render a short notice without summary content.
- `.mivia/invariants.md` and owned docs: add/update exact invariant test names,
  privacy/persistence behavior, and migration notes.

ADLC closeout waves:

| Wave | Type | File | Task / verification |
|---|---|---|---|
| 1 | RED | `internal/events/event_test.go` | Assert every compaction event field is allowlisted and bounded. |
| 2 | GREEN | `internal/events/event.go` | Implement typed constructor and validation. |
| 3 | RED | UI event tests | Assert short notice and no summary/prompt/reasoning output. |
| 4 | GREEN | agent/CLI event adapters | Wire event and rendering. |
| 5 | review | repository | Run bug-audit loop; uncertain findings require targeted tests. |
| 6 | verify | repository | Run all gates below and record exact results. |

```text
go test ./internal/provider ./internal/agent ./internal/chat ./internal/subagents
go test -race ./internal/chat ./internal/agent ./internal/subagents
go test -race ./internal/storage ./internal/ledger ./internal/contextmgr
go vet ./internal/provider ./internal/agent ./internal/chat ./internal/subagents
make verify
make docs-check
```

Closeout requires zero confirmed audit bugs, failure-injection and migration
evidence, no secret/PII/raw prompt/model dump fixtures, and a final Step 0/plan
scorecard disposition. No commit or implementation-ready claim is valid while a
required gate is unrun.
