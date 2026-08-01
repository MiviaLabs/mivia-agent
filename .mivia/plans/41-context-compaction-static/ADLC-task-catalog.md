# 41 — ADLC task catalog

This catalog locks the Step 1/2 task protocol for phases 01–08. It is part of
the plan, not runtime state. No implementation task may be added without a RED
test, an exact file, one function, a dependency, a command, a timeout, and a
context scope of five files or fewer.

## Task record

Every row in a phase file uses:

```text
ID | Wave | Type | File | Test/function | Depends on | Command | Timeout | Context
```

Types are `bootstrap`, `RED`, `GREEN`, `review`, or `verify`. A bootstrap task
creates only a package declaration and compile-safe type/test seams so the
first RED test can compile; it adds no behavior. A RED task must fail an
assertion rather than fail
compilation. Its matching GREEN task is the only task allowed to implement that
function. Review tasks are read-only and may not target a file they modify.

## Dependency waves

```text
01 contracts
  → 02 source foundation
  → 03 atomic storage
  → 04 session fencing
  → 05 pure planner
  → 06 summary/privacy + typed event
  → 07 surface integration
  → 08 adapters, audit, closeout
```

Within each phase, task IDs are ordered by dependency. Bootstrap rows are the
explicit new-file declaration for files that do not yet exist; later rows must
refer to that declared path. A phase validator must
reject a task if its named file does not exist and is not explicitly a new file,
if its production task names more than one function, or if its command cannot
run against the current package boundary. New packages must have a bootstrap
task before their first RED task. RED tasks must use a test-only seam, fixture,
or explicit pending assertion so missing production behavior produces an
assertion failure, never a compile failure. Every context entry is an exact
repository-relative path; basenames, abbreviations, wildcards, and “same
command” are invalid. Every command is literal and runnable from repository
root. No phase may place more than three behavior-producing tasks between
review rows, and cross-phase dependencies must name the immediately preceding
phase review row.

The source phase separately covers `ReadRange` and `ReadPayload`; lifecycle
covers principal-scoped delete, export, audit, tombstone, and revocation.
Surface integration specifies one shared `*storage.SQLite`: CLI opens and
closes it once, while ledger and chat borrow it and never close.

## Validators

Before implementation, dispatch one read-only validator per phase wave. Each
validator reads at most the task's context scope and reports `PASS` or `REJECT`
with an exact reason. Any REJECT returns to Step 1; two REJECTs for the same
task return to Step 0. The orchestrator records RED assertion output, GREEN
output, reviewer output, and the wave race gate in ephemeral context.

## Non-user-visible landing rules

- Phases 01–04 may land only as compatibility-preserving foundations.
- Phase 05 is pure and remains behind the disabled feature gate.
- Phase 06 adds the typed event and summary transaction but remains disabled.
- Phase 07 may land only atomically with the feature enablement decision from
  phase 06; otherwise it compiles with the old prune path.
- Phase 08 enables no new authority and must follow the complete integration.

## Final scorecard

The final Step 0 PASS requires all of these to be true in the plan:

- package dependency direction has no cycle;
- `contextstate.Store.Commit` is the single atomic checkpoint boundary;
- source recovery semantics are explicit for configured and unconfigured
  redaction;
- session, durable, source, binding, and turn revisions have CAS behavior;
- summary provenance cannot become authority;
- nested handlers cannot acquire checkpoint/persistence capabilities;
- every production task has a preceding RED task and exact file/function;
- legacy JSONL migration has deterministic idempotency and rollback behavior;
- feature rollout cannot expose a partial compaction path;
- rollback and failure-injection tests are named;
- the repository gates are actually run before completion claims.
- canonical DTOs define `CheckpointID`, provider/model/generation binding,
  complete post-turn `CommitRequest` state, canonical serialization,
  constructors, validation errors, and the `TurnResult`→`CommitRequest` mapper;
- payload APIs require `Principal` on every read/lifecycle operation and use a
  dedicated sanitized namespace with authoritative tombstone/revocation;
- numeric limits cover IDs, event counts/bytes, payloads, summaries, exports,
  deletes, and audit records;
- typed compaction events are sealed at construction and generic-envelope
  serialization boundaries;
- CLI/chat/ledger share one explicitly owned SQLite instance.
