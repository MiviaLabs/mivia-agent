# 19 — Make execution references resolvable

**Status:** IMPLEMENTED 2026-07-30. All decisions closed (§4 **B**, §7 **enum**).
Supersedes the 2026-07-30 proposal draft, whose §1 premise was overtaken by
`49970ad` the same day and whose Tool B is unshippable as specified.

**Corrections found during implementation** (the plan was wrong about these):

- **§1 undercounts the minters: there were five, not three.**
  `internal/runtime/dispatcher.go`'s `reference()` and `internal/ledger/memory.go`'s
  `normalizeReference` were both missed. The dispatcher's refs were dead pointers of
  the same class as `multi_step.go`'s and were removed on the same reasoning; run-level
  error refs in `dispatch.go`/`delegate.go`/`join_run` were dead too and were removed.
- **§9's location for the minter does not compile.** `internal/runtime` must reach the
  minter and cannot import `internal/ledger` — `internal/storage`'s in-package test
  imports `internal/agent`, which closes a cycle through `runtime` → `ledger` →
  `storage`. `go build` and `go vet` do not see it; `go test ./...` does. The minter
  therefore lives in a stdlib-only leaf, `internal/contentref`, which `internal/ledger`
  re-exports under the §9 names.
- **§5 is wrong about how old refs are reported.** A pre-change *output* ref was 16 hex
  and is rejected as malformed, not `not_found`. Only pre-change *error* refs (full
  digest) report `not_found`.
- **§6's `MaxResultBytes` cap does the opposite of what it says.** It is min'd against a
  4000-char session cap and bounds the *marshalled envelope*, so setting it to the
  content cap guaranteed the framing was truncated away. Fixed by ordering `content`
  last and dropping the tool-level override.
- **§7's enum is 12 values, not the vocabulary implied.** `task_queued` is never
  emitted; offering it would hand the model a filter that always returns zero rows.
- **§8's threat model is wrong.** Task `Input` never reaches the content store — only
  task output and error text do. `ledger_read` is not a read amplifier over unredacted
  input. It *is* an unscoped equality oracle over recorded content; see
  `docs/product/agent.md`.
- **§12's rule-60 guard did not cover what it claimed.** The bias patterns were
  Go/module-only, so a description naming SQL or a table name passed. Patterns added.
- **Change #8 was already partly done** — `internal/cli/session_tool_surface_test.go`
  existed and was extended rather than created.
**Date:** 2026-07-30
**Depends on:** `13` §6 (content store — implemented, `49970ad`).
**Blocks:** nothing. **Composes with:** `18` (both add agent tools; no shared code).
**Blast radius:** MEDIUM — §5 changes a reference format already persisted in
existing ledgers. §5 is the load-bearing section.
**Commits:** `fix(agent): mint one canonical content reference`,
`feat(agent): resolve execution references and event history`

---

## 1. The defect

`spawn_agent` and `dispatch_tasks` hand the model an `output_ref` for every
completed sub-task. **Every one of those refs is unresolvable**, and not for the
reason the draft assumed. The content is stored — under a different key.

Three independent ref minters exist. Only one writes to the content store, and it
disagrees with the one the model sees:

| Site | output ref | error ref | writes content? |
|---|---|---|---|
| `internal/coordinator/record_results.go:105` | `digest[:8]` → **16 hex** | `digest[:]` → 64 hex | **yes** (`persistResultContent`, `:67-74`) |
| `internal/cli/orchestrate_lifecycle.go:21` | `digest[:]` → **64 hex** | `digest[:]` → 64 hex | no |
| `internal/subagents/multi_step.go:134-136` | 64 hex | 64 hex | no |

`orchestrationReference` populates `modelTaskResult.OutputRef`
(`orchestrate_lifecycle.go:46`) and `dispatchTasksTool.encodeResults`
(`internal/cli/dispatch.go:264`). So the agent copies a 64-hex output ref out of a
tool result while the coordinator filed those same bytes under the first 16 hex of
the same digest.

> The bytes are present, the ref is well-formed, and the lookup misses. Every
> time, for every output ref, deterministically.

Error refs happen to agree — both use the full digest — which makes it worse, not
better: the surface behaves correctly for errors and fails silently for outputs.

Three supporting defects found while confirming this:

- **`multi_step.go`'s refs are genuinely dead.** Nothing writes them, under any key.
- **Storage failures are silent.** `persistResultContent` discards both results
  (`_ = c.repo.StoreContent(...)`, `record_results.go:69,72`). A task can carry an
  `OutputRef` whose bytes were never written, with no error anywhere.
- **A ref is used as an error message.** `internal/coordinator/recovery.go:283-284`
  does `results[i].Err = errors.New(task.ErrorRef)`, so a resumed run reports the
  literal string `ref:error:9f2c…` as its failure reason.

And the draft's actual point: **no tool can resolve a ref even when it is
correct.** `LoadContent` exists (`internal/ledger/repository.go:109`) and is
reachable from no agent surface.

`orchestrate_lifecycle.go:35-37` documents the intent honestly — refs "remain
stable correlation IDs, but do not retrieve persisted content." This plan is the
decision to stop shipping a correlation ID that looks like a handle.

## 2. Corrections to the proposal draft

**The premise expired before the ink dried.** The draft's §1 says the content
store "writes the bytes to a `content` table, but no tool exposes it," and its §3
claims `ref:output:` refs prove "content never stored." `persistResultContent`
landed in `49970ad` on 2026-07-30 — the same day. Content *is* stored for the
coordinator path. The real defect is the key mismatch in §1, which the draft's own
tool would have misdiagnosed: `ledger_read` returning `ErrContentNotFound` would
have read as "the ref is a dead pointer" when the bytes were in the table under a
different key.

**Three schema facts in the draft are wrong**, each of which would have produced a
tool that silently returns nothing:

- **`storage_schema.go` is not the DDL.** The draft points the tool description at
  it as "the canonical schema." `internal/ledger/storage_schema.go` describes JSON
  event payload shapes. The DDL is inline in `OpenSQLite`
  (`internal/storage/store.go:253-275`); there is no schema file and no version table.
- **The column lists are incomplete.** `events` also has `id` and `created_at`;
  `content` also has `created_at`. `rowid` is load-bearing — `Changes`
  (`store.go:344-376`) uses it as the append cursor.
- **`kind='task_completed'` matches zero rows.** The eight real `events.kind`
  values are the constants at `storage_schema.go:13-22` (`run_created`,
  `run_status_changed`, `task_created`, `task_status_changed`, `task_output_set`,
  `task_attempt`, `lifecycle_event`, `run_closed`). `task_completed` is a *ledger*
  `LifecycleEvent.Kind` nested **inside** a `lifecycle_event` payload
  (`record_results.go:51`, `Kind: "task_" + newStatus`). The draft's headline
  example query returns nothing.

**Scope, decided:** both tools ship unprivileged and available to every agent,
sub-agents included. Restriction and configuration are later phases.

## 3. Invariant to establish

> A reference handed to the model resolves, or it is not handed to the model.

Corollaries:

- **One minter.** A reference format with three implementations is a defect
  generator; §1 is the proof. Minting moves to exactly one function.
- **`not found` means the bytes are absent** — never "you asked with the wrong key
  shape." Today the tool could not distinguish these, which makes its most
  valuable answer (proving a dead pointer) unreliable.
- **A failed store is an error, not a silence.** A ref must not be recorded on a
  task whose content write failed.

## 4. Decision: no freeform SQL — DECIDED B

### A. `ledger_query(sql, limit)` as drafted

Guard: reject anything not starting with `SELECT`, plus `PRAGMA query_only=ON` and
a row cap.

*Against — defeated by the first payload.* Verified empirically against this
module's own driver (`modernc.org/sqlite` v1.54.0, SQLite 3.53.3):

```
db.Query("SELECT 1; DROP TABLE u")        → err=nil, table u dropped
db.Query("SELECT ?; DROP TABLE u", 5)     → err=nil, table u dropped
```

`database/sql` on modernc takes an explicit multi-statement fallback branch in
both `exec()` and `query()` and steps every statement. Bound placeholders do not
help. A prefix check inspects statement #1 while the executor runs #1..N — the
exact bug behind CVE-2025-66335 (Apache Doris MCP), the Apache Pinot MCP advisory,
the archived reference Postgres MCP server, and fourteen SQL MCP servers broken in
2026, one to RCE.

The rest of the guard fails too. `PRAGMA query_only=ON` is undone by
`PRAGMA query_only=OFF`, and it is per-connection against an 8-connection pool
(`store.go:243-244`) — set it on one conn and writes proceed on the other seven.
Opening `mode=ro` does not contain the query either: `ATTACH DATABASE` escapes a
read-only handle, yielding arbitrary file write plus an exfiltration channel that
never passes through the agent's output. `modernc.org/sqlite` does not expose
`sqlite3_set_authorizer`, the one mechanism free of parser/engine divergence. And
context cancellation does not bound the query — measured, a statement that emits
one cheap row first overran a 2 s deadline by **305 s**, because modernc installs
no interrupt watchdog on `rows.Next()`.

*Also — inert by default.* The default backend is memory
(`internal/config/load.go:46-48`); SQLite is opt-in, and both construction sites
fall back to memory with only a stderr warning on open failure
(`internal/cli/dispatcher.go:33-51`).

*Also — the accuracy case is weak on its own merits.* Freeform text-to-SQL scores
21.3% on Spider 2.0 for an o1-preview code-agent framework; a controlled 2026
study across three frontier models measured 45.5–50.5% schema-only, rising to only
67.7–68.7% with a semantics document, with the three models statistically
indistinguishable. Over 80% of failures are schema grounding. §2 shows the draft's
own example query was wrong about `kind` values; an agent will be wrong the same
way, with no structured surface to recover against.

### B. Parameterized read tools over `LedgerRepository`

No SQL. Two tools calling the read-only subset of the existing repository
interface — the queries are written here, the agent supplies bound parameters only.

*For:* Removes the attack surface rather than guarding it. Needs none of A's
mitigations — no read-only connection, no `SQLITE_LIMIT_ATTACHED`, no
multi-statement lexer, no out-of-band timeout, no accessors punched through
`storage.SQLite.db`/`.path` (both unexported, no accessor today). **Works on both
backends**, including the default memory one, because it goes through the
interface. Immune to §2's schema errors — no `kind` string to guess, no column
names to hallucinate.
*Against:* Fixed query shapes. An unanticipated investigation needs a code change.

### C. Fix the refs only; ship no tool

*For:* Smallest change; removes the lie without adding surface.
*Against:* Leaves `LoadContent` unreachable, so the ref still isn't a handle —
just a correct one nobody can dereference. Solves half the problem.

**DECIDED: B.** A is rejected on empirical security grounds and would ship a tool
inert in the default configuration. The accuracy and security evidence point the
same way, which makes the call easy. C is what B degrades to if §5 fails, not a
destination.

Re-adopting A later would require: switching to `mattn/go-sqlite3` for
`RegisterAuthorizer` (costing cgo and pure-Go cross-compilation of the `mivia`
binary), a dedicated `file:…?mode=ro` handle, `SQLITE_LIMIT_ATTACHED=0` pinned per
checkout, a multi-statement rejector, and a subprocess executor for timeouts. That
is a plan of its own, not an increment on this one.

## 5. Prerequisite: one minter, one format

§4 B is pointless while §1 stands — `ledger_read` would return `not found` for
every output ref an agent can copy, violating §3's second corollary on day one.

**Canonical form:** `ref:<kind>:<64 hex>` — the full SHA-256, matching what the
model already sees and what error refs already use. Truncation to 8 bytes buys
nothing (`normalizeReference` already caps refs at 256 bytes,
`internal/ledger/memory.go:455-464`) and costs collision resistance.

`resultReferences` (`record_results.go:102-112`) and `orchestrationReference`
(`orchestrate_lifecycle.go:17-23`) collapse into one exported helper.
`multi_step.go:134-136` either uses it and stores, or stops emitting refs — §3
forbids the third option.

**Old refs stay unresolvable, honestly.** Refs already persisted under the 16-hex
form will not resolve after this change, and no migration recovers them: the
content rows are keyed by the truncated ref and the source bytes are gone. This is
acceptable — those refs do not resolve *today* either. The tool reports them as
not found rather than pretending. **Do not add a truncated-key fallback lookup**;
it would re-introduce two formats to serve refs that were never reachable.

## 6. What the tools are

`inspect_agents` (`internal/cli/orchestrate.go:296`) already reports run and task
status, so run/task listing is not a gap. Adding tools for it would cost routing
accuracy — tool-inventory growth measurably degrades selection — for no new
capability. The gap is exactly two things: **event history** and **content**.

```
ledger_read(ref)                          → bytes, or an explicit not_found
list_run_events(run_id, kind?, limit?)    → ordered lifecycle events for one run
```

Both are `ExecutionRead` with a `MaxResultBytes` cap. Both call only the read-only
subset of `LedgerRepository` — `LoadContent` (`repository.go:109`) and `ListEvents`
(`repository.go:58`). No mutating method is reachable from either.

Rule `60` applies: names and descriptions must not mention Go, SQL, SQLite, table
names, or this module. These describe agent-execution history, a language-neutral
concept — the generic surface here is honest, not a workaround.

## 7. Decision: `kind` is an enum

`ListEvents` returns `[]LifecycleEvent`, whose `Kind` is `task_completed`,
`task_failed`, and so on. The storage-level `events.kind` column has the eight
different values in §2. The `kind` parameter must document exactly one vocabulary
— saying "kind" and leaving the agent to guess between two overlapping sets is
§2's failure reproduced in the tool surface.

**DECIDED: enum over the `LifecycleEvent.Kind` vocabulary.** `16` §4 records that
enums are not free — they couple the schema to the vocabulary and grow the tool
description — but a free-string typo that returns zero rows is indistinguishable
from "no such events happened," which is §3's third corollary at the tool
boundary. The coupling is the cheaper cost. Unknown values are rejected with the
accepted list, so the enum and the runtime check agree.

## 8. Security

The tools are read-only by construction (§6), so §4 A's SQL threat model does not
apply. Two exposures remain, neither closed by read-only access:

- **The ledger holds unredacted task input.** `12` §4a puts task `Input` payloads
  into the store by default, and redaction is configuration-only — a workspace
  that configures nothing redacts nothing (`10`). `ledger_read` is a read
  amplifier over that. Not a new capability (the agent already has `read_file`),
  but a new *path*, so the response goes through `internal/redact` on the same
  terms as any other tool output.
- **The ledger contains untrusted content.** Sub-agent output is model-authored
  and tool-captured; `ledger_read` returns it into a higher-level agent's context.
  That is the second leg of the lethal trifecta, supplied by construction.
  Read-only is a write control, not a confidentiality control — the Supabase MCP
  incident exfiltrated via the agent's own output channel with every read-only
  guard intact. Content returned by `ledger_read` must be framed as data, never
  as instructions.

`ledger_read` also rejects refs whose format it does not own: `ref:`-prefixed
only, known kind, exact digest length. Not a privilege boundary — `LoadContent` is
keyed lookup with no traversal — but it converts a malformed argument into an
error rather than a `not found` that reads as evidence.

## 9. API surface

`internal/ledger/reference.go`:

```go
// Reference kinds for content-addressed task results.
const (
	RefKindOutput = "output"
	RefKindError  = "error"
)

// ErrMalformedReference reports a reference that is not in canonical form.
var ErrMalformedReference = errors.New("malformed content reference")

// Reference returns the canonical reference for data: "ref:<kind>:<64 hex>".
// It returns "" for empty data.
func Reference(kind string, data []byte) string

// ParseReference splits a canonical reference into its kind and hex digest,
// returning ErrMalformedReference for any other shape.
func ParseReference(ref string) (kind, digest string, err error)
```

`internal/cli/ledger_tools.go` — the tools live here, alongside the other
repository-backed tools, so `internal/tools` gains no dependency on
`internal/ledger`:

```go
type ledgerReadTool struct {
	repo     ledger.LedgerRepository
	maxBytes int
}

type ledgerReadArgs struct {
	Ref string `json:"ref"`
}

type listRunEventsTool struct {
	repo     ledger.LedgerRepository
	maxEvents int
}

type listRunEventsArgs struct {
	RunID string `json:"run_id"`
	Kind  string `json:"kind,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

// registerLedgerTools registers the read-only ledger tools on both the
// model-visible registry and the dispatcher. Unlike registerSessionTool these
// are deliberately unprivileged, so sub-agents can call them.
func registerLedgerTools(d *runtime.Dispatcher, reg *tools.Registry, repo ledger.LedgerRepository) error
```

`registerLedgerTools` mirrors `registerSessionTool` (`internal/cli/dispatcher.go:168-180`)
— duplicate-name check, `d.RegisterTool`, then `reg.Register` — minus the
`PrivilegedTool` assertion.

## 10. Changes

| # | File | Change |
|---|---|---|
| 1 | `internal/ledger/reference.go` (new) | The single minter per §9. |
| 2 | `internal/coordinator/record_results.go:102-112` | `resultReferences` delegates to #1. Removes `digest[:8]`. |
| 3 | `internal/cli/orchestrate_lifecycle.go:17-23` | Delete `orchestrationReference`; call #1. |
| 4 | `internal/coordinator/record_results.go:67-74` | Stop discarding `StoreContent` errors; a failed write must not leave a ref on the task (§3). |
| 5 | `internal/subagents/multi_step.go:134-136` | Use #1 and store, or stop emitting refs. §3 forbids emitting an unstored ref. |
| 6 | `internal/cli/ledger_tools.go` (new) | Both tools + `registerLedgerTools` per §9. |
| 7 | `internal/cli/dispatcher.go:88-93` | Call `registerLedgerTools(d, reg, repo)` in `newSessionDispatcher`. |
| 8 | `internal/cli/session_tool_surface_test.go` (new) | Rule-60 guard over the **session-built** registry — see below. |
| 9 | `internal/coordinator/recovery.go:283-284` | Stop using `task.ErrorRef` as the error message; resolve it or report a bounded description. |
| 10 | `.mivia/invariants.md` + `Makefile:131` | Register new invariant test names in both. |
| 11 | `docs/product/agent.md` | Document both tools in language-neutral terms; state that pre-change refs do not resolve (§5). |

**Registration is constrained by construction order.** The repository is built
inside `NewSessionDispatcher` (`dispatcher.go:32-50`), which *receives* the
already-built registry — `NewDefaultRegistry` runs earlier
(`internal/cli/chat_repl.go:63`) and cannot be given a repo. So these tools cannot
join `registerDefaultTools`. Registering in `newSessionDispatcher` works and
reaches sub-agents: `restrictedRegistry` (`internal/subagents/multi_step.go:249`)
filters on the `Privileged` marker and a name denylist and passes everything else
through. Ordering is safe — `MultiStepHandler` holds the registry pointer and
calls `restrictedRegistry()` at dispatch time, so tools registered after the
handler still appear.

**Change #8 is not optional.** `generic_surface_test.go:49` builds only
`NewDefaultRegistry`; tools registered on the session path are invisible to it —
the vacuous-check trap `16` §5 documents. This is already live: `34fd01f` made
`dispatch_tasks` and `spawn_agent` descriptions include workspace-derived skill
names, and no rule-60 test covers that text. Without #8, rule `60` is unenforced
for everything this plan adds *and* for what already shipped.

**No schema migration**, no new tables, no config surface. `DisableTools` provides
the off switch.

## 11. Implementation waves

Per `.mivia/rules/05` Step 1: one file per task, test task precedes each
production task, reviewer every 2–3 tasks.

**Wave 1 — one canonical reference** (§5; no new tools yet)
1. `internal/ledger/reference_test.go` — `Reference` determinism, empty-data case,
   `ParseReference` rejects short digests / unknown kinds / missing prefix.
2. `internal/ledger/reference.go` — change #1.
3. `internal/coordinator/record_results_test.go` — add
   `TestStoreContentFailureBlocksRef`.
4. `internal/coordinator/record_results.go` — changes #2 and #4.
   *Reviewer checkpoint.*

**Wave 2 — collapse the other minters** (depends on Wave 1)
5. `internal/cli/orchestrate_lifecycle_test.go` — assert the model-visible ref
   equals `ledger.Reference`.
6. `internal/cli/orchestrate_lifecycle.go` — change #3.
7. `internal/subagents/multi_step.go` — change #5.
8. `internal/coordinator/recovery.go` — change #9.
   *Reviewer checkpoint.*

**Wave 3 — the tools** (depends on Wave 2)
9. `internal/cli/ledger_tools_test.go` — `TestLedgerReadWorksOnMemoryBackend`,
   `TestLedgerReadRejectsMalformedRef`, `TestLedgerReadRedactsOutput`,
   `TestListRunEventsRejectsUnknownKind`, result-cap behaviour.
10. `internal/cli/ledger_tools.go` — change #6.
11. `internal/cli/dispatcher.go` — change #7.
    *Reviewer checkpoint.*

**Wave 4 — close the guard gap and the loop** (depends on Wave 3)
12. `internal/cli/session_tool_surface_test.go` — change #8.
13. **End-to-end:** `TestModelVisibleOutputRefResolves` — the §12 load-bearing test.
14. `.mivia/invariants.md` + `Makefile:131` — change #10.
15. `docs/product/agent.md` — change #11.

Wave 1 alone is shippable and is option C. If Waves 3–4 are cut, the reference fix
still stands on its own merit (§13).

## 12. Verification

```bash
go build ./... && go vet ./...
go test ./internal/ledger/ ./internal/coordinator/ ./internal/subagents/ ./internal/cli/ -race -count=1
go test ./internal/... ./cmd/... -race
make verify && make invariants
```

**Tests:**

- `TestModelVisibleOutputRefResolves` — **the load-bearing one.** Run a task
  end-to-end, take the `output_ref` from the tool result the model receives, pass
  it to `ledger_read`, assert it returns the task's output bytes. Fails today.
- `TestReferenceHasSingleMinter` — every `ref:` literal in production code
  originates from `ledger.Reference`. Guards §3's first corollary structurally.
- `TestStoreContentFailureBlocksRef` — when `StoreContent` fails, no `OutputRef`
  is recorded on the task (§3, change #4).
- `TestLedgerReadWorksOnMemoryBackend` — the default backend. Proves the §4 B
  claim that the tools function without SQLite.
- `TestLedgerReadRejectsMalformedRef` — non-`ref:` input, unknown kind, and wrong
  digest length each error distinctly from `not_found` (§8).
- `TestLedgerReadRedactsOutput` — asserted under a **configured** redaction
  policy; with no policy installed the assertion passes trivially (`20`).
- `TestListRunEventsRejectsUnknownKind` — an unrecognised `kind` errors with the
  accepted values rather than returning zero rows (§7).
- `TestSessionToolSurfaceIsProjectAndLanguageGeneric` — change #8; covers
  `dispatch_tasks` and `spawn_agent` too, which no test covers today.

**Mutation proofs:**

| # | Mutation | Test that MUST fail |
|---|---|---|
| 1 | Restore `digest[:8]` in `resultReferences` | `TestModelVisibleOutputRefResolves` |
| 2 | Re-add a second local ref formatter | `TestReferenceHasSingleMinter` |
| 3 | Restore `_ = c.repo.StoreContent(...)` | `TestStoreContentFailureBlocksRef` |
| 4 | Gate `ledger_read` on the SQLite backend | `TestLedgerReadWorksOnMemoryBackend` |
| 5 | Return `not_found` for a malformed ref | `TestLedgerReadRejectsMalformedRef` |
| 6 | Return content without redaction | `TestLedgerReadRedactsOutput` |
| 7 | Accept an unknown `kind` and return zero rows | `TestListRunEventsRejectsUnknownKind` |
| 8 | Put a table name or `SELECT` in a tool description | `TestSessionToolSurfaceIsProjectAndLanguageGeneric` |

Mutation #1 is the regression proof for §1 and must be recorded with
`Regression: INV-XXX` per `20`.

**Docs:** `docs/product/agent.md` — both tools in language-neutral terms, plus the
§5 statement that references minted before this change do not resolve.

## 13. Plan scorecard

| Criterion | Verdict |
|---|---|
| Compiles (no import cycles) | PASS — tools live in `internal/cli`, which already imports `ledger`; `internal/tools` gains no new edge |
| No breaking API change | PASS internally; **`resultReferences` output format changes** — see §5 on old refs |
| Testable in isolation | PASS — both tools take a `LedgerRepository`; memory backend is the test double |
| Backward-compatible config | PASS — no new config; `DisableTools` is the off switch |
| Every function has a test | PASS — Waves 1–4 pair each production task with a preceding test task |
| Security tests present | PASS — malformed-ref rejection and redaction-under-policy are both negative tests (`secure-change`) |
| Rule `60` satisfied | PASS **only with change #8** — the existing guard cannot see these tools |

## 14. Rollback criterion

Kill this plan if:

- **§5 cannot land.** Without one canonical format the tools ship against a
  known-broken key space and `not found` stops being evidence of anything. Ship
  Wave 1 alone (option C) rather than tools over a format that lies.
- **The fixed query shapes prove insufficient in practice.** If real
  investigations routinely need a shape the tools lack, that argues for more
  parameterized tools, not for revisiting A. A returns only under the full
  mitigation stack in §4, as its own plan.
- **`ledger_read` becomes a routine bulk-read path** rather than an investigation
  tool. It is a read amplifier over unredacted task input (§8); if agents pull
  ledger content by habit, withdraw it and expose event metadata only.

In each case the correct action is to remove the tools and keep Wave 1. The
reference fix stands alone: it removes a handle that was never a handle, which is
worth doing whether or not anything can dereference it.
