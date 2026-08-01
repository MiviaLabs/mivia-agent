# 23 - Content retention: none exists, and almost none should be built

**Status:** ✅ IMPLEMENTED 2026-07-30 - §3 decision E landed in `99609fc`:
content retention is deliberately unbounded, pinned by two regression tests,
documented in the owned product surface and repository contract, and registered
as `INV-AG-15`. §4 change #4 and §8's invariant allocation resolved at landing.
**Date:** 2026-07-30
**Depends on:** `19` (implemented - `ledger_read`, `contentref` as the one minter, INV-AG-10),
`20` (validated → do-not-build; INV-AG-12 registered the accepted limitation this plan
narrows), `21` (implemented - durable event timestamps).
**Prerequisite landing in parallel:** `24-durable-run-deletion.md` - durable
`DeleteRun`. This plan proposes **no change to `StorageLedgerRepository.DeleteRun`**
and no change to `internal/storage/store.go`, both of which `24` is editing right
now. Every claim below about deletion is written to hold *before and after* `24`
lands, and §4 states which of this plan's tests must be written against `24`'s
post-change behaviour.
**Blocks:** nothing. **Composes with:** `24` (adjacent, deliberately disjoint -
`24` deletes events and never content; this plan explains why nothing should
delete content either), `15` (resume surface - a user-facing delete surface would
inherit this analysis).
**Blast radius:** **LOW.** No production behaviour changes. One new test file, one
doc-comment, one documentation section, one invariant row. No schema change, no
interface change, no config key, no new tool, no model-facing text.
**Landed commit:**
`test(agent): pin that shared content references outlive run deletion`,
commit `99609fc` (including the accompanying owned-doc, contract and invariant updates).

---

## Premises corrected up front

Three claims that framed this work are wrong. The plan is written against the
corrected versions, and two of them are the reason §3 lands on E.

- **`INV-AG-13` is NOT the next free id. TWO other in-flight plans already claim it.**
  I was told ids 1–7, 9–12 are taken, `INV-AG-8` is absent, and 13 is next free.
  The first two halves are correct (`.mivia/invariants.md`, verified: `INV-AG-1`…`7`,
  `9`, `10`, `11`, `12`; no `8`). The third is false, twice over:
  `.mivia/plans/24-durable-run-deletion.md:300` registers `INV-AG-13` for durable
  run deletion (`.mivia/INDEX.md:69` - implementation-ready), **and**
  `.mivia/plans/22-idempotent-spawn-fingerprints-the-work.md:662,667` claims
  `INV-AG-13` for idempotency scope (`.mivia/INDEX.md:68` - design-ready). Both were
  written concurrently with this one and neither knows about the other; `22:662`
  says "the next free id is `INV-AG-13`. Re-verified by reading the file", which was
  true when read and is now contested.
  **This plan proposes `INV-AG-14`** on the assumption that exactly one of `22`/`24`
  takes 13. That assumption is fragile by construction, so §8 makes the rule
  mechanical: the commit that registers this row **re-reads `.mivia/invariants.md`
  and takes the lowest free id above 12**, and never `INV-AG-8`, which is a gap
  rather than a free slot. Every "`INV-AG-14`" below means "the id §8 resolves to".

- **Plan `24`'s blast radius under-counts the `storage.Store` test doubles by one.**
  `24:7` says "two implementations plus one test double" and `24:160` names only
  `countingStore` (`internal/ledger/storage_catchup_test.go:16-90`). There is a
  second: `flushSQLite` (`internal/storage/store_agent_integration_test.go:171-176`),
  which delegates all eleven `Store` methods explicitly - `Append` at `:178`,
  `Events` `:186`, `EventsSince` `:194`, `Changes` `:202`, `Count` `:210`,
  `ListRunIDs` `:218`, `Close` `:226`, claims at `:234-256`, `PutContent` `:258`,
  `GetContent` `:265` - and is passed to `NewQueuedWriter(store Store, …)`
  (`internal/storage/queue.go:35`) at `store_agent_integration_test.go:133`. Adding a
  method to `storage.Store` therefore breaks `internal/storage`'s own test package
  too, not only `internal/ledger`'s. This matters to §3 because it raises the price
  of every option that needs a store-level delete, and it is worth telling `24`.
  *Not* affected: `contentStoreFailingRepo` (`internal/cli/delegation_test.go:401-403`)
  and `storeContentFailingRepo` (`internal/coordinator/record_results_test.go:17-19`) both
  **embed** `*ledger.MemoryLedgerRepository`, so they inherit new
  `LedgerRepository` methods for free.

- **There is no `mivia diagnostics` command, and `Diagnostics` has no production
  caller at all.** `Execute` dispatches exactly `version|help|chat|config|doctor`
  (`internal/cli/root.go:18-33`). `NewDiagnostics` is referenced only from
  `internal/cli/diagnostics_test.go` - `grep -rn 'NewDiagnostics\|Diagnostics{'`
  returns six test hits and the definition. Plan `21` §1c's claim that the run
  timestamp defect "reaches `mivia diagnostics`" describes a surface a user cannot
  invoke. Consequence for this plan: **there is no operator-facing store command to
  hang a prune on**, and `mivia doctor` never opens the store
  (`internal/cli/doctor.go:11-51` loads config and prints; no `storage`,
  no `ledger` import).

**Confirmed as handed to me**, with my own evidence - every one of the following
held exactly:

- Memory content is a flat `map[string][]byte` keyed by reference only:
  `MemoryLedgerRepository.content` (`internal/ledger/memory.go:27`), written by
  `StoreContent` (`internal/ledger/memory_claims.go:34-42`) and read by
  `LoadContent` (`:44-53`). No run key, no principal key, no timestamp.
- SQLite DDL is
  `CREATE TABLE IF NOT EXISTS content (ref TEXT PRIMARY KEY, data BLOB NOT NULL, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`
  (`internal/storage/store.go:269-271`). Writes are
  `INSERT OR IGNORE INTO content(ref, data) VALUES(?, ?)` (`:443`) - three columns,
  `created_at` never named. Reads are `SELECT data FROM content WHERE ref = ?`
  (`:449`). Both **line-exact**.
- `content.created_at` is therefore whatever `CURRENT_TIMESTAMP` produced. Measured
  on a real file: `typeof = text`, value `"2026-07-30 05:02:22"` - UTC text,
  one-second granularity, no fractional part, no zone - and three rows inserted in
  one call all tied inside one second. Plan `21` §1b measured this for `events`;
  it is confirmed here for `content`.
- `MemoryLedgerRepository.DeleteRun` (`internal/ledger/memory.go:349-362`) deletes
  the idempotency index entries and the run record and never touches `m.content`.
- No retention machinery of any kind:
  `grep -rn 'DeleteContent\|retention\|Retention\|expire\|Expire\|prune\|Prune\|VACUUM\|sweep\|Sweep' --include=*.go internal/ledger internal/storage`
  returns exactly one line - `VACUUM INTO` in `SQLite.Backup`
  (`internal/storage/store.go:281`).
- `PRAGMA user_version` reads **0**; tables are exactly `content events run_claims`;
  the only pragmas are the four at `internal/storage/store.go:247`. Adding a column
  and naming it fails at runtime on an existing file, measured:
  `INSERT INTO content(ref, data, run_id) …` →
  `SQL logic error: table content has no column named run_id (1)`.
- The default backend is `memory`: `storeBackend = "memory"` when unset
  (`internal/config/load.go:46-48`).
- `RunSnapshot.Status` is `RunStatus`, not `string`
  (`internal/ledger/types.go:47-57`). Every expression in §6 was compiled.

## 1. The defect

Two properties, deliberately separated because they have different severities,
different backends, and different fixes.

### 1a. Recorded content is never deleted

| Backend | Content store | Deleted by `DeleteRun`? | Any other deletion? | Survives the process? |
|---|---|---|---|---|
| `memory` (**default**, `internal/config/load.go:46-48`) | `map[string][]byte` (`internal/ledger/memory.go:27`) | **No** - `DeleteRun` (`memory.go:349-362`) touches `m.runs` and `m.idemLookup` only | **None.** No `DeleteContent` on the interface (`internal/ledger/repository.go:24-110`), no sweep, no expiry | **No** - the map dies with the process |
| `sqlite` (opt-in) | `content` table (`internal/storage/store.go:269-271`) | **No** - today `StorageLedgerRepository.DeleteRun` delegates to `s.mem.DeleteRun` (`internal/ledger/storage.go:437-442`). After plan `24` it will delete `events` and `run_claims` rows and still, deliberately, no `content` row (`24:219-222`) | **None.** `storage.Store` has `PutContent`/`GetContent` and no delete (`internal/storage/store.go:67-72`) | **Yes** - a file under `os.UserCacheDir()/mivia/workspaces/<hash>/orchestration.db` (`internal/config/defaults.go:68-80`), outside the workspace |

Measured, both backends, on the tree as it stands (probe file created in
`internal/ledger`, run, output recorded verbatim, file deleted):

```
PROBE memory after DeleteRun: content="x" err=<nil>
PROBE sqlite  after DeleteRun: content="y" err=<nil>
PROBE sqlite  GetRun after DeleteRun: err=not found
PROBE ref for "context deadline exceeded" = ref:error:05e510230f2518b842597cecaaf0106c48892735f561e532b9c3ab4ffa46c72d
PROBE cross-process(sqlite) LoadContent: data="context deadline exceeded" err=<nil>
PROBE cross-process(sqlite) deleted-run ref: data="y" err=<nil>
```

The run is gone from the projection (`GetRun` → `not found`) and its bytes resolve
anyway, in this process and in the next one over the same file.

**But "content outlives its run" describes a path that never carries content.**
`DeleteRun`'s only production callers are the four create-failure unwinds in
`internal/coordinator/spawn.go` - `:65` and `:68` (claim refused or claim error),
`:74` via `releaseAndDeleteRun` (`run_created` append failed), `:80` (task creation
failed) - plus `releaseAndDeleteRun` itself (`:169-174`). All four precede
`go c.executeRun(...)` at `:91`. Content is written by `persistResultContent`
(`internal/coordinator/record_results.go:70-84`), reached only from
`recordRunResults`, whose single production caller is
`internal/coordinator/coordinator.go:34` after the pool has run. **A run that
`DeleteRun` deletes has stored no content.** Plan `24` §1b reached the same
conclusion independently; I confirmed it by reading the call graph rather than
taking it.

So the honest statement of 1a is: *nothing ever deletes content, and the one
deletion path that exists would have nothing to collect if it tried.*

### 1b. Recorded content has no bound

Distinct from 1a and, unlike it, unconditional on both backends.

- No count cap, no byte cap, no eviction: the greps above return nothing, and
  neither `StoreContent` implementation consults a limit
  (`internal/ledger/memory_claims.go:34-42`, `internal/ledger/storage_claims.go:55-57`
  → `internal/storage/store.go:439-445`).
- Per-write bounds exist and do not bound the *store*: `normalizeReference` caps a
  reference key at 256 bytes (`internal/ledger/memory.go:470-480`, `maxReferenceBytes`
  at `:14`) and `maxEventPayload` caps event payloads at 1024 (`:15`). Neither
  applies to `content.data`, which is written raw.
- The memory backend's map is therefore unbounded for the process lifetime, and
  `defaultOrchestrationRepo` is a package-level var
  (`internal/cli/orchestration_state.go:29`), so its lifetime *is* the process.

**Measured cost.** 20 runs × 10 tasks = 200 tasks through the real
`StorageLedgerRepository` over a real SQLite file, with a ~500-byte task-output
payload per task:

```
PROBE size: content rows=200 logical bytes=127100 (635.5 B/task)
PROBE size: event   rows=1280 logical bytes=228230 (1141.2 B/task)
PROBE size: db file=520192 bytes wal=0 bytes total=520192 (2601.0 B/task)
PROBE size: content share of logical bytes = 35.8%
```

Content is 636 B/task at a 500-byte reply, 36% of the logical bytes and 24% of the
file. The floor is smaller: `buildResult`'s envelope with an empty reply
(`internal/subagents/multi_step.go:123-151` - `output`, `steps`, `elapsed`,
`step_count`, `status`) marshals to **74 bytes**, and a realistic terse reply to
**129 bytes**; add the 76-byte reference key and the floor is ~150 B/task. Error
content is smaller still: `"context deadline exceeded"` is 25 bytes.

Extrapolated honestly: 1 000 tasks ≈ 0.6 MB of content inside a 2.6 MB file;
10 000 tasks ≈ 6 MB inside 26 MB; reaching 1 GB of *content* takes ~1.6 million
tasks. With `max_fanout` 16 and `max_depth` 3 (`internal/config/defaults.go:17-19`)
and one LLM round-trip per task, that is not a workload anyone will reach.

### 1c. The finding that reframes the whole plan: events have no retention either

`events` grows at 1141 B/task - **1.8× the content** - and nothing deletes those
rows either. Before plan `24` there is no `DELETE` against `events` anywhere; after
`24` there is one, reachable only from the create-failure unwind (`24:314`, and
§1a's call-graph proof). Run records accumulate identically: `CloseRun` marks
`rec.closed = true` (`internal/ledger/memory.go:335-347`) and removes nothing.

**A plan that gives `content` retention and leaves `events` alone reclaims 24% of a
file and calls the problem solved.** Retention is a property of the *history*, not
of the content table. That is an argument about scope, and §3 treats it as decisive.

### 1d. The privacy dimension, stated without overclaiming

The material is real. Stored content is task output (`r.Output`) and task error
text (`r.Err.Error()`) written raw by `persistResultContent`
(`internal/coordinator/record_results.go:72,78`) - no redaction on the write path,
and redaction is off by default (INV-SEC-2, plan `10`). `ledger_read` redacts on
read (`internal/cli/ledger_tools.go:139`) and only under a configured policy. So
the store does hold unredacted, model-authored and tool-captured bytes forever on
SQLite.

**And retention here is not a privacy control, because it would delete the second
copy while the first survives.** The same bytes are already in the session
transcript:

1. `modelTaskResults` sets `Output` **and** `OutputRef` on the same record
   (`internal/cli/orchestrate_lifecycle.go:42-49`); `delegateResultPayload` likewise
   (`internal/cli/delegate.go:167-184`). The output is inline next to its reference.
2. That tool-result body is persisted verbatim as JSONL -
   `writeJSONL` encodes every `provider.Message` unmodified
   (`internal/chat/persistence_io.go:18-33`) - and restored wholesale by
   `Session.Load` (`internal/chat/persistence.go:256-286`).
3. Sessions live **inside the workspace**: `SessionsDir` is
   `<root>/.mivia/sessions` (`internal/workspace/namespace.go:36`, `Namespace` at
   `:17`, wired at `internal/cli/chat_command.go:82`). `internal/chat` performs no
   redaction - `grep -rn 'redact\.' internal/chat/` returns nothing.
4. That directory is reachable by the agent's own workspace-confined `read_file`
   (`internal/tools/read.go:15-19`), and nothing is compiled into
   `secret_path_patterns` (INV-SEC-1, `internal/config/types.go:52-53`).
5. Auto-saves are pruned to 50 exit snapshots and 5 turn snapshots
   (`AutoSaveKeep`/`TurnSaveKeep`, `internal/chat/persistence.go:38,45`;
   `expiredAutoSaves`, `internal/chat/autosave_retention.go:76-96`) - but a
   **user-named** session is deliberately never pruned (`IsAutoSaveName` requires
   the full minted shape, `:21-31`, precisely so `/save mywork` survives).

The only asymmetry favours the transcript: a tool result in history is capped at
`DefaultMaxToolResultChars = 4000` (`internal/chat/session.go:84`, applied at
`internal/agent/loop_tools.go:423`), while the content row holds the full bytes. For
payloads under 4 KB - which §1b measures as the ordinary case - the two copies are
identical.

**Conclusion, flatly: content retention is a disk-space control. Claiming it as a
privacy or deletion-path control would be false while `<workspace>/.mivia/sessions`
holds the same bytes unredacted with no age limit for user-named sessions.** Rule
`10`'s PDPL posture asks for a deletion path; this plan cannot honestly supply one,
and says so rather than half-supplying it.

### 1e. Who is actually harmed

| Surface / actor | Harmed by "never deleted"? | Harmed by "no bound"? | Evidence |
|---|---|---|---|
| The model, resolving a reference it holds | **No - helped.** Permanence is what makes INV-AG-10 true across a restart | No | Measured: `PROBE cross-process(sqlite) LoadContent: data="context deadline exceeded" err=<nil>` |
| A user reopening a saved session | **No - helped.** Transcript refs are arbitrarily old and still resolve | No | `internal/chat/persistence_io.go:18-33`, `persistence.go:256-286`; user sessions exempt from prune (`autosave_retention.go:21-31`) |
| Operator, disk | No | **Not measurably.** 636 B/task, 24% of a 2.6 KB/task file; and only on the opt-in backend | §1b probes; `internal/config/load.go:46-48` |
| Operator, memory (default backend) | No | **Technically yes, immaterially.** Unbounded map for the process lifetime; 127 KB at 200 tasks | `internal/ledger/memory.go:27`; `internal/cli/orchestration_state.go:29` |
| A user wanting their data deleted | **Yes, in principle - and not fixable here** | No | §1d: the workspace transcript copy has no age limit for user-named sessions |
| A second local user on the machine | No | No | Store is under `os.UserCacheDir()`, per-user (`internal/config/defaults.go:68-80`) |
| An agent using the digest as an equality oracle | **Yes, marginally** - permanence widens the oracle's window to "ever" | No | INV-AG-12; plan `20` §1, C4. Severity already analysed and accepted there |
| `ledger_read`'s `not_found` as evidence | **No - helped.** A store that forgets would make `not_found` mean two things | `internal/cli/ledger_tools.go:108-129`; `19` §3 corollary 2 |
| CI / tests | No | No | Every test uses a fresh repo or `t.TempDir()` |

**Nobody is harmed today, and two constituencies are actively helped.** That is the
finding, and it is not a comfortable one for a plan whose title contains
"retention".

## 2. Invariant to establish

> Recorded content is retained unconditionally: nothing reclaims it, and its
> lifetime is the store's, not its run's.

Corollaries:

- **A reference outlives its run by design.** References are content-addressed, so
  the reference→run relation is many-to-many; "its owning run" is not a
  well-defined thing to key a lifetime on. Measured: two runs producing
  byte-identical output share one row, and both task records name the same
  reference.
- **Any future reclamation must be reachability-based, never age-based or
  run-based.** A model transcript is durable, restorable and unbounded in age, so
  no age is safe and no run is authoritative.
- **The bound is the operator's disk, and it is stated rather than enforced.** An
  enforced bound would either evict a live reference (breaking INV-AG-10) or refuse
  new writes forever (§3 D).

## 3. Options

Every option is judged against INV-AG-10 explicitly - *"a content reference handed
to the model resolves, or it is not handed to the model"*
(`.mivia/invariants.md`, INV-AG-10) - because that is the guarantee plan `20` was
killed for proposing to break.

### A. Age-based sweep on `content.created_at`

Delete rows older than N days; on the memory backend, keep a parallel
`map[string]time.Time` and sweep it the same way.

*For:* No new relation to maintain. The column already exists. `MemoryLedgerRepository`
already has an injectable clock (`m.now`, `internal/ledger/memory.go:28`,
`SetTimeSource` `:57-61`), so it is deterministically testable.

*Against - it breaks INV-AG-10, and there is no safe N.* A reopened session's
transcript holds references of arbitrary age (§1d chain, steps 1–3), and
user-named sessions are never pruned (`autosave_retention.go:21-31`), so a session
saved six months ago is a first-class artefact whose references must still resolve.
Choosing N is choosing how old a saved session may be before the product silently
stops answering questions about it. The honest N is ∞.

*Against - the timestamp is the wrong quantity anyway.* `created_at` is the
*insert* instant of the first write of those bytes. Because writes are
`INSERT OR IGNORE` (`internal/storage/store.go:443`), the second run to produce
identical output does **not** refresh it: the row keeps the first run's timestamp,
so shared content ages out on its oldest reference. Measured granularity is one
second, text, no zone - tolerable for day-scale ages, but it is measuring the
wrong event.

*Against - cost.* Needs a delete on `storage.Store` (2 implementations + 2 explicit
test doubles, per the corrected premise), plus a timestamp side-map and a clock on
the memory path.

**Rejected: breaks INV-AG-10 with no defensible parameter.**

### B. Reference counting

*For:* The textbook answer to shared ownership.

*Against - there is no decrement event.* A count needs a release. Nothing ever
releases a reference: the model holds it in a transcript that the product does not
own, cannot enumerate, and restores from disk (`Session.Load`). A count that only
increments is a row count.

*Against - it needs durable state the schema cannot hold.* A counter must live in a
new column or table. There is no `PRAGMA user_version` (measured: 0), no version
table, no migration runner; `CREATE TABLE IF NOT EXISTS` never alters an existing
table, so the column is silently absent on every existing file and the first
`INSERT` naming it fails at runtime - measured:
`SQL logic error: table content has no column named run_id (1)`, with nothing able
to detect the mismatch first. **If this option were chosen, the migration mechanism
would be the plan**, and it would be a prerequisite plan of its own, not a section
of this one.

**Rejected: no decrement exists, and the durable state needs a migration mechanism
that does not.**

### C. Mark-and-sweep over references reachable from the projection

Mark set = every non-empty `TaskSnapshot.OutputRef` / `.ErrorRef` over every run in
the projection; sweep everything else.

*For - the mark set is already durable, with no schema change.* This corrects the
starting hypothesis I was given. Plan `20` §9 item 4 says content retention "needs
the schema-versioning work §3 B priced". **Tested and false for this option.** The
reference→run relation is already recorded twice: on the task snapshot
(`TaskSnapshot.OutputRef`/`ErrorRef`, `internal/ledger/types.go:99-100`) and
durably in `events` rows of kind `task_output_set`, whose payload is
`{"task_id","output_ref","error_ref"}` (`marshalOutputRefs`,
`internal/ledger/storage_schema.go:83-94`) alongside the row's own `run_id` column.
Measured:

```
PROBE sqlite: content rows for shared ref = 1; task_output_set events = 2
PROBE sqlite: task_output_set run_id=runA payload={"task_id":"t1","output_ref":"ref:output:8789d27f…","error_ref":""}
PROBE sqlite: task_output_set run_id=runB payload={"task_id":"t1","output_ref":"ref:output:8789d27f…","error_ref":""}
```

One row, two runs, relation on disk. No join table, no column, no migration.

*For - it is the only option that handles many-to-many correctly.* A reference stays
marked while **any** surviving task names it, so deleting run A cannot orphan run
B's live reference.

*Against - INV-AG-10 is still not safe, for a different reason.* The projection is
not the transcript. A reference the model holds is only marked while the *run
record* survives; once plan `24` makes run deletion durable, deleting a run unmarks
its references and the sweep collects bytes a transcript still points at. C is
INV-AG-10-safe only under the additional rule "never delete a run", which makes the
sweep unreachable.

*Against - the collection set is empty today. Measured by call graph, not assumed.*
Content can only be unreachable if (i) its run was deleted - impossible with
content present, §1a; (ii) `SetTaskOutput` failed after `StoreContent` succeeded -
adjacent statements in one loop (`record_results.go:40-43`); or (iii) a task's
references were overwritten with different values - `recordRunResults` runs exactly
once per run (`coordinator.go:34`) and writes each task's references once.
`recovery.go:188` does write `SetTaskOutput(ctx, runID, taskID, "", "interrupted_unrecoverable")`,
which replaces references with a non-canonical string and *would* orphan the
previous ones - but that path is `markInterruptedTasks` on a recovered run, and any
orphan it creates is bytes whose reference the model was handed earlier, i.e.
exactly what must not be collected.

*Against - cost.* A store-level delete: `storage.Store` gains a method (2
implementations + `countingStore` + `flushSQLite`), `LedgerRepository` gains one (2
implementations, 0 test-double edits - both doubles embed).

**Rejected as a build, adopted as the shape.** C is the correct mechanism and has
nothing to collect. §11 names the condition that would make it worth building.

### D. Never delete; bound by refusing new writes

Cap total content bytes; past the cap, `StoreContent` fails. INV-AG-10 survives
**by construction**, because plan `19` change #4 already made a failed content
write drop the reference rather than record it: `persistResultContent` sets
`outputRef = ""` on error (`internal/coordinator/record_results.go:74,80`), pinned
by `TestStoreContentFailureBlocksRef`.

*For:* The only bound in this list that cannot break INV-AG-10. No deletion, no
schema change, no store-level delete, no timestamp. Genuinely cheap.

*Against - it converts a non-problem into a permanent silent regression.* Past the
cap every task loses its reference forever, with no operator signal (the error is
joined into `runErr`, not surfaced as a store-full condition) and no way to reclaim
space, since nothing deletes. The failure mode is "the product quietly stops
recording history" and its trigger is a number nobody can calibrate: §1b says the
cap would need ~1.6 M tasks to matter, so any cap small enough to ever fire is a
cap set wrong.

*Against - it bounds 24% of the file* and lets `events` grow past it (§1c).

**Rejected: the cure fires only when miscalibrated, and bounds the wrong quarter.**

### E. Accept, pin, and document

Change no production behaviour. Write the two tests that make the accepted property
observable, state it in the user-facing contract, and register it as an invariant so
the next reader finds an analysis instead of rediscovering the greps.

*For:*
- Matches the measurement. §1e finds nobody harmed and two constituencies helped.
- Matches the guarantee. INV-AG-10 is a shipped promise; every option that would
  collect anything weakens it, and the one that would not (C) collects nothing.
- Honest about privacy. §1d shows the privacy case belongs to
  `<workspace>/.mivia/sessions`, not to the `content` table. Retention here would
  buy a claim the product could not keep.
- Correct scope. §1c shows the real question is history retention (runs + events +
  content, 2.6 KB/task) and it needs the run-lifetime policy this plan does not
  have.
- Cheap and reversible: one test file, one comment, one doc section, one invariant row.

*Against:*
- It leaves an unbounded store. Named, measured, and stated - which is the whole
  content of the objection.
- It leaves INV-AG-12 asserting a claim plan `24` is about to falsify ("on SQLite it
  deletes nothing from disk at all"). §8 fixes that on top of `24`'s own amendment.
- The word "retention" in a roadmap looks unanswered. §11 states exactly what would
  answer it.

**DECISION: E.**

Reasoning, in the order the evidence forced it. A and C both break INV-AG-10 - A on
a timer nobody can set, C the moment run deletion becomes durable - which is the
same failure that killed plan `20`'s A″, and the same reason plan `20` §3 A was
rejected ("its owning run" is undefined for a content-addressed key). B needs a
decrement that does not exist and durable state the schema cannot take without a
migration runner nobody has written. D is the only INV-AG-10-safe bound and is a
regression waiting for a mis-set constant. That leaves E - and E is not a
concession, because the measurements say there is nothing to reclaim (§1b), the
constituencies say permanence is a feature (§1e), the privacy case belongs to a
different file tree (§1d), and the mechanism that *would* be correct has an empty
collection set (§C). Plan `20` reached this shape and was right to; the difference
here is that C, the one option worth revisiting later, is now priced at *no
migration* rather than at the schema-versioning work plan `20` assumed - which is
the single most useful thing this plan contributes.

## 4. Blast radius and changes

**LOW.** No production behaviour changes. Nothing in `internal/storage/store.go` or
`StorageLedgerRepository.DeleteRun` - the two files plan `24` is editing.

| # | File | Current lines | Change |
|---|---|---|---|
| 1 | `internal/ledger/content_retention_test.go` (**new**) | - | The two §7 tests. New file so no existing file grows |
| 2 | `.mivia/invariants.md` | 76 | New `INV-AG-14` row; amend `INV-AG-12`; amend `INV-AG-10`'s note. Exact text in §8 |
| 3 | `Makefile` (`invariants:` target, line 131) | - | Add the two test names to the `-run` regex. Verified neither is selected today |
| 4 | `internal/ledger/memory_claims.go` | 54 | **DECISION OPEN.** A contract comment on `StoreContent` recording that nothing reclaims what it writes, and why (many-to-many). ~8 lines. The equivalent comment on `internal/storage/store.go`'s `PutContent` is deliberately **not** proposed: that file is 468/500 and plan `24` is splitting it |
| 5 | `docs/product/agent.md` | 173 | Two bullets in "Execution-history tools" per §7. Owned doc (`docs/OWNERS.yaml:18-20`, owner `product`) |

Not touched, deliberately: `internal/storage/**` (plan `24`),
`internal/ledger/storage.go` (plan `24`), `internal/cli/ledger_tools.go` (451/500,
and no tool text changes), `internal/coordinator/**`.

**Structure-gate budget.** `python3 scripts/check_go_structure.py --strict --all`
exits 0 on the tree today (run, exit=0). Non-test files ≤500 soft / 800 hard,
tests ≤800 soft / 1200 hard, functions ≤80 soft / 120 hard
(`.mivia/policy/go-structure.json`). Measured, and why each new thing goes where it does:

| File | Now | After | Ceiling |
|---|---|---|---|
| `internal/ledger/content_retention_test.go` (new) | 0 | ~130 | 800 testSoft |
| `internal/ledger/memory_claims.go` | 54 | ~62 | 500 soft |
| `internal/ledger/ledger_test.go` | 729 | 729 | 800 testSoft - **only 71 lines of headroom, which is why change #1 is a new file and not an append here** |
| `internal/ledger/storage_test.go` | 724 | 724 | 800 testSoft - same reason |
| `internal/ledger/memory_events_test.go` | 220 | 220 | 800 testSoft |

`internal/cli` additionally caps **every** file including tests at 800 lines
(`maxFileLines`, `internal/cli/structure_test.go:18`); this plan adds no file
there. No function in change #1 approaches 80 lines (the larger test is a
two-case table).

**Migration and compatibility.** No schema change, so the table is short and every
row is "nothing happens" - which is the point of choosing E over A/B/C.

| Situation | Result |
|---|---|
| Database written by an earlier version, read by this version | Unchanged. No column is added, named, or read; `PRAGMA user_version` stays 0 (measured) |
| Database written by this version, read by an earlier version | Unchanged. Byte-identical schema and writes |
| Database written by this version, read by this version | Unchanged |
| Database written before plan `24`, read after `24` **and** this plan | `24`'s tombstone/replay concern only; content is untouched by both, so this plan adds no interaction |
| Memory backend (default) | Unchanged. No timestamp map, no cap, no clock dependency |
| Existing config files | Unchanged. **No new config key.** A `content_retention_days` key is exactly what §3 A would have needed and is deliberately absent |
| Existing session transcripts | Unchanged, and the reason A/C are rejected: their references must keep resolving |

**Interaction with plan `24`, stated as a constraint on the implementer.** Change
#1's tests assert that content survives `DeleteRun`. That is true today (measured)
and remains true after `24` (`24:270` adds `TestDeleteRunLeavesContentUntouched`
for the single-run case, and `24:219-222` puts "content is NEVER removed" in
`Store.DeleteRun`'s contract). This plan's tests must therefore be written so they
pass on **both** sides of `24`. The one thing to avoid is duplicating `24`'s test:
`24` proves *one* run's content survives its own deletion; this plan proves a
*shared* reference survives one of its two runs being deleted. Different hazard,
different mutation.

## 5. Implementation waves

Per `.mivia/rules/05-adlc-agentic-development-lifecycle.md` Step 1: one file per
task, a test task before each production task, reviewer every 2–3 tasks. This plan
has **no production task**, so the "test before production" rule is satisfied
vacuously and the reviewer checkpoints carry the weight instead.

**Wave 1 - make the accepted property observable**
1. `internal/ledger/content_retention_test.go` (new) -
   `TestSharedContentRefSurvivesOneRunDeletion` and
   `TestContentStoreIsNeverReclaimed`. Both **pass on first write**; see §7 on why
   that is correct here and how it is guarded.
   *Reviewer checkpoint:* confirm both tests fail under §7's mutations, not merely
   that they pass.

**Wave 2 - state the contract**
2. `internal/ledger/memory_claims.go` - change #4, if the open decision resolves to
   yes.
3. `docs/product/agent.md` - change #5.
   *Reviewer checkpoint:* rule `60` re-read. No tool `Description()` changes, so
   `TestSessionToolSurfaceIsProjectAndLanguageGeneric` is unaffected; the reviewer
   confirms the doc bullets name no storage engine or table in text that could
   later be lifted into a tool description.

**Wave 3 - register**
4. `.mivia/invariants.md` + `Makefile:131` - changes #2 and #3, in one commit
   because `scripts/validate_invariants.py` fails if a manifest test is not
   selected by the regex (verified: both new names are unselected today).

**Wave order is a correctness constraint for Wave 3 only, and for a mechanical
reason.** `scripts/validate_invariants.py` requires every backticked test in the
manifest to exist in the tree *and* to be matched by the `invariants:` regex, so
Wave 3 cannot precede Wave 1. Waves 1 and 2 are independent of each other and
either may land alone. Wave 1 is the one worth landing alone: it is the regression
guard that stops a future contributor from "finishing" run deletion by collecting
content.

## 6. API surface

**Proposed: no new or changed Go symbol.** The whole of change #1 is two ordinary
tests:

```go
// TestSharedContentRefSurvivesOneRunDeletion is the load-bearing guard. Two runs
// producing byte-identical output share ONE content row, because the reference is
// sha256(content) (internal/contentref/contentref.go). Deleting one of them must
// not collect bytes the other's reference still names (INV-AG-10).
func TestSharedContentRefSurvivesOneRunDeletion(t *testing.T)

// TestContentStoreIsNeverReclaimed pins the absence of retention: after every run
// is deleted, every reference still resolves. Runs on the default memory backend.
func TestContentStoreIsNeverReclaimed(t *testing.T)
```

Every type and method they use exists and was read, not assumed:

```go
func NewMemoryLedgerRepository() *MemoryLedgerRepository            // internal/ledger/memory.go:46
func storage.OpenSQLite(path string) (*storage.SQLite, error)       // internal/storage/store.go:232
func NewStorageLedgerRepository(store storage.Store) *StorageLedgerRepository // internal/ledger/storage.go:56

// LedgerRepository, internal/ledger/repository.go
CreateRun(ctx context.Context, key string, snapshot RunSnapshot) error   // :27
CreateTask(ctx context.Context, snap TaskSnapshot) error                 // :44
SetTaskOutput(ctx context.Context, runID, taskID, outputRef, errorRef string) error // :68
DeleteRun(ctx context.Context, runID string) error                       // :84
StoreContent(ctx context.Context, ref string, data []byte) error         // :105
LoadContent(ctx context.Context, ref string) ([]byte, error)             // :109

func Reference(kind string, data []byte) string                     // internal/ledger/reference.go:24
const RefKindOutput = contentref.KindOutput                         // internal/ledger/reference.go:15
var ErrContentNotFound = errors.New("content not found")            // internal/ledger/repository.go:19
```

Compile-critical details confirmed by reading the structs, in the spirit of `21`'s
correction C1 (a proposed change that would not have compiled):

- `RunSnapshot.Status` is `RunStatus`, not `string` - a test must write
  `Status: RunStatusCreated` (`internal/ledger/types.go:47-57`).
- `TaskSnapshot.Status` **is** `string` - the asymmetry is real, so a test must
  write `Status: string(TaskStatusQueued)` (`internal/ledger/types.go:94`).
- `StoreContent` returns `nil` without storing for empty data
  (`memory_claims.go:35-37`; `internal/storage/store.go:440-442`), so the test
  payload must be non-empty or the assertion is vacuous.
- `Reference` returns `""` for empty data or an unknown kind
  (`internal/contentref/contentref.go:42-48`), so the test must assert the minted
  reference is non-empty before using it.
- `SetTaskOutput` returns `ErrClosed` after `CloseRun` (`memory.go:279-281`), so the
  test must set outputs before closing, or not close.

**NOT PROPOSED - the surface option C would need, recorded so the next plan does
not re-derive it.** Nothing below exists; do not implement it under this plan.

```go
// internal/storage - Store would gain:
//   DeleteContent removes rows by reference. Refs absent from the store are
//   ignored, so a sweep is idempotent.
DeleteContent(ctx context.Context, refs []string) error
//   ListContentRefs enumerates every stored reference. Needed because the sweep
//   is computed as (all refs) minus (refs reachable from the projection).
ListContentRefs(ctx context.Context) ([]string, error)

// internal/ledger - LedgerRepository would gain:
//   PruneUnreferencedContent removes content no surviving task record names, and
//   reports how many rows went. Reachability, never age: a model transcript is
//   durable and unbounded in age, so no age is safe (plan 23 §3 A).
PruneUnreferencedContent(ctx context.Context) (removed int, err error)
```

Every implementation that would have to gain them - the list §3 C's cost bullet is
priced from, and the reason it is priced high:

| Interface | Implementation | Site | Gains the method for free? |
|---|---|---|---|
| `storage.Store` | `*storage.Memory` | `internal/storage/store.go:78-91` | No |
| `storage.Store` | `*storage.SQLite` | `internal/storage/store.go:226-230` | No |
| `storage.Store` | `*flushSQLite` (test double) | `internal/storage/store_agent_integration_test.go:171-176` | **No - explicit delegation, all eleven methods. Plan `24` misses this one** |
| `storage.Store` | `*countingStore` (test double) | `internal/ledger/storage_catchup_test.go:16-90` | No - explicit delegation (`24:160` names it) |
| `LedgerRepository` | `*MemoryLedgerRepository` | `internal/ledger/memory.go:22-29` | No |
| `LedgerRepository` | `*StorageLedgerRepository` | `internal/ledger/storage.go:24-40` | No |
| `LedgerRepository` | `contentStoreFailingRepo` (test double) | `internal/cli/delegation_test.go:401-403` | **Yes** - embeds `*ledger.MemoryLedgerRepository` |
| `LedgerRepository` | `storeContentFailingRepo` (test double) | `internal/coordinator/record_results_test.go:17-19` | **Yes** - embeds |

Sweep verified as the complete set:
`grep -rn 'func (.*) PutContent' --include=*.go .` → four hits;
`grep -rn 'func (.*) CreateRun' --include=*.go .` → two hits.

**Rule `60`.** No tool name, `Description()`, parameter description or
`defaultAgentPrompt` changes, so
`TestSessionToolSurfaceIsProjectAndLanguageGeneric` and
`internal/tools/generic_surface_test.go` are unaffected. `ledger_read`'s
description (`internal/cli/ledger_tools.go:67-74`) is **not** touched: it already
says only that `not_found` means the content is absent, which stays exactly true
under E. Had any option changed it, the text would have had to name no storage
engine, table, SQL keyword or language - the trap `19` §12's correction records.

## 7. Verification

```bash
go build ./... && go vet ./...
go test ./internal/ledger/ -race -count=1
go test ./internal/... ./cmd/... -race
python3 scripts/check_go_structure.py --strict --all
python3 scripts/validate_invariants.py
make verify && make invariants
```

**Tests.** Both new tests **pass the moment they are written**, and that is the
correct outcome, not a defect in the plan: E changes no behaviour, so its tests are
regression guards rather than RED tests. Plan `21`'s correction C1 established the
discipline - a green guard must be justified by its mutation, never by its
assertion - so each one below states the mutation that makes it fail, and §5 Wave 1's
reviewer checkpoint is to run those mutations rather than to observe a pass.

- `TestSharedContentRefSurvivesOneRunDeletion` - **the load-bearing one.** Two runs
  each store the *same* payload, so `ledger.Reference` mints one reference and one
  row exists; both task records are given it; run A is deleted; run B's reference
  must still resolve to the original bytes. Table-driven over **both** backends: the
  default memory repository and a `StorageLedgerRepository` over a
  `t.TempDir()` SQLite file. This is the §1e/§2 hazard as a test, and it is the
  guard any future sweep must pass. Passes today - measured:
  `PROBE shared ref: runA.OutputRef == runB.OutputRef -> true`,
  `PROBE after DeleteRun(runA), runB ref resolves: len=43 err=<nil>`,
  `PROBE sqlite: content rows for shared ref = 1`.
- `TestContentStoreIsNeverReclaimed` - the **default (memory) backend** behaving as
  specified, per §7's requirement. K runs each with distinct content; delete every
  run; assert all K references still resolve and none returns
  `ErrContentNotFound`. Pins §1a *and* §1b as one observable: it fails under any
  reclamation, whether triggered by deletion, age, count or size.

**Existing tests that must keep passing, and are load-bearing to E.** They are the
reason E needs no new negative test for INV-AG-10:
`TestModelVisibleOutputRefResolves`, `TestModelVisibleErrorRefResolves`
(INV-AG-10), `TestLedgerReadWorksOnMemoryBackend`,
`TestLedgerReadReportsNotFoundForAbsentContent` (INV-AG-12 - the second is the one
a sweep would turn into a flake, since `not_found` would stop meaning "the bytes
were never stored").

**Mutation proofs.** Each mutation is observable by the single named test, and the
test renders the thing the mutation changes - the check `21`'s correction C4
introduced after a mutation proof named a test that could not see its own claim.

| # | Mutation | Test that MUST fail |
|---|---|---|
| 1 | Make `MemoryLedgerRepository.DeleteRun` also `delete(m.content, ref)` for the run's task references | `TestSharedContentRefSurvivesOneRunDeletion` (memory case) |
| 2 | Make the durable `DeleteRun` also delete `content` rows for the run's references (i.e. reject plan `24`'s §6 contract) | `TestSharedContentRefSurvivesOneRunDeletion` (sqlite case) |
| 3 | Add any age sweep to `StoreContent` or `LoadContent` - evict on read, or delete rows older than N | `TestContentStoreIsNeverReclaimed` |
| 4 | Add a count or byte cap that evicts the oldest entry on overflow (§3 D's rejected eviction variant) | `TestContentStoreIsNeverReclaimed` |
| 5 | Make `StoreContent` refuse to overwrite/re-register an existing reference such that a second run's store is a no-op **and** the row is attributed to run A only | `TestSharedContentRefSurvivesOneRunDeletion` - the test asserts both task records carry the same reference and that it resolves after A is gone |
| 6 | Make `LoadContent` require a surviving run for the reference | `TestSharedContentRefSurvivesOneRunDeletion` (both cases) |

Mutations 1, 2 and 6 must be recorded with `Regression: INV-AG-14`.

**Why there is no mutation for "add a bound".** A refusal-to-store cap (§3 D) is
invisible to both tests below their thresholds and would need a test that
configures the cap - which requires the config key E deliberately does not add.
Stated here rather than covered by a test that could not see it.

**Docs.** `docs/product/agent.md`, "Execution-history tools", two bullets:

- Recorded content is **never deleted and has no size limit**. A reference resolves
  for as long as the execution history exists - including after the run that
  produced it is gone, and, when a durable history is configured, in later
  processes. That permanence is deliberate: it is what makes a reference in a
  reopened session's transcript still answerable.
- The same bytes are also kept in the session transcript, so removing recorded
  content would not remove the material. Treat the execution history as retained,
  not as a deletion path.

Both sentences are language- and stack-neutral and name no storage engine, table or
SQL keyword, so they remain safe if ever lifted toward a tool description.

## 8. Invariant registration

**Id - resolve it mechanically, do not trust this document.**
`.mivia/invariants.md` holds `INV-AG-1`…`INV-AG-7`, `INV-AG-9`, `INV-AG-10`,
`INV-AG-11`, `INV-AG-12`. `INV-AG-8` is a **gap, not a free slot** - do not reuse it
(plan `20` §8). `INV-AG-13` is claimed **twice** by concurrent plans:
`.mivia/plans/24-durable-run-deletion.md:300` (durable run deletion) and
`.mivia/plans/22-idempotent-spawn-fingerprints-the-work.md:662,667` (idempotency
scope). Both assert 13 is free; both were right when written.

**Rule for the registering commit:** re-read `.mivia/invariants.md`, take the lowest
unused `INV-AG-N` with `N > 12`, and use it. **This plan writes `INV-AG-14`** because
that is the answer if exactly one of `22`/`24` lands first - but if both land it is
15, and if neither does it is 13. Never leave two rows claiming one id, and never
resurrect `INV-AG-8`. `make validate-invariants` does **not** catch a duplicate id
(`scripts/validate_invariants.py` checks only that backticked test names exist and
are selected by the Makefile regex), so this is a human check, which is why it is
written as a rule rather than a number.

Proposed row, Agent Loop table:

```
| INV-AG-14 | Safety | Recorded content is retained unconditionally: nothing reclaims it on either backend, so a reference resolves for as long as the store exists - after its run is deleted, and on the durable backend after the process that recorded it. Deliberate, not an omission: references are content-addressed, so one stored row is the target of references held by several runs and "its owning run" is undefined; a model transcript is persisted verbatim and restored on reopen, so no age is safe and no run is authoritative; and the one correct mechanism (sweeping content unreachable from every surviving task record) has a measured empty collection set, because the only path that deletes a run runs before any content exists. Retention here is a disk-space concern and NOT a privacy control: the same bytes are kept unredacted in the workspace session transcript, which has no age limit for a user-named session. Content is measured at ~636 B per task against ~1141 B of events for the same task, so a content-only sweep would reclaim about a quarter of the store. Severity analysis, the four rejected schemes, and the reopening condition are in plan 23 §1b, §1d, §3 and §11 | `TestSharedContentRefSurvivesOneRunDeletion`, `TestContentStoreIsNeverReclaimed`, `TestLedgerReadReportsNotFoundForAbsentContent`, `TestModelVisibleOutputRefResolves` | 2026-07-30 (plan 23 → accept and document) |
```

Why those pins, and why none is vacuous:

- `TestSharedContentRefSurvivesOneRunDeletion` and `TestContentStoreIsNeverReclaimed`
  are new and observe the property directly; §7's mutation table gives six
  mutations, each failing a named test that renders the changed thing.
- `TestLedgerReadReportsNotFoundForAbsentContent` (`internal/cli/ledger_tools_test.go`,
  already pinned by INV-AG-12) is the pin that any reclamation *breaks by
  implication*: with a sweep, `not_found` stops meaning "these bytes were never
  recorded" and starts meaning "or they were collected", which is the corollary
  `19` §3 exists to protect.
- `TestModelVisibleOutputRefResolves` (INV-AG-10) is listed because permanence is
  how that guarantee survives a restart; if it ever conflicts with a retention row,
  INV-AG-10 wins.

**Rows to amend.**

`INV-AG-12` currently reads, in part: *"content outlives its run - `DeleteRun`
removes no content on either backend, on SQLite it deletes nothing from disk at
all, and there is no retention."* Two problems. The middle clause becomes **false**
when plan `24` lands, and plan `24` §8 already proposes replacing it with
*"`DeleteRun` removes the run's events but deliberately removes no content
(INV-AG-13), so content still outlives its run and there is still no retention."*
This plan amends whichever version is in the file at the time, to point the
retention claim at its own row rather than restating it:

> …and content outlives its run: `DeleteRun` deliberately removes no content on
> either backend, and there is no retention - see INV-AG-14 for why, and for the
> schemes that were rejected.

`INV-AG-10` - **amend by one clause, not restructure.** It currently guarantees
that a reference handed to the model resolves. Append, after the existing text:

> The guarantee is unbounded in time on purpose: nothing reclaims recorded content,
> so a reference in a reopened session's transcript still resolves (INV-AG-14). Any
> future reclamation is subordinate to this row.

That clause is the whole reason §3 rejected A, C and D's eviction variant, and
putting it in INV-AG-10 is what stops the next reader from adding a sweep and
discovering the conflict afterwards. Its test list is unchanged.

`INV-AG-9`, `INV-AG-11`, `INV-SEC-1`, `INV-SEC-2` - unchanged.

**`Makefile:131`.** Both new names must be added to the `invariants:` `-run` regex.
Verified mechanically rather than by eye - `scripts/validate_invariants.py`
extracts the regex and requires `regex.search(name)` for every backticked manifest
test, and both come back unselected today:

```
TestSharedContentRefSurvivesOneRunDeletion False
TestContentStoreIsNeverReclaimed False
```

`TestLedgerReadReportsNotFoundForAbsentContent` and
`TestModelVisibleOutputRefResolves` are already selected (`TestLedgerRead` and
`TestModelVisibleOutputRefResolves` are both alternatives in the regex). Because
the script fails on an unselected name, change #3 lands in the same commit as
change #2, and both after Wave 1.

## 9. What this does NOT solve

Flat, and none of it is hedged.

1. **The store still grows without limit.** On the opt-in durable backend it grows
   forever, on the default backend for the process lifetime. Measured, stated,
   documented, not fixed. If a user's store does become a problem, this plan gives
   them nothing but a number.
2. **`events` and run records have no retention either**, and they are 1.8× the
   content (§1c). This plan does not address them and its title should not be read
   as if it did. History retention is the plan that would.
3. **No deletion path for a user's data.** Rule `10`'s PDPL posture asks for one.
   The blocker is not the `content` table: it is
   `<workspace>/.mivia/sessions/**`, which keeps the same bytes unredacted with no
   age limit for user-named sessions (§1d). A deletion path is a plan about session
   history, and it would have to cover the ledger as one of its two targets.
4. **No migration mechanism.** Still no `PRAGMA user_version` (measured: 0), no
   version table, no migration runner. This plan needs none, and by choosing E it
   declines to build one - so option B stays blocked behind it, and plan `20` §3 B
   stays blocked behind it too.
5. **The equality oracle is untouched.** INV-AG-12 stands: `ledger_read` resolves
   any well-formed reference any caller presents. Permanence widens the oracle's
   window from "recently" to "ever", which is a severity note on INV-AG-12 rather
   than something this plan changes.
6. **Content is written unredacted.** `persistResultContent` stores `r.Output` and
   `r.Err.Error()` raw (`internal/coordinator/record_results.go:72,78`); only the
   read path redacts, and only under a configured policy. Redacting on write is a
   separate question with a separate trade-off (it would change what a resolved
   reference means), and plan `10`'s decision is that redaction is configuration.
7. **A sub-agent is not isolated from its parent's content.** Plan `20` §1b, still
   true, untouched.
8. **No operator surface.** There is no `mivia diagnostics` command,
   `NewDiagnostics` has no production caller, and `mivia doctor` never opens the
   store. So there is no place to report store size from either - which is worth
   knowing before anyone proposes "just show the user how big it is".
9. **Option C is not built.** Its mechanism is designed and priced in §3 and §6 and
   nothing implements it. That is a deliberate deferral with a named trigger (§11),
   not an oversight.

## 10. Plan scorecard

| Criterion | Verdict |
|---|---|
| Compiles (no import cycles) | PASS - one new test file in `internal/ledger`, which already imports `internal/storage`; §6 lists every symbol it uses with its definition site, and the compile-critical type asymmetries (`RunSnapshot.Status` is `RunStatus`, `TaskSnapshot.Status` is `string`) were read rather than assumed |
| No breaking API change | PASS - no exported symbol added, changed or removed. Contrast plan `24`, which deliberately scores FAIL here |
| Testable in isolation | PASS - both tests use a fresh `MemoryLedgerRepository` and a `t.TempDir()` SQLite file; no clock injection needed because nothing is age-dependent, which is itself a consequence of rejecting §3 A |
| Backward-compatible config | PASS - **no new config key.** A retention-days key is the surface §3 A would have needed |
| Backward-compatible DATA | PASS - §4's table: no schema change, no column named, no write changed, `PRAGMA user_version` stays 0 |
| Every function has a test | PASS vacuously and honestly - the plan adds no function. Changes #2–#5 are a comment, a doc, a manifest row and a Makefile regex, none of which is testable code; the manifest row and regex are validated by `scripts/validate_invariants.py` |
| Security tests present | PASS - `TestSharedContentRefSurvivesOneRunDeletion` is a negative test in the `secure-change` sense: it asserts that a plausible "cleanup" does **not** happen, which is the failure mode plan `20`'s A″ shipped as a regression. §1d's privacy claim is deliberately *not* asserted by a test, because the plan's position is that retention is not a privacy control |
| `--strict` structure gate | PASS - `python3 scripts/check_go_structure.py --strict --all` exits 0 today; §4's table shows every ceiling with headroom, and records that `ledger_test.go` (729/800) and `storage_test.go` (724/800) are why change #1 is a new file |
| Rule `60` satisfied | N/A - no tool name, `Description()`, parameter description or default-prompt text changes; `TestSessionToolSurfaceIsProjectAndLanguageGeneric` is unaffected. The doc bullets in §7 are checked against the rule anyway and name no engine, table, SQL keyword or language |
| Invariants honest | PASS **only with the §8 amendments** - INV-AG-12's "on SQLite it deletes nothing from disk at all" is about to be falsified by plan `24`, and INV-AG-10 currently states its guarantee without stating that permanence is what makes it hold |
| Cost proportionate to harm | PASS - §1e finds nobody harmed and two constituencies helped, so the proportionate response is a test, a paragraph and a row. Any of A–D would have cost an exported interface, a migration mechanism, or INV-AG-10 |

## 11. Rollback criterion

**What kills this plan** - i.e. what makes E the wrong answer and reopens the
options:

- **A measurement contradicts §1b.** If a real store is found where `content`
  materially exceeds `events`, or where absolute size is an operator complaint
  rather than an extrapolation, re-derive from **C**. Re-measure with §1b's method
  (per-task logical bytes for both tables plus the file size) before touching
  anything - the whole decision rests on that ratio.
- **Run deletion becomes user-facing.** Plan `24` §9 keeps `DeleteRun` reachable
  only from the create-failure unwind. If a `mivia`-level or model-level delete
  surface lands (plan `15`'s territory), a user deleting a run and finding its
  output still resolvable is a legitimate defect, and **C** becomes the answer - with
  the transcript problem solved explicitly, because a shared reference must still
  survive and §3 C's INV-AG-10 objection does not disappear just because a user
  asked.
- **A real deletion requirement appears** (a user, or PDPL). Then the plan is
  session-history deletion covering the ledger *and*
  `<workspace>/.mivia/sessions/**` together (§1d, §9 item 3). Do not accept a
  ledger-only sweep as satisfying it; that would be exactly the overclaim §1d
  refuses.
- **A migration mechanism lands** - `PRAGMA user_version` plus a runner. That
  unblocks **B** and plan `20` §3 B, and makes a durable per-reference record
  (refcount or run join) discussable. It does not by itself make B correct: the
  missing decrement (§3 B) is a lifecycle problem, not a schema problem.
- **The transcript stops being durable.** If session persistence gained an age
  limit that applied to user-named sessions too, the "no safe age" argument against
  **A** weakens and A becomes arguable at an age above that limit. As of today
  `expiredAutoSaves` prunes only machine-minted names
  (`internal/chat/autosave_retention.go:76-96`, `IsAutoSaveName` `:21-31`), so this
  has not happened.

**What must survive in every case.** Wave 1's two tests. Whatever retention scheme
a future plan builds, a shared content reference must not be collected while any
holder survives, and the guard for that is cheaper to keep than to rediscover. If
this plan is rejected wholesale, land Wave 1 anyway.
