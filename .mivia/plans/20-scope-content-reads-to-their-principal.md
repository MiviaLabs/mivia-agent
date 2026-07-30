# 20 — Scope content reads to their principal

**Status:** PROPOSED 2026-07-30. Decisions open (§3 **A″**, proposed; §8 registration).
**Date:** 2026-07-30
**Depends on:** `19` (IMPLEMENTED — §4 **B**, `ledger_read`/`list_run_events`; INV-AG-10).
**Blocks:** nothing. **Composes with:** nothing.
**Blast radius:** MEDIUM — touches five model-visible payload sites, all in `internal/cli`. No schema change, no config change, no cross-package API change.
**Proposed commits:** `fix(agent): route model-visible content references through one discloser`, `fix(agent): scope content resolution to the principal that was handed the reference`

**Two premises corrected up front:**

- **`03` is CLOSED**, not pending (`.mivia/plans/03-agentkit-embedded-serving.md:3`, and `00` §4 at `00-agent-roles-program-overview.md:89`: "any future plan proposing to embed or serve instruction content is proposing new work, not resuming `03`"). There is no funded path to multi-tenant serving. The cross-tenant escalation is therefore **hypothetical**, and this plan is not justified by it. §1c gives the justification that survives that correction.
- **The principal *is* in scope at the content write site.** `recordRunResults` takes `tasks []subagents.Task` (`internal/coordinator/record_results.go:12`) and `subagents.Task` carries `SessionID, TurnID, Role` (`internal/subagents/subagents.go:16-18`). `t.SessionID` is one identifier away from `persistResultContent`. But §3 B shows that availability is not what makes principal-keying wrong.

---

## 1. The defect

`ledger_read` resolves any reference any caller presents, with no principal gate.

`internal/cli/ledger_tools.go:98-131` is the whole authorization story: unmarshal, `ledger.ParseReference`, `t.repo.LoadContent`. There is no `principalFromContext`, no `runHandles` lookup, no `orchestrationHandleAccessible`. Compare `list_run_events` **in the same file**, `:339-352`:

```go
// INV-AG-9: every run-scoped tool gates on the caller's principal, and an
// unknown run and an inaccessible run must be indistinguishable.
rawHandle, ok := runHandles.Load(params.RunID)
...
if !ok || !orchestrationHandleAccessible(ctx, record, t.dispatcher, t.repo) {
```

`ledger_read` is the only run-scoped read surface in the file with no such gate. `.mivia/invariants.md` INV-AG-9 was extended for `list_run_events` and not for `ledger_read`.

**The store is unscoped by construction, on both backends.**

- Memory: `defaultOrchestrationRepo` is a package var (`internal/cli/orchestration_state.go:29`), one `MemoryLedgerRepository` shared by every session that does not supply its own. Its content is a flat `map[string][]byte` keyed by ref (`internal/ledger/memory_claims.go:34-53`).
- SQLite: `CREATE TABLE content (ref TEXT PRIMARY KEY, data BLOB NOT NULL, created_at TEXT ...)` (`internal/storage/store.go:269-272`). No run column, no principal column. Reads are `WHERE ref = ?` (`:449`).
- **No retention, and `DeleteRun` does not touch content.** `internal/ledger/memory.go:334-347` deletes the run record and idempotency keys; the content map is untouched. The SQLite path is the same shape. Content therefore outlives its run, its session, and — on SQLite — its process, forever.

**The equality oracle.** The key is `sha256(content)`. So `status: ok` versus `not_found` answers "were these exact bytes ever recorded", and the `bytes` field (`ledger_tools.go:148`, deliberately the pre-truncation, pre-redaction length) answers "how long were they". For task *output* this is unguessable. For error text it is not: `persistResultContent` stores `r.Err.Error()` (`internal/coordinator/record_results.go:78`), which is short, templated, and drawn from a small vocabulary. A few hundred guesses cover most of it.

### 1a. Reachability: how an agent comes to hold a reference it was not handed

Six candidate paths. **Two are real.**

| # | Path | Real? | Evidence |
|---|---|---|---|
| 1 | Task results from `spawn_agent` / `dispatch_tasks` / `delegate` | **No — authorized** | `modelTaskResults` (`orchestrate_lifecycle.go:33-50`), `encodeResults` (`dispatch.go:281-308`). These are the caller's own runs. |
| 2 | `inspect_agents` | **No — already gated** | `orchestrate.go:341-344` calls `orchestrationHandleAccessible` before emitting refs. Also `Privileged()`. |
| 3 | `list_run_events` metadata | **No** | `runEventInfo` (`ledger_tools.go:376-383`) has no ref field. Payloads are never returned. |
| 4 | Event payloads, audit metadata, logs | **No** | `grep` for `output_ref\|OutputRef\|ref:` across `internal/events`, `internal/agent` and the audit path returns nothing outside `internal/cli`. |
| 5 | **Guessing** | **YES** | §1's oracle. Cheap against recorded error text; infeasible against output. |
| 6 | **Direct store access** | **Conditionally YES** | On SQLite the store is a file under `os.UserCacheDir()`, outside the workspace. `read_file` is workspace-confined (`internal/tools/read.go:16,28`) so cannot reach it. `run_command` can, **if** the workspace's `run_allowlist` contains anything that reads a path — and no allowlist is compiled in (`60` §4). Where reachable it yields every ref *and every byte*, with no tool-level gate. |

Path 5 is the defect this plan can close. Path 6 is out of reach of any change to `ledger_read` and is named so the plan is not mistaken for a confidentiality boundary it cannot be.

A seventh case looks like a leak and is not: **resume**. A recovered run re-registered in this process exposes task snapshots — including `OutputRef` values minted by a *previous* process under a *different* `SessionID` — through path 2. That is `15`'s resume surface working as designed. Any scheme that blocks it (§3 B) removes a shipped feature.

### 1b. Sub-agents inherit the parent principal — verified

`MultiStepHandler` copies `SessionID` and `Role` into the child loop's options (`internal/subagents/multi_step.go:88-93`), and `runThroughCoordinator` stamps the caller's `SessionID` onto every task (`internal/cli/orchestrate.go:26-36`). `orchestrationPrincipal` is exactly `{sessionID, role}` (`orchestration_state.go:42-45`).

**Consequence:** a principal gate does not isolate a sub-agent from its parent's content, and cannot be made to without a separate provenance concept. Every option in §3 inherits this. `ledger_read` is unprivileged on purpose and remains so.

### 1c. How many principals actually exist in one process today

One, in the shipped CLI. `chat.NewSession` mints `SessionID` once (`internal/chat/session.go:119`); `/clear` resets history and keeps the object (`internal/cli/welcome.go:218-231`); `openSessionByName` calls `Load` on the same object (`welcome.go:262`). `SessionID` is stable for the process lifetime.

The one way a second principal appears is `orchestrate.go:30`: with no caller in context, a fresh `SessionID` is synthesized. Runs created under it are unreachable by *everyone*, so that is an availability wart, not a confidentiality one.

**This is why today's severity is low.** One process, one principal, one local user. The blast radius is the same user's own earlier content — plus, on SQLite, content from that user's previous processes. Under a hypothetical multi-tenant host it would be cross-tenant, but there is no such host planned, and a process-local map (§3 A″) is not what would protect it either.

## 2. Invariant to establish

> Content is resolvable only by a principal this process handed the reference to.

Corollaries:

- **One discloser.** A reference becomes readable at exactly one place — the function that writes it into a model-visible payload. Emitting and recording cannot be separated, or a new emit site silently breaks INV-AG-10. This is `19` §3's "one minter" corollary applied to the read side.
- **Three misses, three answers.** `malformed` means the shape is wrong; `unknown_reference` means this principal was never handed it; `not_found` means **this principal holds it and the bytes are absent**.
- **A gate is not confidentiality.** §1a path 6 and `19` §8's lethal-trifecta exposure survive this plan untouched.

## 3. Options

### A. Resolve the ref to its owning run, then require `orchestrationHandleAccessible`

*For:* Reuses the exact gate `list_run_events` uses. No new state. Works on both backends.

*Against — it breaks INV-AG-10 on a timer.* `runHandles` entries are deleted `retention` after the run completes, default 10 minutes (`orchestration_state.go:76-102`). A ref handed to the model resolves for ten minutes and then stops. `list_run_events` can accept this because the run id's *existence* is what expires; `ledger_read` cannot, because the model still has the ref in its transcript.

*Also against:* refs are content-addressed, so the ref→run relation is many-to-many — two runs producing identical output share one ref. "Its owning run" is not well-defined.

### B. Key stored content per principal

*For:* The strongest form. A miss is indistinguishable from absence with no extra status.

*Against — it contradicts a standing decision.* `internal/ledger/types.go:105-118`: "Only fields describing the WORK live here. Permission, scope, role, session and turn are deliberately absent: the ledger is a file in the workspace and the agent has file tools, so a persisted permission would be a privilege grant the agent could write for itself." Keying content by `SessionID` writes a principal into that store.

*Against — it deletes resume.* `SessionID` is fresh per process and resume deliberately does not restore it (`recovery.go:344`). Every pre-restart ref becomes permanently unreadable.

*Against — the schema change is not free.* There is no schema version table: `OpenSQLite` issues three `CREATE TABLE IF NOT EXISTS` and four pragmas, none of them `user_version`. `CREATE TABLE IF NOT EXISTS` never alters an existing table, so an added column is silently absent on every existing database file and the first `INSERT` naming it fails at runtime, with nothing able to detect the mismatch.

### C. Withdraw `ledger_read`; expose event metadata only

`19` §14 names this as the withdrawal path.

*For:* Closes the oracle completely. Zero new code. Cannot regress.

*Against:* Removes the capability `19` was written to add, over a defect whose demonstrated impact is "an agent can confirm which templated error strings this same user's earlier tasks produced" — strings it can usually reproduce by causing the same failure. `19` §14's own withdrawal trigger was different: routine bulk-reading, not observed.

### D. Accept and document

*For:* Proportionate to §1c. Honest.

*Against:* Leaves INV-AG-9 asserting a rule the sibling tool in the same file does not follow. `19` gated `list_run_events` on the reasoning that "read-only is not an exemption"; that reasoning applies verbatim to content and was not applied.

### A″ (chosen). Admit only refs this process disclosed to this principal

Keep the store unscoped. Maintain a process-local, append-only set: principal → the refs this process has written into a model-visible payload for that principal. `ledger_read` admits a ref iff it is in the caller's set.

*For:*
- **Preserves INV-AG-10 exactly.** Membership is granted at disclosure and never expires — strictly better than A.
- **Closes the oracle.** A guessed ref was never disclosed, so it reports `unknown_reference` regardless of whether bytes exist. Path 5 is dead.
- **No schema change, no migration, no version table needed.** Works identically on the default memory backend and on SQLite, because it does not touch the store.
- **Persists no ownership.** The set is in-process memory, so `types.go:105-118` is respected.
- **Resume keeps working.** A recovered run's refs are disclosed through `inspect_agents` (already principal-gated) and become readable at that moment.

*Against:*
- **A new failure mode:** a future emit site that forgets to disclose hands out a ref that no longer resolves — INV-AG-10 broken silently. This is the whole risk, and the reason §4 requires a single discloser plus a structural test mirroring `19`'s `TestReferenceHasSingleMinter`. If that funnel cannot be made airtight, take **D**, not a partial gate.
- **Unbounded growth**, technically: ~75 bytes per ref per principal. 10 000 tasks ≈ 1 MB for the process lifetime. Not capped — a cap would evict a ref the model still holds.
- **Process-local.** A restart resets the set; refs from a previous process report `unknown_reference`. A resumed session whose transcript was reloaded from disk *can* hold such a ref. Document it; do not add a persistence back door.

**DECISION: A″.** The only option that narrows the surface without breaking INV-AG-10 (A), contradicting the ledger's no-authority-fields decision and deleting resume (B), or withdrawing a working capability over a low-severity oracle (C). D remains the fallback if §5 Wave 1 cannot land cleanly.

### Resolving the tension in `19` §3's second corollary

`19` §3 requires: **`not_found` means the bytes are absent** — never "you asked with the wrong key shape." Any scoping introduces a second reason a lookup can miss.

**Resolution: do not overload `not_found`. Add a third status.**

| Status | Meaning | Preserves the corollary? |
|---|---|---|
| `malformed reference` | shape is not canonical | yes (already shipped) |
| `unknown_reference` | this principal was never handed this ref | yes — the answer is about *entitlement*, and says nothing about presence |
| `not_found` | **this principal holds this ref and the bytes are absent** | **yes — strengthened** |

The corollary becomes narrower and truer. And `unknown_reference` leaks nothing: it is returned identically whether the bytes exist under another principal or do not exist at all — INV-AG-9's indistinguishability property transposed to content.

## 4. Blast radius and changes

MEDIUM, confined to `internal/cli`. **The write side does not change**, so §1's finding that the principal is available at `persistResultContent` is recorded and deliberately unused.

**File-size constraint is load-bearing.** `internal/cli/ledger_tools.go` is already 451 lines against the `--strict` 500-line ceiling. The disclosure set and admission helper live in a **new** file; `ledger_tools.go` may gain at most ~10 lines.

| # | File | Change |
|---|---|---|
| 1 | `internal/cli/content_disclosure.go` (new) | The disclosure set, `discloseTaskRefs`, and the admission check. Per §6. |
| 2 | `internal/cli/orchestrate_lifecycle.go:33-70` | `modelTaskResults` and `persistedTaskResults` route refs through `discloseTaskRefs`. |
| 3 | `internal/cli/orchestrate.go:358-372` | `inspect_agents`' `taskInfo` routes through `discloseTaskRefs`. |
| 4 | `internal/cli/dispatch.go:281-308` | `encodeResults` routes through `discloseTaskRefs`. |
| 5 | `internal/cli/delegate.go:160-185` | The `addRef` call sites route through `discloseTaskRefs`. |
| 6 | `internal/cli/ledger_tools.go:98-131` | After `ParseReference`, before `LoadContent`: admit or return `unknown_reference`. |
| 7 | `internal/cli/content_disclosure_test.go` (new) | Unit tests for #1. |
| 8 | `internal/cli/ledger_tools_test.go` | Scoping and status-trichotomy tests; update existing tests to disclose first. |
| 9 | `internal/cli/ref_disclosure_surface_test.go` (new) | Structural guard: every model-visible ref write goes through `discloseTaskRefs`. |
| 10 | `.mivia/invariants.md` + `Makefile:131` | New INV-AG-12 row, amended INV-AG-9/10 rows, new test names. |
| 11 | `docs/product/agent.md` | Document `unknown_reference`; state that references from a previous process do not resolve. |

**Change #9 is not optional**, for the reason `19` §10 gave for its own change #8: the failure mode of A″ is a forgotten disclosure, and only a structural test makes that impossible rather than merely unlikely.

**No config surface.** `DisableTools` remains the off switch.

## 5. Implementation waves

Per `.mivia/rules/05-adlc-agentic-development-lifecycle.md` Step 1: one file per task, a test task before each production task, reviewer every 2–3 tasks.

**Wave 1 — the discloser** (must land airtight or the plan reverts to §3 D)
1. `internal/cli/content_disclosure_test.go` — disclosure/admission round-trip; distinct principals do not see each other's refs; empty ref never disclosed; absent caller denied; concurrent disclose/admit under `-race`.
2. `internal/cli/content_disclosure.go` — change #1.
3. `internal/cli/ref_disclosure_surface_test.go` — change #9, written against the *unrefactored* sites so it starts RED.
   *Reviewer checkpoint.*

**Wave 2 — route every emit site through it** (Wave 1 task 3 goes GREEN here)
4. `internal/cli/orchestrate_lifecycle.go` — change #2.
5. `internal/cli/orchestrate.go` — change #3.
6. `internal/cli/dispatch.go` — change #4.
7. `internal/cli/delegate.go` — change #5.
   *Reviewer checkpoint.*

**Wave 3 — the gate** (gating before Wave 2 breaks every legitimate read)
8. `internal/cli/ledger_tools_test.go` — change #8.
9. `internal/cli/ledger_tools.go` — change #6.
   *Reviewer checkpoint.*

**Wave 4 — end-to-end and registration**
10. `TestModelVisibleOutputRefStillResolvesAfterScoping` — the §7 load-bearing test.
11. `.mivia/invariants.md` + `Makefile:131` — change #10.
12. `docs/product/agent.md` — change #11.

**Wave ordering is a correctness constraint, not a preference.** Wave 3 before Wave 2 makes every `ledger_read` return `unknown_reference`.

## 6. API surface

`internal/cli/content_disclosure.go`:

```go
// contentDisclosureSet records, per principal, every content reference this
// process has handed to that principal. Membership is granted at disclosure and
// never expires: a reference handed to the model must keep resolving
// (INV-AG-10), so nothing here evicts.
type contentDisclosureSet struct {
	mu   sync.RWMutex
	refs map[orchestrationPrincipal]map[string]struct{}
}

// disclosedContentRefs is process-global for the same reason runHandles is: the
// emitting tool and the resolving tool are different objects on one dispatcher.
var disclosedContentRefs = newContentDisclosureSet()

func newContentDisclosureSet() *contentDisclosureSet

// disclose records refs as readable by principal. Empty refs are ignored.
func (s *contentDisclosureSet) disclose(principal orchestrationPrincipal, refs ...string)

// admits reports whether principal was handed ref by this process.
func (s *contentDisclosureSet) admits(principal orchestrationPrincipal, ref string) bool

// discloseTaskRefs is the ONE place a content reference becomes readable. It
// records the refs for the caller in ctx and returns them unchanged, so a caller
// cannot emit a reference without disclosing it. Callers with no principal in
// ctx get the refs back undisclosed — the reference is still correct, it is
// simply unreadable, which is the safe direction.
func discloseTaskRefs(ctx context.Context, outputRef, errorRef string) (string, string)

// contentRefAdmitted reports whether the caller in ctx may resolve ref. A
// missing principal is a denial, matching orchestrationHandleAccessible.
func contentRefAdmitted(ctx context.Context, ref string) bool
```

`internal/cli/ledger_tools.go` — the only addition, between `ParseReference` and `LoadContent`:

```go
if !contentRefAdmitted(ctx, params.Ref) {
	return jsonPayload(map[string]any{
		"status": "unknown_reference",
		"ref":    params.Ref,
	}), nil
}
```

**Rule `60`:** the `ledger_read` description gains one sentence describing `unknown_reference` in language-neutral terms — "a reference this caller was not given" — naming no storage engine, table, language or module. `TestSessionToolSurfaceIsProjectAndLanguageGeneric` already covers this text.

## 7. Verification

```bash
go build ./... && go vet ./...
go test ./internal/cli/ -race -count=1
go test ./internal/... ./cmd/... -race
make verify && make invariants
```

**Tests:**

- `TestModelVisibleOutputRefStillResolvesAfterScoping` — **the load-bearing one.** Run a task end-to-end, take the `output_ref` from the tool result the model receives, resolve it as the same caller, assert the bytes come back. Proves the gate did not break `19`.
- `TestLedgerReadRejectsUndisclosedRef` — store content directly, present its ref, assert `unknown_reference` and that no bytes are returned. The §1 defect as a test; fails today.
- `TestLedgerReadRejectsForeignPrincipalRef` — P1 runs a task, P2 presents P1's ref, assert `unknown_reference`.
- `TestGuessedDigestIsIndistinguishableFromAbsent` — the oracle test. A ref whose bytes ARE recorded but were never disclosed, and a ref whose bytes were never recorded, must produce byte-identical responses (`bytes` included).
- `TestLedgerReadDistinguishesThreeMisses` — the three statuses are textually distinct, and `not_found` is reachable only for a disclosed ref.
- `TestSubAgentInheritsParentContentScope` — pins §1b as **intended behaviour** so a later reader does not "fix" it silently.
- `TestEveryModelVisibleRefIsDisclosed` — change #9. Structural.
- `TestLedgerReadScopingWorksOnMemoryBackend` — the default backend.
- `TestContentDisclosureSetIsRaceFree` — `-race`, concurrent `disclose`/`admits`.
- `TestDisclosureWithoutPrincipalDeniesRead` — no caller in ctx ⇒ denial.

**Mutation proofs:**

| # | Mutation | Test that MUST fail |
|---|---|---|
| 1 | Remove the `contentRefAdmitted` gate | `TestLedgerReadRejectsUndisclosedRef` |
| 2 | Make `contentRefAdmitted` ignore the principal | `TestLedgerReadRejectsForeignPrincipalRef` |
| 3 | Return `not_found` instead of `unknown_reference` | `TestLedgerReadDistinguishesThreeMisses` |
| 4 | Include `bytes` in the `unknown_reference` response | `TestGuessedDigestIsIndistinguishableFromAbsent` |
| 5 | Drop `discloseTaskRefs` from any one emit site | `TestEveryModelVisibleRefIsDisclosed` |
| 6 | Admit a ref when ctx has no principal | `TestDisclosureWithoutPrincipalDeniesRead` |
| 7 | Add eviction/TTL to the disclosure set | `TestModelVisibleOutputRefStillResolvesAfterScoping` |
| 8 | Gate scoping on the SQLite backend | `TestLedgerReadScopingWorksOnMemoryBackend` |

Mutations #1 and #2 are the regression proofs for §1 and must be recorded with `Regression: INV-AG-12`.

## 8. Invariant registration

`.mivia/invariants.md` has `INV-AG-1`…`INV-AG-7`, `INV-AG-9`, `INV-AG-10`, `INV-AG-11`. **`INV-AG-8` is absent** — a gap, not a free slot; do not reuse it. **The next free id is `INV-AG-12`.**

Add to the Agent Loop table:

```
| INV-AG-12 | Safety | Recorded content is resolvable only by a principal this process handed the reference to. A reference that was never disclosed is reported identically whether its bytes exist or not, so the content digest is not an equality oracle; a sub-agent deliberately shares its parent's scope, because it shares its principal. Scope is process-local by design — no principal is ever written to the ledger (plan 12 §3) | `TestLedgerReadRejectsUndisclosedRef`, `TestLedgerReadRejectsForeignPrincipalRef`, `TestGuessedDigestIsIndistinguishableFromAbsent`, `TestSubAgentInheritsParentContentScope`, `TestEveryModelVisibleRefIsDisclosed`, `TestDisclosureWithoutPrincipalDeniesRead`, `TestLedgerReadScopingWorksOnMemoryBackend` | 2026-07-30 (plan 20) |
```

Amend `INV-AG-9` — append: "Content reads are scoped by disclosure rather than by run handle, because a run handle expires and a reference must not (INV-AG-12)."

Amend `INV-AG-10` — append: "A malformed reference, an undisclosed reference and absent content are three distinct answers, so `not_found` means 'this caller holds this reference and the bytes are absent'." Add `TestModelVisibleOutputRefStillResolvesAfterScoping` to its test list.

`Makefile:131` — add `TestModelVisibleOutputRefStillResolvesAfterScoping`, `TestGuessedDigestIsIndistinguishableFromAbsent`, `TestSubAgentInheritsParentContentScope`, `TestEveryModelVisibleRefIsDisclosed`, `TestContentDisclosure`, `TestDisclosureWithoutPrincipalDeniesRead`.

## 9. What this does NOT solve

Stated flatly, because a gate named "scope content to its principal" invites more credit than it earns.

1. **A sub-agent is not isolated from its parent** (§1b). Untrusted sub-agent output can be read back by anything sharing that principal. Fixing this needs a provenance concept that does not exist and is not proposed here.
2. **Direct store access is untouched** (§1a path 6). A tool-level gate cannot address this; only store-level encryption or confinement could.
3. **Confidentiality of content the caller legitimately holds is unchanged.** `19` §8's second exposure stands verbatim: the `content_is_data` framing remains the only mitigation, and it is a prompt, not a control.
4. **Content still has no retention and no deletion.** `DeleteRun` does not remove content, and the `content` table has no expiry. A″ makes it *unaddressable* by a new process rather than *absent*. Retention is a real defect and a separate plan — it needs the schema-versioning work §3 B priced.
5. **It provides nothing under a multi-tenant host.** A process-local map is not a tenant boundary.
6. **The disclosure set is a new stateful dependency of a shipped guarantee.** Its failure mode — a forgotten disclosure — breaks INV-AG-10 silently. Change #9 is the mitigation and the reason Wave 1 gates the rest.

## 10. Plan scorecard

| Criterion | Verdict |
|---|---|
| Compiles (no import cycles) | PASS — all changes in `internal/cli`; `orchestrationPrincipal` is already local there |
| No breaking API change | PASS — helpers unexported; one new tool status, additive in the JSON envelope |
| Testable in isolation | PASS — the set is a plain struct; the gate takes a `context.Context`; memory repo is the double |
| Backward-compatible config | PASS — no new keys, no schema change, no migration |
| Every function has a test | PASS — Waves 1–4 pair each production task with a preceding test task |
| Security tests present | PASS — three negative tests (`secure-change`) |
| `--strict` structure gate | PASS **only via change #1's new file** — `ledger_tools.go` is 451/500 and takes ≤10 more |
| Rule `60` satisfied | PASS — one language-neutral sentence, covered by the existing session-surface guard |

## 11. Rollback criterion

Kill this plan if:

- **Wave 1 cannot be made airtight.** If a forgotten disclosure cannot be made to fail a test, stop and take §3 **D**: register INV-AG-12 as an *accepted limitation* with the §1c severity analysis, and change no code. A gate that sometimes drops legitimate refs is worse than no gate — it trades a low-severity confidentiality gap for a correctness regression on a shipped guarantee.
- **`unknown_reference` proves confusing in practice.** If the model retries or treats it as transient, collapse to §3 D rather than folding it into `not_found`, which would destroy `19` §3's corollary.
- **Disclosure-set memory becomes material.** Do not add eviction (mutation #7). Take §3 **C** — withdraw `ledger_read` — which is `19` §14's own withdrawal path.

In each case keep the disclosure funnel: routing every model-visible reference through one function is worth doing on its own merits, because it is where `19`'s "one minter" discipline belongs on the read side.
