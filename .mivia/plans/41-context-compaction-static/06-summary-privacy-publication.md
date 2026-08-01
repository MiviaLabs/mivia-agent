# 41.06 — Summary adapter, privacy, and transactional publication

Status: ready after phase `05` review; lands atomically with phase 07 or remains disabled.

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
- `internal/events/event.go` and `_test.go`: define the immutable typed
  `CompactionEvent` payload before surface integration; it contains only trigger,
  before/after estimates, source-range IDs, and summary schema version.

The summary is framed as untrusted state data, never a system/developer message.
It cannot carry tool calls or authority fields. With no configured redaction,
content-bearing summaries are ephemeral and only structural checkpoint metadata
is persisted. Numeric limits from phase 01 are enforced before provider use and
before persistence.

The root/session manager exposes `CheckpointPublisher` only to chat. Nested
handlers receive `PreparationManager` with `Prepare` and `Discard` but no
`Commit`, store, session ID lookup, active provider credentials, or root
dispatcher. Summarization uses the captured active provider/model, a 10-second
deadline, no retries, and no network call when credentials or explicit provider
enablement are absent.

ADLC micro-tasks:

| ID | Wave | Type | File | Test/function, dependency, command, timeout, context |
|---|---|---|---|---|
| 06-RED-001 | 1 | RED | `internal/contextmgr/summary_test.go` | `TestValidateSummaryRejectsSensitiveAndOversizedData`; depends 05-REVIEW-001; `go test -run '^TestValidateSummaryRejectsSensitiveAndOversizedData$' ./internal/contextmgr`; 60s; `internal/contextmgr/summary.go`, `internal/contextmgr/summary_test.go`, `internal/contextmgr/contracts.go`, `internal/contextstate/contracts.go` |
| 06-GREEN-001 | 2 | GREEN | `internal/contextmgr/summary.go` | `ValidateSummary`; depends 06-RED-001; `go test -run '^TestValidateSummaryRejectsSensitiveAndOversizedData$' ./internal/contextmgr`; 60s; `internal/contextmgr/summary.go`, `internal/contextmgr/summary_test.go`, `internal/contextmgr/contracts.go`, `internal/contextstate/contracts.go` |
| 06-RED-002 | 3 | RED | `internal/contextmgr/summarizer_test.go` | `TestSummarizerUsesCapturedProviderAndTimeout`; depends 06-GREEN-001; `go test -run '^TestSummarizerUsesCapturedProviderAndTimeout$' ./internal/contextmgr`; 120s; `internal/contextmgr/summarizer.go`, `internal/contextmgr/summarizer_test.go`, `internal/contextmgr/summary.go`, `internal/contextmgr/contracts.go` |
| 06-GREEN-002 | 4 | GREEN | `internal/contextmgr/summarizer.go` | `Summarize`; depends 06-RED-002; `go test -run '^TestSummarizerUsesCapturedProviderAndTimeout$' ./internal/contextmgr`; 120s; `internal/contextmgr/summarizer.go`, `internal/contextmgr/summarizer_test.go`, `internal/contextmgr/summary.go`, `internal/contextmgr/contracts.go` |
| 06-REVIEW-001 | 5 | review | `internal/contextmgr/summarizer.go` | Provider/network/privacy review; depends 06-GREEN-002; `go test ./internal/contextmgr`; 120s; `internal/contextmgr/summarizer.go`, `internal/contextmgr/summarizer_test.go`, `internal/contextmgr/summary.go`, `internal/contextmgr/contracts.go` |
| 06-RED-003 | 6 | RED | `internal/contextmgr/commit_test.go` | `TestFailedCommitLeavesStateUnchanged`; depends 06-REVIEW-001; `go test -run '^TestFailedCommitLeavesStateUnchanged$' ./internal/contextmgr`; 120s; `internal/contextmgr/commit.go`, `internal/contextmgr/commit_test.go`, `internal/contextmgr/summary.go`, `internal/contextmgr/contracts.go`, `internal/contextstate/contracts.go` |
| 06-GREEN-003 | 7 | GREEN | `internal/contextmgr/commit.go` | `CommitPreparation`; depends 06-RED-003; `go test -run '^TestFailedCommitLeavesStateUnchanged$' ./internal/contextmgr`; 120s; `internal/contextmgr/commit.go`, `internal/contextmgr/commit_test.go`, `internal/contextmgr/summary.go`, `internal/contextstate/contracts.go` |
| 06-RED-004 | 8 | RED | `internal/events/event_test.go` | `TestCompactionEventIsSealedAndContentFree`; depends 06-GREEN-003; `go test -run '^TestCompactionEventIsSealedAndContentFree$' ./internal/events`; 60s; `internal/events/event.go`, `internal/events/event_test.go`, `internal/contextmgr/commit.go` |
| 06-GREEN-004 | 9 | GREEN | `internal/events/event.go` | `NewCompactionEvent`; depends 06-RED-004; `go test -run '^TestCompactionEventIsSealedAndContentFree$' ./internal/events`; 60s; `internal/events/event.go`, `internal/events/event_test.go`, `internal/contextmgr/commit.go` |
| 06-RED-005 | 10 | RED | `internal/events/serialize_test.go` | `TestCompactionEventSerializationRejectsGenericEnvelope`; depends 06-GREEN-004; `go test -run '^TestCompactionEventSerializationRejectsGenericEnvelope$' ./internal/events`; 60s; `internal/events/serialize.go`, `internal/events/serialize_test.go`, `internal/events/event.go` |
| 06-GREEN-005 | 11 | GREEN | `internal/events/serialize.go` | `MarshalCompactionEvent`; depends 06-RED-005; `go test -run '^TestCompactionEventSerializationRejectsGenericEnvelope$' ./internal/events`; 60s; `internal/events/serialize.go`, `internal/events/serialize_test.go`, `internal/events/event.go` |
| 06-REVIEW-002 | 12 | review | `internal/events/event.go` | Typed-event and publication review; depends 06-GREEN-005; `go test -race ./internal/contextmgr ./internal/events`; 180s; `internal/events/event.go`, `internal/events/event_test.go`, `internal/events/serialize.go`, `internal/events/serialize_test.go`, `internal/contextmgr/commit.go` |

Gate: security review PASS; `go test -race ./internal/contextmgr`; feature remains
disabled until phase 07 integration is ready.
Summary authority is permanently data-only. `SummaryContext` is an immutable,
host-framed value with a fixed untrusted prefix, canonical field ordering, and
source range; it is serialized as a user/context message, never a system or
developer message, and cannot authorize tools or alter dispatcher policy.
`UntrustedSummary` has no mutable `Untrusted` escape hatch. Provider calls are
allowed only when `SummaryEnabled`, credentials in `CredentialScope`, and
`NetworkEnabled` are all true; otherwise return `ErrSummaryUnavailable`
without network access or retry. The provider receives bounded canonical JSON
with an explicit delimiter/framing contract.
