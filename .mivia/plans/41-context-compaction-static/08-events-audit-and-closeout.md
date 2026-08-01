# 41.08 — Events, audit, and closeout

Status: ready after phase `07` review.

Exact scope:

- `internal/agent/emit.go` and `_test.go`: publish the already-typed event only
  after durable commit.
- `internal/cli/tui_events.go` and `_test.go`: render a short notice without
  summary content.
- `.mivia/invariants.md`: add/update exact invariant test names. Documentation
  changes are owned by phase 02 in `docs/architecture/embedded-persistence.md`
  and `docs/security/overview.md`.

Source-event construction is a dedicated allowlist projector, tested with
reasoning/prompt/tool sentinels, and excludes system/developer prompts, hidden
reasoning, credentials, provider payloads, and generic event content.
The typed compaction event cannot be converted into the generic content/input/
output envelope. Context-enabled persistence never falls back to raw JSONL.

ADLC closeout waves:

| ID | Wave | Type | File | Test/function, dependency, command, timeout, context |
|---|---|---|---|---|
| 08-BOOT-001 | 1 | bootstrap | `internal/cli/tui_events_test.go` | compile-safe compaction-render fixture seam only; depends 07-REVIEW-004; `go test ./internal/cli`; 60s; `internal/cli/tui_events_test.go`, `internal/cli/tui_events.go`, `internal/events/event.go` |
| 08-BOOT-002 | 1 | bootstrap | `internal/agent/compaction_invariant_test.go` | compile-safe invariant fixture seam only; depends 07-REVIEW-004; `go test ./internal/agent`; 60s; `internal/agent/compaction_invariant_test.go`, `internal/agent/emit.go`, `internal/storage/context_store.go` |
| 08-RED-001 | 2 | RED | `internal/agent/emit_test.go` | `TestEmitCompactionAfterCommitOnly`; depends 07-REVIEW-004; `go test -run '^TestEmitCompactionAfterCommitOnly$' ./internal/agent`; 60s; `internal/agent/emit.go`, `internal/agent/emit_test.go`, `internal/events/event.go`, `internal/contextmgr/commit.go` |
| 08-GREEN-001 | 3 | GREEN | `internal/agent/emit.go` | `emitCompaction`; depends 08-RED-001; `go test -run '^TestEmitCompactionAfterCommitOnly$' ./internal/agent`; 60s; `internal/agent/emit.go`, `internal/agent/emit_test.go`, `internal/events/event.go`, `internal/contextmgr/commit.go` |
| 08-RED-002 | 4 | RED | `internal/cli/tui_events_test.go` | `TestRenderCompactionNoticeOmitsContent`; depends 08-GREEN-001, 08-BOOT-001; `go test -run '^TestRenderCompactionNoticeOmitsContent$' ./internal/cli`; 60s; `internal/cli/tui_events.go`, `internal/cli/tui_events_test.go`, `internal/events/event.go` |
| 08-GREEN-002 | 5 | GREEN | `internal/cli/tui_events.go` | `renderCompactionNotice`; depends 08-RED-002; `go test -run '^TestRenderCompactionNoticeOmitsContent$' ./internal/cli`; 60s; `internal/cli/tui_events.go`, `internal/cli/tui_events_test.go`, `internal/events/event.go` |
| 08-REVIEW-001 | 6 | review | `internal/agent/emit.go` | Event adapter review; depends 08-GREEN-002; `go test ./internal/events ./internal/agent`; 120s; `internal/agent/emit.go`, `internal/agent/emit_test.go`, `internal/events/event.go`, `internal/cli/tui_events.go`, `internal/cli/tui_events_test.go` |
| 08-RED-003 | 7 | RED | `internal/agent/compaction_invariant_test.go` | `TestContextCompactionInvariants`; depends 08-REVIEW-001, 08-BOOT-002; `go test -run '^TestContextCompactionInvariants$' ./internal/agent`; 120s; `internal/agent/compaction_invariant_test.go`, `internal/agent/emit.go`, `internal/chat/session.go`, `internal/storage/context_store.go` |
| 08-GREEN-003 | 8 | GREEN | `.mivia/invariants.md` | record compaction invariants (docs task); depends 08-RED-003; `make invariants`; 300s; `.mivia/invariants.md`, `internal/agent/compaction_invariant_test.go` |
| 08-REVIEW-002 | 9 | review | `.mivia/invariants.md` | invariant/audit review; depends 08-GREEN-003; `make invariants`; 300s; `.mivia/invariants.md`, `internal/agent/compaction_invariant_test.go`, `internal/chat/session.go`, `internal/storage/context_store.go` |
| 08-VERIFY-001 | 10 | verify | `internal/provider/context.go` | focused provider/agent/chat/subagent gate; depends 08-REVIEW-002; `go test ./internal/provider ./internal/agent ./internal/chat ./internal/subagents`; 300s; `internal/provider/context.go`, `internal/agent/loop.go`, `internal/chat/session.go`, `internal/subagents/multi_step.go`, `internal/subagents/oneshot.go` |
| 08-VERIFY-002 | 10 | verify | `internal/storage/context_store.go` | race storage/context gate; depends 08-REVIEW-002; `go test -race ./internal/storage ./internal/contextmgr ./internal/contextstate`; 600s; `internal/storage/context_store.go`, `internal/contextmgr/commit.go`, `internal/contextstate/contracts.go` |
| 08-VERIFY-003 | 10 | verify | `Makefile` | repository quality gate; depends 08-REVIEW-002; `make verify`; 600s; `Makefile`, `.mivia/INDEX.md`, `scripts/check_go_structure.py` |
| 08-VERIFY-004 | 10 | verify | `Makefile` | documentation gate; depends 08-REVIEW-002; `make docs-check`; 600s; `Makefile`, `docs/OWNERS.yaml`, `docs/architecture/embedded-persistence.md`, `docs/security/overview.md` |

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
