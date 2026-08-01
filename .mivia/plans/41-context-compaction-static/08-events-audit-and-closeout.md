# 41.08 — Events, audit, and closeout

Status: blocked pending `07`.

Exact scope:

- `internal/agent/emit.go` and `_test.go`: publish the already-typed event only
  after durable commit.
- `internal/cli/tui_events.go` and `_test.go`: render a short notice without
  summary content.
- `.mivia/invariants.md`: add/update exact invariant test names. Documentation
  changes are owned by phase 02 in `docs/architecture/embedded-persistence.md`
  and `docs/security/overview.md`.

ADLC closeout waves:

| ID | Wave | Type | File | Test/function, dependency, command, timeout, context |
|---|---|---|---|---|
| 08-RED-001 | 1 | RED | `internal/agent/emit_test.go` | `TestEmitCompactionAfterCommitOnly`; depends 07-REVIEW-003; `go test -run '^TestEmitCompactionAfterCommitOnly$' ./internal/agent`; 60s; emit.go, emit_test.go, event.go, contextmgr/commit.go |
| 08-GREEN-001 | 2 | GREEN | `internal/agent/emit.go` | `emitCompaction`; depends 08-RED-001; same command; 60s; emit.go, emit_test.go, event.go, contextmgr/commit.go |
| 08-RED-002 | 2 | RED | `internal/cli/tui_events_test.go` | `TestRenderCompactionNoticeOmitsContent`; depends 08-GREEN-001; `go test -run '^TestRenderCompactionNoticeOmitsContent$' ./internal/cli`; 60s; tui_events.go, tui_events_test.go, event.go |
| 08-GREEN-002 | 3 | GREEN | `internal/cli/tui_events.go` | `renderCompactionNotice`; depends 08-RED-002; same command; 60s; tui_events.go, tui_events_test.go, event.go |
| 08-REVIEW-001 | 4 | review | `internal/agent/emit.go` | Event adapter review; depends 08-GREEN-002; `go test ./internal/events ./internal/agent`; 120s; emit.go, emit_test.go, event.go, tui_events.go, tui_events_test.go |
| 08-AUDIT-001 | 5 | review | `.mivia/invariants.md` | `TestContextCompactionInvariants`; depends 08-REVIEW-001; `make invariants`; 300s; invariants.md, agent/emit.go, chat/session.go, storage/context_store.go |
| 08-VERIFY-001 | 6 | verify | `Makefile` | Focused provider/agent/chat/subagent gate; depends 08-AUDIT-001; `go test ./internal/provider ./internal/agent ./internal/chat ./internal/subagents`; 300s; provider/context.go, agent/loop.go, chat/session.go, subagents/multi_step.go, subagents/oneshot.go |
| 08-VERIFY-002 | 6 | verify | `Makefile` | Race storage/context gate; depends 08-AUDIT-001; `go test -race ./internal/storage ./internal/contextmgr ./internal/contextstate`; 600s; storage/context_store.go, contextmgr/commit.go, contextstate/contracts.go |
| 08-VERIFY-003 | 6 | verify | `Makefile` | Repository quality gate; depends 08-AUDIT-001; `make verify && make docs-check`; 600s; Makefile, .mivia/INDEX.md, docs/architecture/embedded-persistence.md, docs/security/overview.md |

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
