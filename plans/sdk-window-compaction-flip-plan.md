# SDK Window compaction flip — implementation plan

Status: planned, not yet built. Revision 3, after two hostile plan
reviews returned REVISE.

**Blocked until rebase.** A concurrent slice is landing the summarizer
memo and the one-call retry in this same tree. See "Dependency on the
concurrent summarizer slice". Do not start until that slice commits.

Predecessor: `plans/sdk-window-compaction-adoption-plan.md`. That plan
shipped items 1-6 and part of item 8. It deferred items 7 and 9. This
plan lands them, and makes the SDK Window the only compaction path in
production.

Owner role: planner. This document contains no production code.

## Revision 3 changes

Review round 2 closed seven findings and raised eight more. Seven of
the eight were verified and accepted. One is disputed in part, with
evidence, under "Dependency on the concurrent summarizer slice".

| Finding | Section changed |
|---|---|
| Item C.10's overlay double-counts elision | Item C.10, rewritten field by field; test T.5; mutation proof six |
| Item C.11's translation loses chat history | Item C.11, scoped to the preflight's own shape |
| Collision with the concurrent slice | Items A.1-A.3 cut; new dependency section |
| Item C.9's reset list is incomplete | Item C.9, four fields added, one residual named |
| Test T.1's third arm is vacuous | Test T.1, arm 3 and sub-assertion 2 |
| The over-budget conversions are not verbatim | Raw-sweep table, two rows |
| `sdkCompactionSkipped` is turn-sticky | Item A.4, reset per call |
| The legacy-path helper contradicts itself | Raw-sweep section, helper definition |

## Revision 2 changes

Review round 1 raised eleven findings. Every finding was verified
against the tree and accepted. None was rejected.

| Finding | Section changed |
|---|---|
| Incomplete inversion tables | New "raw `summaryProbeOptions` sweep" section |
| Recovery gate reads wiring, not the skip | Item C.7, new `sdkCompactionSkipped` flag |
| Recovery rerun keeps abandoned grounding | New item C.9 |
| Prefix-cache re-anchor is not assertion-preserving | New "prefix-cache test" section |
| Parity test cannot fail | Test T.1, rewritten to assert the difference |
| Behaviour change under-scoped | Two new losses, new test T.10 |
| `Candidate` clobbered by `confirmSDKCompaction` | New item C.10, test T.5 strengthened |
| Finding F.2's evidence was false | Finding F.2, evidence replaced |
| Two missed sweep sites, one name collision | Items B.1 and B.2 |
| Subagent double mechanism unanalysed | New item C.11, new test T.11 |
| "Lower the ceiling" is not an edit | Per-call-site instructions |

## Goal

Make the SDK `agentloop` Window own mid-run compaction on every
production turn. Make the host summarizer always enabled. Complete the
summarizer adapter so the SDK path matches the memoization and retry
semantics the legacy path already pins.

## Reading order for the builder

1. This file, in full.
2. `plans/sdk-window-compaction-adoption-plan.md`, sections "Design"
   and "Tests".
3. In the SDK repo, read-only: `docs/history/agentloop.md`, "Addendum:
   summarizer interface, request observer, summary skip, and shape
   repair" (line 5575).
4. In the SDK repo, read-only: `docs/history/contextsummary.md`,
   "Addendum: host-schema keys, evidence fields, preamble, and skip
   sentinel" (line 289).
5. `agentloop/compaction.go` in the SDK repo. Read `compactHistory`,
   `summarizeDropped`, `recoverPromptTooLong`, `checkCompactedBudget`,
   and `preserveSummaryName`.
6. `internal/contextmgr/planner_elision.go`. Read `planCompact` and
   `installRetainedElisionRefs`.

Never edit the SDK repo. The SDK side is shipped.

---

## Verified current state

Every statement below was checked against the tree on 2026-09-07.

- `go build ./...` in the host repo succeeds. The tree builds today.
- `internal/agent/agentloop_adoption.go:176` defines
  `sdkCompactionAdopted`. It returns false without a positive
  `MaxContextTokens` or a wired `SummaryConfig.Summarizer`. With a nil
  `PreparationManager` it returns true. Otherwise it returns
  `opts.PreferSDKCompaction`.
- `Options.PreferSDKCompaction` is declared at
  `internal/agent/options.go:272` and read at
  `internal/agent/agentloop_adoption.go:183`. No production call site
  writes it. Only tests set it.
- `internal/agent/sdk_summarizer_adapter.go` NOW HAS a turn memo and a
  one-call retry. Revision 2 said it had neither. A concurrent slice
  landed both while revision 2 was under review. `Summarize`
  (`:63-95`) computes `sdkSummarizeInputKey`, replays through
  `memoized`/`replayMemo`, and calls `summarizeWithOneRetry`. The memo
  lives in `internal/agent/sdk_summarizer_memo.go`.
  `resetTurnCompaction` (`internal/agent/context.go:101`) already
  clears `l.sdkSummaryMemo`. Re-verify this bullet after the rebase.
- `internal/agent/agentloop_adoption.go:272` defines
  `sdkCompactionObserver`. It runs `PreparationManager.Prepare` with
  `Budget = math.MaxInt` at line 287.
- `adoptSDKCompaction` pins `TriggerPercent` 100 and `TargetTokens` at
  `MaxContextTokens / 2`.
- `internal/agent/agentloop_recovery.go:52` already stands the host
  prompt-too-long rerun down on an adopted turn. Commit `190555f0`
  landed that. Its doc comment at `:36-42` gives a second reason: `l`
  persists across both `run()` calls with no reset between them.
- `internal/config/types.go:101` defines `SummaryEnabled`. It is
  opt-out already. Only an explicit `enabled = false` turns it off.
- `internal/clichat/context_setup_session.go:120` states a false claim
  about the summary gate. `buildSummaryWiring`
  (`internal/clichat/context_summary_setup.go:131`) states that
  `[privacy]` is no longer a precondition.
- `internal/agent/context.go:85` is `recordPreparation`'s last
  assignment: `l.LastPreparation = preparation`. It replaces the
  struct wholesale.

### SDK facts this plan depends on

- `agentloop/run.go:190` assigns the planned history back:
  `*st.history = planned`. The compacted history reaches
  `Result.History`.
- `agentloop/options.go:342` rejects `Trim` together with `Window`.
  The two are mutually exclusive.
- `agentloop/compaction.go` planning path: a summarizer skip with no
  prior summary injects nothing and proceeds with the kept history.
- `agentloop/compaction.go` recovery path: a summarizer skip with no
  prior returns `(nil, false, nil)`. `recoverPromptTooLong` then
  returns the original `ErrPromptTooLong` with no retry.
- `agentloop/compaction.go` `checkCompactedBudget` fails closed with
  `plan.ErrRetentionOverflow` wrapped under `ErrCompactionFailed`. It
  cannot shrink a message body; it only drops whole messages.

---

## Findings against the brief

The brief carries five statements that the tree contradicts. Each
changes the plan.

### Finding F.1 — the summarizer term cannot leave the recovery gate

A turn with a positive `MaxContextTokens` and no host summarizer
adopts under the new gate. `sdkSummarizerAdapter.Summarize` then
returns `plan.ErrSummarySkipped`. The SDK's `recoverPromptTooLong`
treats a skip with no prior as unrecoverable and retries nothing.
`runSDKPromptTooLongRecoverable` already stands down on an adopted
turn. The turn therefore loses recovery on both sides.

`internal/chat/session_sdk_backend_test.go:237`
(`TestSDKSessionTurnRetriesAfterPromptTooLong`) proves this. Its
fixture sets `MaxContextTokens = 16 << 10` with no
`PreparationManager` and no summarizer.

**Correction.** The stand-down keeps a term that observes the skip.
See item C.7. Review round 1 showed that a nil-pointer term is not
enough. The corrected term is a per-turn observed-skip flag.

### Finding F.2 — deleting `applySDKTrim` removes preparation from zero-ceiling turns

A turn with a `PreparationManager` and `MaxContextTokens == 0` does
not adopt. Deleting `applySDKTrim` leaves that turn with no `Trim` and
no observer, so `PreparationManager.Prepare` never runs.

Evidence, corrected after review round 1. The first draft cited
`config.EffectivePromptTokens`. That citation was wrong: the clamp at
`internal/config/prompt_budget.go:137` reads `if capacity < 0`, and
the function's own doc at `:116-120` states that validated config
cannot reach a zero budget. Two honest sources remain:

- Three existing callers use exactly this shape:
  `internal/agent/sdk_trim_prepare_test.go:31`,
  `internal/agent/sdk_trim_prepare_test.go:63`, and
  `internal/agent/context_loop_test.go:127`. All three wire a
  `PreparationManager` with no ceiling. All three break on a literal
  deletion.
- `cliagents.AgentBinding.ContextBudget`
  (`internal/cliagents/agent_binding.go:122-135`) returns
  `b.staticBudget` unclamped when `b.contextWindow <= 0` and the
  session budget accessor returns a non-positive value. A zero result
  is reachable there without config validation.

The plan does not claim the second path is exercised today. The first
is enough: three real callers break.

**Correction.** Keep `applySDKTrim`. See item C.2.

### Finding F.3 — narrowing `sdkPromptBudgetPreflight` breaks the subagent error contract

`sdkPromptBudgetPreflight` runs only when `PreparationManager == nil`
and `MaxContextTokens > 0`. Under the new gate every such turn adopts.
"Narrow it to turns that adopt nothing" therefore makes it
unreachable, not narrow.

Four tests assert `agent.ErrPromptBudgetExceeded`:
`internal/subagents/context_policy_test.go:16`,
`internal/subagents/oneshot_test.go:216`,
`internal/subagents/oneshot_test.go:247`, and
`internal/subagents/multi_step_test.go:132`. The SDK Window fails with
`plan.ErrRetentionOverflow` instead, inside the loop rather than
before the first provider call.

**Correction.** Keep the preflight unchanged. Item C.11 adds the error
translation that keeps the vocabulary stable. Test T.11 pins the
two-mechanism interaction review round 1 found unanalysed.

### Finding F.4 — the commit candidate already comes from the compacted history

`agentloop/run.go:190` writes compaction back, and
`writeBackSDKHistory` (`internal/agent/loop_dispatch.go:127`) carries
it into `loop.Messages`. Sourcing the commit from the observer's
`req.Messages` would lose data: the observer fires before `Chat`, so
its request excludes the final assistant message and the last
iteration's tool results.

**Correction.** Keep `loop.Messages`. Pin the guarantee with test T.5.
Review round 1 added a second half: the committed `Preparation` is
damaged by a different mechanism. See item C.10.

### Finding F.5 — `Evidence` does not come from the dropped-messages argument

`contextmgr.BuildSummaryRequest`
(`internal/contextmgr/summary_request.go:52`) sets the envelope's
`Evidence` from `input.Evidence`. Both the adapter and the legacy
`summarizeTurn` pass `snapshot.Evidence` from `TurnState.Snapshot()`.
The dropped-messages argument feeds `SourceExcerpts`.

**Correction.** Test T.7 asserts both facts separately. See item A.3.

---

## Behaviour change this plan accepts

The observer runs `Prepare` with `Budget = math.MaxInt`
(`internal/agent/agentloop_adoption.go:287`) and discards the prepared
messages. `planCompact` (`internal/contextmgr/planner_elision.go:14`)
runs only when the trigger crosses, so it never runs on an adopted
turn. The host's own shaping therefore stops. The SDK's `plan.Compact`
becomes the only mechanism that removes content.

The host loses five things on an adopted turn. Items 4 and 5 were
added after review round 1.

1. Per-message structural elision of large tool results.
2. Reasoning-block elision.
3. The planner's mandatory-index protection.
4. **The spool re-read handle.** `installRetainedElisionRefs`
   (`internal/contextmgr/planner_elision.go:325-345`) writes an elided
   body to `remainder.Spool` and rewrites the message to
   `elisionNoticeWithRef`. `internal/clichat/read_output.go` lets the
   model re-read that ref. After the flip a large tool result is
   either fully present or fully dropped, and the handle is gone. Test
   T.10 records the loss.
5. **A hard failure replaces a shrink.** Host elision shrinks a body
   INSIDE a mandatory-retention message. `plan.Compact` cannot. It
   drops whole messages, keeps the mandatory set, and then
   `checkCompactedBudget` fails closed. A single oversized newest tool
   result that today elides and continues will fail the turn after the
   flip. This is a regression, not reduced shaping. Item C.11 defines
   the outcome. Test T.10 pins it.

The host keeps one protection: `Window.Compaction.PreserveNames`
protects named frames. Item C.3 widens that list.

This is the plan's largest risk. Test T.1 exists to fail when the loss
is larger than stated. Do not claim parity without T.1 passing.

---

## Dependency on the concurrent summarizer slice

Review round 2 found that a second slice is working this same tree.
`git status --short` shows it: `internal/agent/context.go`,
`internal/agent/loop.go`, and
`internal/agent/sdk_summarizer_adapter.go` modified, plus
`internal/agent/sdk_summarizer_memo.go` and
`internal/agent/sdk_summarizer_memo_test.go` untracked.

That slice has already landed revision 2's items A.1, A.2, and A.3.
`Summarize` memoizes on `sdkSummarizeInputKey`, retries once through
`summarizeWithOneRetry`, and `buildRequest`'s doc comment names
`TestSDKSummarizerAdapterEvidenceProvenance` as the pin for the
Evidence and SourceExcerpts provenance split.

**Rule.** Do not start this plan until that slice commits. Then rebase
on it, and re-verify before writing anything:

1. Re-read the "Verified current state" bullets. The adapter bullet is
   already rewritten; check the rest.
2. Re-run the raw sweep `grep -rn "summaryProbeOptions(" --include=*.go
   .`. The sibling slice's edits move line numbers in every table in
   this document.
3. Re-run the item B.4 greps.
4. Confirm items A.1, A.2, and A.3 are in the committed tree, and that
   the three memo tests this plan relied on exist.

Per the `workflow-rules-no-big-test-suites-absolute-local-sdk-replace-shared-tree-etiquette`
memory, stage only this plan's own files by explicit path.

### Retracted: the section B dispute

This revision first disputed review round 2's claim that section B
belongs to the concurrent slice. The dispute was WRONG, and it is
withdrawn. The tree changed during this revision, which is exactly the
`concurrent_zcode_session_commits` memory's warning to re-check git
state before reporting.

At the first check `git status --short` showed nothing under
`internal/config`, `internal/clichat`, or `internal/composition`, and
all three `SummaryEnabled()` gates were present. Minutes later the same
command showed `internal/clichat/context_summary_setup.go`,
`internal/composition/session.go`, `internal/config/types.go`,
`internal/config/policy_resolve.go`, `internal/config/validate.go`, and
`internal/config/context_summary_test.go` all modified.

Verified as landed by the sibling slice:

- Item B.1. All three `SummaryEnabled()` gate reads are gone. Only the
  `contextstate.SummaryPolicy.SummaryEnabled` field assignments remain,
  at `internal/clichat/context_summary_setup.go:134` and
  `internal/composition/session.go:193`, which is correct: item B.1's
  name-collision warning held.
- Item B.2's Go half. `validateSummaryEnabled`
  (`internal/config/validate.go:67-72`) rejects `enabled = false` with
  the message this plan specified.
- Item B.3. `SummaryDisabledReason`
  (`internal/clichat/context_summary_setup.go:29`) now carries exactly
  the three honest cases; the `enabled is not set` branch is gone.

Items B.1, B.2's Go half, and B.3 are therefore CUT.

Verified as NOT landed, and kept in this plan:

- The config and fixture half of item B.2: `.mivia/mivia.toml:24-25`
  still sets `enabled = true`, `.mivia/mivia.toml.example` still
  documents the opt-out, `docs/product/config.md:490-496` still
  documents the key as a switch, and
  `scripts/e2e_context_compaction.py` still carries its opt-out
  scenario.
- All of item B.4. The stale claim is still present verbatim at
  `internal/clichat/context_setup_session.go:120-122`.

Re-check both lists at rebase time and cut whatever else has landed.

## Scope

One atomic commit. A stacked pair is allowed only when the two commits
never separate on any branch. The tree must build and test green at
the end of the commit, so item A.4, section B's residual, section C,
and every test re-anchor land together. That set is smaller than
revision 2's: the concurrent slice already owns items A.1 to A.3,
B.1, B.2's Go half, and B.3.

### A. The summarizer adapter

Revision 2 carried items A.1 (the memo), A.2 (the one-call retry), and
A.3 (the evidence-provenance pin). Review round 2 found all three
already landed by a concurrent slice. They are CUT from this plan. See
"Dependency on the concurrent summarizer slice".

Only item A.4 remains. It is this plan's own.

#### Item A.4 — record the LAST skip on the turn

Add `sdkCompactionSkipped bool` to `Loop`. It is the input to item
C.7's recovery gate: it records that the summarizer declined, so the
SDK's own recovery cannot be trusted to have retried.

Review round 2 found a turn-sticky defect in revision 2's version. A
turn whose first compaction skips and whose second succeeds would
carry the flag true for the rest of the turn. A later prompt-too-long
rejection would then stand the SDK's working recovery down and rerun
the whole loop host-side. The direction was safe, but the wasted work
is half of what commit `190555f0` set out to remove.

The flag therefore records the LAST attempt, not any attempt. Three
edits, against the code the concurrent slice shipped:

1. In `Summarize` (`internal/agent/sdk_summarizer_adapter.go:63`),
   immediately after `key := sdkSummarizeInputKey(...)` and BEFORE the
   `a.memoized(key)` check, set `a.l.sdkCompactionSkipped = false`.
2. In `a.skip` (`internal/agent/sdk_summarizer_adapter.go:181`), set
   `a.l.sdkCompactionSkipped = true`. All four skip sites route
   through this one function (`:63`, `:68`, `:92`, `:99` in
   `Summarize`), so one assignment covers every case.
3. In `replayMemo` (`internal/agent/sdk_summarizer_memo.go`), set
   `a.l.sdkCompactionSkipped = errors.Is(memo.err,
   sdkplan.ErrSummarySkipped)`. A replay must restore the flag the
   original call recorded, exactly as it already restores
   `summaryFailureReason` and the pending outcome.

Clear the flag in `resetTurnCompaction` as well, so a new turn starts
false even when no `Summarize` call runs.

Test T.14 pins the last-attempt semantics.

### B. The summarizer is always enabled — residual only

The user's decision, verbatim: "summarizer must be not opt in - it
must be always enabled, period, and must be best in class."

Items B.1, B.2's Go half, and B.3 are CUT. The concurrent slice landed
them. See "Retracted: the section B dispute" for the verification.

What remains is the config, fixture, and documentation residual that
slice has not touched. Re-verify each row at rebase time before
editing it.

#### Item B.2r — the config and fixture residual

| Path | Change | Verified not landed |
|---|---|---|
| `.mivia/mivia.toml:24-25` | Delete the `[context.summary]` section. It still sets `enabled = true`, which now loads but documents a switch that no longer exists. | yes |
| `.mivia/mivia.toml.example` | Rewrite the `[context.summary]` block. Delete the "To turn it off" instructions. | yes |
| `docs/product/config.md:490-496` | Rewrite. The section still opens "`[context.summary] enabled` (default `true`) turns on…". State instead that the summarizer is always enabled, and that an explicit `enabled = false` is a load error. | yes |
| `scripts/e2e_context_compaction.py` | Delete the `SUMMARY_OFF` constant and the `explicit-opt-out` scenario. | yes |
| `internal/clichat/sessions_command.go:167` | "a `[context.summary]`-enabled policy" is stale. Rewrite. | check at rebase |
| `internal/clichat/context_summary_channels_integration_test.go:12-16` | The package doc's scenario (c) describes an `enabled = false` workspace. Rewrite to the endpoint case. | check at rebase |

Do not re-add a load rejection. `validateSummaryEnabled`
(`internal/config/validate.go:67-72`) already carries it.

**Name collision, still live.**
`contextstate.SummaryPolicy.SummaryEnabled`
(`internal/contextstate/contracts.go:410`) is a DIFFERENT symbol from
the deleted config method. It is assigned at
`internal/clichat/context_summary_setup.go:134` and
`internal/composition/session.go:193`, and read at
`internal/contextmgr/summarizer.go:111`. Do not delete or change it.

#### Item B.4 — fix the doc drift

A grep sweep found five sites that repeat the stale claim. Each states
that a configured `[privacy]` policy is a precondition. Each is false.
Four also omit the real third condition, a resolved provider/model
binding.

| Path:line | Content to rewrite |
|---|---|
| `internal/clichat/context_setup_session.go:120-122` | "The summary gate is explicit: the [context.summary] flag, a configured [privacy] policy, and a resolved provider endpoint…" |
| `internal/clichat/context_summary_setup_test.go:16-18` | "the smallest resolved config that can open the summary gate: a provider endpoint, a compiled [privacy] policy, and the [context.summary] flag" |
| `internal/chat/compact_summary_test.go:144-146` | "the summary gate refuses requests without it" |
| `docs/product/config.md:494` | "Two more conditions must hold… a configured `[privacy]` redaction policy, and a resolved provider endpoint." |
| `internal/clichat/context_summary_integration_test.go:176-178` | "[context.summary] enabled plus [privacy] and an endpoint produce a Summarizer" |

Copy the correct wording from `.mivia/mivia.toml.example:638-641`.

Re-run these raw greps after the edits, and disposition every hit by
hand: `summary gate`, `context.summary`, `\[privacy\]`,
`buildSummaryWiring`, `SummaryEnabled`, `SummaryDisabledReason`,
`LLM summarizer`, `precondition`. Do not filter. This follows the
`sweep_greps_must_not_filter` memory.

Also add the sentence the predecessor plan deferred: an SDK-adopted
Window turn's mid-run compaction uses this summarizer, through the
same redaction and provider binding.

### C. The flip

#### Item C.1 — the gate

```go
// sdkCompactionAdopted reports whether a turn wires the SDK's
// compaction triple (Window + Summarizer + Calibrated). A context
// ceiling is the only condition: the window is sized from it, and
// sdkSummarizerAdapter is always constructed, so no summarizer term
// is needed. The adapter returns plan.ErrSummarySkipped when no host
// summarizer is wired, and agentloop's planning path handles that
// skip by compacting structurally with nothing injected
// (agentloop/compaction.go). The recovery path does NOT: see
// runSDKPromptTooLongRecoverable, which observes the skip itself.
func sdkCompactionAdopted(opts Options) bool {
	return opts.MaxContextTokens > 0
}
```

Delete `Options.PreferSDKCompaction`
(`internal/agent/options.go:260-272`).

#### Item C.2 — `applySDKTrim` stays

Per Finding F.2, keep `applySDKTrim`
(`internal/agent/agentloop_adoption.go:63`) and its call site
(`internal/agent/agentloop_run.go:82`). The body is unchanged. Only
the doc comment changes: the one case that still installs `Trim` is
`MaxContextTokens <= 0`.

`sdkPrepareTrim`, `prepareSDKOnce`, `injectSummaryAfterPrepare`, and
`Loop.injectSummary` all stay. They stay reachable for that case, and
the legacy memo tests are re-anchored onto it. See the raw-sweep
section.

#### Item C.3 — `PreserveNames` is a union

`adoptSDKCompaction` builds the window's `PreserveNames` as the union
of `opts.PreparationInput.PreserveNames` and the memory-context name.
Dedupe the result. Allocate a fresh slice. Never mutate the caller's
backing array. Mirror the SDK's `preserveSummaryName`
(`agentloop/compaction.go:131`).

Reconcile the duplicate constants first. `chat.MemoryContextMessageName`
is exported. `storage.memoryContextMessageName`
(`internal/storage/context_first_message.go:59`) is an unexported
mirror. `internal/agent` must not import `internal/chat`; check
`scripts/check_import_layers.py` before choosing. When the import is
refused, declare the constant in `internal/agent` with a comment
naming both mirrors, and add a test asserting all three agree.

`internal/chat/context_turn_input.go:33` already sets the name for
chat turns. The union covers `internal/subagents`,
`internal/cliagents`, and `internal/composition`.

#### Item C.4 — the threshold arithmetic

Keep `TriggerPercent` 100 and `TargetTokens` at `MaxContextTokens / 2`.

Pin the arithmetic in a test that states it. For
`MaxContextTokens = 1000`:

- `Reserve = 1000 / 5 = 200`.
- `Budget = MaxTokens - Reserve = 800`, four fifths of the ceiling.
- `CompactTrigger() = 100 percent of Budget = 800`, 80 percent of the
  ceiling.
- `CompactTarget() = TargetTokens = 500`, 50 percent of the ceiling.

`TestAdoptSDKCompactionEffectiveThresholds`
(`internal/agent/sdk_summarizer_adapter_test.go:302`) already asserts
these numbers. Keep it. Delete only its `PreferSDKCompaction: true`
line.

#### Item C.5 — the commit candidate

Per Finding F.4, keep `loop.Messages`. Test T.5 pins the guarantee.

#### Item C.6 — `InjectedSummary` byte equality

`confirmSDKCompaction` calls `recordSDKInjectedSummary` with
`pending.message`. After item A.1 the pending message comes from the
memo on a repeated call, so the durable bytes equal the captured
bytes. Test T.6 pins the equality.

#### Item C.7 — the recovery gate observes the skip

Review round 1 rejected revision 1's predicate. That predicate read
`Window != nil && SummaryConfig.Summarizer != nil`. It is true in
every case where a WIRED summarizer nevertheless skips, and the
adapter skips in four such cases:

- `internal/agent/sdk_summarizer_adapter.go:63-65`: no summarizer
  wired.
- `internal/agent/sdk_summarizer_adapter.go:66-69`: a
  `TurnState.Snapshot` failure.
- `internal/agent/sdk_summarizer_adapter.go:91-93`: a
  `BuildSummaryRequest` failure. This is the redaction-refusal path
  that `TestSummaryInjectionRedactionRefusalFallsBack` already pins as
  live.
- `internal/agent/sdk_summarizer_adapter.go:96-100`: any non-retryable
  summarizer failure, including a provider 4xx and an over-budget
  refusal.

In each case, with no prior summary, the SDK returns the original
error and retries nothing. A nil-pointer predicate stands the host
down anyway, so the turn loses recovery on both sides.

Gate on the observed skip instead. `runSDKPromptTooLongRecoverable`
already receives `sdkOpts` and `l`. The stand-down term becomes:

```go
sdkOpts.Compaction.Window != nil && !l.sdkCompactionSkipped
```

The flag is read AFTER the run returns, so it already reflects any
skip the SDK's own `recoverPromptTooLong` attempt produced. Item A.4
sets it.

Update the doc comment. State both conditions. Cite the SDK's
skip-with-no-prior recovery rule.

#### Item C.8 — `sdkPromptBudgetPreflight` is unchanged

Per Finding F.3. See item C.11 and test T.11.

#### Item C.9 — the host rerun discards the abandoned attempt's grounding

Review round 1 found that item C.7 reopens the state half of the
defect commit `190555f0` fixed. The reason is recorded in that
commit's own doc comment
(`internal/agent/agentloop_recovery.go:36-42`): `l` persists across
both `run()` calls with no reset between them.

Under item C.7 a skipping adopted turn reruns host-side. Its SDK
Window still compacted structurally, and `confirmSDKCompaction`
(`internal/agent/agentloop_adoption.go:329`) already grounded that
compaction: `recordPreparation` with `Compacted: true`,
`lastEmittedCompactionKey`, `turnCompactionEmitted = true`, and one
`EmitCompaction`. That is a real emitted compaction over a history the
host is about to discard.

Add `resetAbandonedSDKAttempt()` on `Loop`. Call it in
`runSDKPromptTooLongRecoverable`, immediately after the
`EventAssistantReset` emit at `internal/agent/agentloop_recovery.go:58`
and before the second `run()`.

It calls `resetTurnCompaction()` and additionally clears NINE fields.
Revision 2 listed four. Review round 2 found four more that the
abandoned attempt writes and `resetTurnCompaction` does not touch:

| Field | Written by | Why it must not survive |
|---|---|---|
| `injectedSummary` | `recordSDKInjectedSummary`, `internal/agent/summary_inject.go` | The discarded attempt's summary would reach `commitContextTurn`. |
| `hasInjectedSummary` | same | Same. |
| `summaryFailureReason` | `a.skip`, `internal/agent/sdk_summarizer_adapter.go` | The reason belongs to the discarded attempt. |
| `sdkCompactionSkipped` | item A.4 | Run 2 must record its own skip, not run 1's. |
| `LastPreparation` | `recordPreparation`, `internal/agent/context.go:85` | After item C.10 it carries `Compacted: true` and a `Candidate` built from the discarded history. When run 2 fails before `ObserveRequest` fires, from a `reserveWork` failure or a pre-canceled context, `commitContextTurn` (`internal/chat/turn_finish.go:206`) commits exactly that stale preparation. |
| `HasPreparation` | `recordPreparation`, `:86` | It is the guard every caller reads before trusting `LastPreparation`. |
| `PreparationErr` | `sdkCompactionObserver`, `internal/agent/agentloop_adoption.go:290` | Run 1's observer error would be reported for run 2. |
| `preCompactSource` | `captureOmittedEvidence`, `internal/agent/context.go:23` | It is the pre-compaction history run 1 stashed for its summary request. |

`resetTurnCompaction` (`internal/agent/context.go:88-102`) already
clears `sdkPendingCompaction`, `sdkSummaryMemo`,
`lastEmittedCompactionKey`, `turnCompactionEmitted`, `turnCompacted`,
`turnCompactionKey`, the summary memo, and the six turn accumulators.
Do not duplicate those lines.

**Named accepted residual.** `captureOmittedEvidence`
(`internal/agent/context.go:28-29`) adds items to `l.TurnState` through
`AddEvidence`. `TurnState` has no removal API, so run 1's omitted
evidence survives into run 2's summary request. The plan accepts this
rather than adding a removal API for one call site. The consequence is
bounded: the evidence list is content-free (role plus size bucket, per
INV-AG-40) and the tracker dedupes and bounds it, so the worst case is
a summary that names a message run 2 also dropped. Record the residual
in the builder's completion report.

Order matters. Item C.7's gate is the early return at
`internal/agent/agentloop_recovery.go:52`; the emit is at `:58`. A
reset placed after `:58` therefore cannot clear the flag before the
gate reads it.

Test T.12 pins the outcome: exactly one compaction event, not two.

#### Item C.10 — `confirmSDKCompaction` must preserve the Candidate

`confirmSDKCompaction` (`internal/agent/agentloop_adoption.go:338-343`)
builds a synthetic `contextmgr.Preparation` carrying only `Compacted`,
`Token`, and two counters. `recordPreparation` assigns it wholesale
(`internal/agent/context.go:85`). The `Candidate` the bookkeeping
`Prepare` just produced is discarded, together with its
`SummaryMetadata` and its real `CandidateAlgorithm()`.

When the compaction lands on the final iteration, `commitContextTurn`
(`internal/chat/turn_finish.go:206`) passes that emptied `Preparation`
into `Commit`. `buildCommitRequest`
(`internal/contextmgr/commit_request.go:73`) feeds
`preparation.Candidate.CandidateAlgorithm()` into the checkpoint
identity. The checkpoint identity and its summary metadata therefore
change.

This is unreachable in production today, because no production caller
adopts. It is reachable on every turn after the flip.

**The overlay is field by field. It ASSIGNS the counters; it never
adds them.** Revision 2 said "add `pending.elidedMessages` and
`pending.elidedBytes`". Review round 2 proved that double-counts.
`recordPreparation` both accumulates from and writes back to the same
fields: `l.turnElidedMessages += preparation.ElidedMessages`
(`internal/agent/context.go:73`), then
`preparation.ElidedMessages = l.turnElidedMessages` (`:80`), then
`l.LastPreparation = preparation` (`:85`). So
`l.LastPreparation.ElidedMessages` ALREADY holds the running turn
total. Adding the pending count to it and passing the sum back in
would make a second compaction report `2 * total + pending`.

Build the overlay exactly like this:

| Field | Source |
|---|---|
| `Candidate` | copy from the current `l.LastPreparation` |
| `Messages` | copy from the current `l.LastPreparation` |
| `Token` | copy from the current `l.LastPreparation` |
| `BeforeTokens` | copy from the current `l.LastPreparation` |
| `AfterTokens` | copy from the current `l.LastPreparation` |
| `Compacted` | set to `true` |
| `ElidedMessages` | ASSIGN `pending.elidedMessages` |
| `ElidedBytes` | ASSIGN `pending.elidedBytes` |
| `ElidedReasoningMessages` | ASSIGN `0` |
| `ElidedReasoningBytes` | ASSIGN `0` |

`recordPreparation` stays the single accumulator for all four counters.
Do not change it. The two reasoning counters are zeroed because the SDK
Window does not elide reasoning blocks; a copied non-zero value would
be re-accumulated on every compaction.

Test T.5 asserts the checkpoint's `id` and `summary_metadata` VALUES,
and adds a two-compaction elision sub-assertion. Mutation proof six is
this rule's detector, because
`TestLoopAccumulatesElisionAcrossSteps` is itself being re-derived and
can no longer serve.

#### Item C.11 — the retention-overflow error translation, scoped

Per behaviour-change item 5 and Finding F.3, an adopted turn can now
fail with `plan.ErrRetentionOverflow` under `ErrCompactionFailed`
where the host previously elided and continued, or previously rejected
with `agent.ErrPromptBudgetExceeded`.

Revision 2 proposed a blanket translation. Review round 2 proved that
loses user data. `agent.ErrPromptBudgetExceeded` has three chat-side
readers revision 2 never checked:

- `internal/chat/turn_finish.go:130-139`. `finishErroredContextTurn`
  discards the preparation, drops the pending admission, and returns
  `nil` with no commit and no history adoption.
- `internal/chat/turn_finish.go:294`. `commitPreparedTurn` refuses
  `s.Messages = msgs` for this sentinel.
- `internal/chat/context_integration_turn.go:224-228`.
  `commitErroredPlainContext` discards and returns.

All three carry an "Unreachable today" comment. A blanket translation
makes all three reachable on every adopted chat turn whose compaction
overflows. Today such a turn falls through to the
`!loop.HasPreparation` branch and at least adopts its visible partial
history. Under a blanket translation the user's question and any
streamed partial reply are dropped, and the function returns `nil`.
That is data loss, not a vocabulary change. Test T.10's "no partial
commit happened" assertion would pass and pin the regression as
intended.

**Decision: option A, scope the translation to the preflight's own
shape.** Translate only when `opts.PreparationManager == nil`. That is
exactly the shape `sdkPromptBudgetPreflight` runs on, and exactly the
shape the four subagent tests use. The chat path always wires a
`PreparationManager`, so it keeps `ErrCompactionFailed` and keeps its
partial-history adoption.

Implement in `finishAgentLoopTurn`
(`internal/agent/agentloop_adoption.go`): when
`opts.PreparationManager == nil` and `errors.Is(err,
sdkplan.ErrRetentionOverflow)`, wrap onto
`agent.ErrPromptBudgetExceeded` with `%w` so the SDK sentinel stays
inspectable. Otherwise return the error unchanged.

Option B was weighed and rejected: it would need
`internal/chat/turn_finish.go:130` and
`internal/chat/context_integration_turn.go:224` to distinguish a
preflight rejection from a mid-run overflow, which widens this plan
into the chat error-handling surface for no gain. Option C, a new
sentinel, stays rejected from revision 2.

**The two comments are now half false and must change in the same
commit.** `internal/chat/turn_finish.go:131-135` and
`internal/chat/context_integration_turn.go:225-228` both say
"Unreachable today". After this item the sentinel is reachable from a
subagent-shaped turn's mid-run overflow, not only from the preflight.
Rewrite both to name the two producers and to state that the chat path
is deliberately not one of them.

Tests T.10 and T.11 pin the scoped translation. T.10 gains a chat-shape
sibling asserting the partial history still survives.
---

## Tests

Write the parity tests first. They gate every deletion. A deletion
lands only after its parity test passes.

### T.1 — golden old-versus-new transcript parity, assertive

New file `internal/agent/compaction_parity_test.go`.

Revision 1's version only recorded token counts. Revision 2 made it
compare the two runs, but review round 2 found its third arm vacuous:
the test owns the fake summarizer, so a fake that echoes its
`SourceExcerpts` makes every marker satisfy arm 3 no matter how much
content the flip loses.

Build the fixture so each message carries a unique, greppable marker
string. **Pin the fake summarizer's rendered output to fixed content
that contains NONE of the markers.** Arm 3 then cannot absorb an
arbitrary marker. Have the fake record every `SourceExcerpt` it was
handed, so arm 3 is a checkable fact about the summarizer's input, not
a text match on its output.

Drive one multi-step turn twice over the same scripted completer and
the same starting history: once with the pre-flip wiring, once with
the post-flip wiring. Capture every request.

Assert:

1. Collect the marker set present in the pre-flip final request. Each
   marker is present verbatim in the post-flip final request, present
   inside a post-flip elision notice, or appeared in the
   `SourceExcerpts` the post-flip summarizer was actually handed. Fail
   with the list of markers that satisfy none of the three.
2. Compute the ref-loss list: markers the pre-flip run preserved
   through an `elisionNoticeWithRef` and the post-flip run does not
   carry at all. Assert the list EQUALS a stated expected list written
   into the test as a literal. Behaviour-change item 4 permits the
   loss; a literal expected set makes a LARGER loss fail. A recorded
   list that cannot fail is the pattern review round 1 rejected.
3. No empty assistant turn reaches the provider in either run.
4. The `core-memory-context` frame survives every request after the
   compaction crossing in both runs.

Do not assert "every request fits `MaxContextTokens`". The SDK Window
bounds requests at `Budget()`, four fifths of the ceiling, so that
check cannot fail and proves nothing.
### T.2 — prune-once-then-stabilize on the new path

New test beside `internal/agent/prune_hysteresis_test.go`. Do not
change that file: item C.8 leaves `sdkPromptBudgetPreflight`
unchanged, so both its tests keep passing.

Port the two semantics onto the SDK Window:

- Under the trigger, the history is untouched and no compaction fires.
- On the crossing step, exactly one compaction fires, the result lands
  near the 50 percent target, and an immediate second step fires
  nothing.

Drive it through `loop.Run`, not through a direct function call.

### T.3 — durable round trip

Compact, commit, restart, load, run the next turn. Assert the summary
survives every hop. Colocate with
`internal/clichat/context_summary_auto_resume_integration_test.go:27`.

### T.4 — resume-compaction chaining

A resumed session compacts again. Assert the SDK holds the prior
summary aside and the second compaction's summary supersedes it.
`agentloop/compaction.go`'s `splitSummary` is the mechanism.

### T.5 — checkpoint identity, metadata, and elision accounting

Assert five things:

- The committed checkpoint's JSON key set is byte-identical to the
  pre-flip golden.
- Its `id` value equals the pre-flip value for the same inputs. This
  is item C.10's regression detector; the identity derives from
  `Candidate.CandidateAlgorithm()`.
- Its `summary_metadata` value is non-empty and equals the pre-flip
  value.
- The last observed request equals the committed history prefix, per
  item C.5.
- **Two-compaction elision accounting.** Drive a turn with two real SDK
  compactions. Compute the expected total INDEPENDENTLY of the run: sum
  the two `sdkCompactionOutcome.elidedMessages` values the adapter
  produced, and the two `elidedBytes` values. Assert
  `l.LastPreparation.ElidedMessages` and `.ElidedBytes` equal those two
  sums exactly. Assert `.ElidedReasoningMessages` and
  `.ElidedReasoningBytes` are zero. This is item C.10's
  assign-not-add rule. Under revision 2's "add" wording the first
  number would read `2 * total + pending` and this assertion fails.

### T.6 — summary bytes are identical end to end

Assert `loop.InjectedSummary()` returns the exact bytes the provider
received. Assert `result.Active`'s appended copy carries the same
content with an empty `Name`.

### T.7 — verify the sibling slice's adapter coverage, then fill gaps

Revision 2 specified this test file as new. The concurrent slice
already created it:
`internal/agent/sdk_summarizer_memo_test.go` is present and untracked.
This item therefore becomes a VERIFY-then-fill step, run after the
rebase, not a write-from-scratch step.

Read the shipped file. Check that it covers the seven legacy semantics
below on the SDK path. Add only the rows it does not cover. Do not
rewrite rows it already covers, and do not duplicate them.

| Legacy semantic | Assertion on the SDK path |
|---|---|
| One summarize per compaction across steps | One compaction event costs exactly one `Summarize` provider call. Later steps see byte-identical bytes. |
| A second compaction re-summarizes once | Two distinct compactions cost exactly two calls. Each window's bytes stay stable. |
| Transient failure retries then stops at the cap | A retryable failure costs exactly two calls, then fails the iteration closed. |
| Transient failure recovers on the retry | A retryable failure that succeeds on the retry memoizes the success. |
| Non-retryable failure is not re-attempted | A non-retryable failure costs exactly one call and is memoized. |
| Over-budget drop reports its own reason | An over-budget outcome returns `ErrSummarySkipped` and reports `SummaryReasonOverBudget`. |
| Failure reason clears after a later success | The failure reason is not sticky across compactions. |

The provenance pin the sibling slice named in `buildRequest`'s doc
comment, `TestSDKSummarizerAdapterEvidenceProvenance`, covers
revision 2's item A.3. Confirm it exists and asserts both halves:
`SourceExcerpts` come from the dropped-messages argument, and
`Evidence` comes from the turn-state snapshot. Add the missing half
only if one is absent.

Record in the completion report which rows were already covered and
which this plan added. Do not claim coverage the sibling slice wrote.

### T.14 — `sdkCompactionSkipped` records the LAST attempt

Covers item A.4's per-call reset.

Drive an adopted turn with two real compactions. Make the first
`Summarize` skip through a non-retryable failure and the second
succeed.

Assert:

- After the turn, `l.sdkCompactionSkipped` is false.
- A prompt-too-long rejection after the second compaction does NOT
  rerun the host loop: count the `run()` closure's invocations and
  assert exactly one.

Add the mirror case: first compaction succeeds, second skips, flag is
true, host reruns exactly once. Together the two cases prove the flag
tracks the last attempt rather than any attempt.

### T.8 — the observer contract

New file `internal/agent/sdk_compaction_observer_test.go`.

- `Prepare` runs at least twice on a two-step turn.
- The captured request equals the completer-received request byte for
  byte.
- A recovery retry that captures twice discards the superseded
  preparation.
- A pre-canceled context at the observer seam surfaces the
  interrupted-preparation identity, not a checkpoint conflict.
- An observer error fails the turn with that exact error.

### T.9 — the config decision, verify then fill

The concurrent slice landed the load rejection and modified
`internal/config/context_summary_test.go`. Run these four checks after
the rebase and add only what is missing:

- `enabled = false` is a load error naming the key.
- `enabled = true` loads and is a no-op.
- An absent key loads and is a no-op.
- A workspace with no `[privacy]` section still gets a summarizer.

Add one this plan owns regardless: `.mivia/mivia.toml`, this repo's own
dogfooded config, loads clean after item B.2r deletes its
`[context.summary]` section.

### T.10 — an oversized newest turn has a stated outcome

Covers behaviour-change items 4 and 5, and item C.11.

Drive an adopted turn whose newest turn alone exceeds
`Window.Budget()`. `plan.Compact` keeps the mandatory set, so
`checkCompactedBudget` fails closed.

Assert:

- The turn's surfaced error satisfies
  `errors.Is(err, agent.ErrPromptBudgetExceeded)`.
- The error also satisfies
  `errors.Is(err, sdkplan.ErrRetentionOverflow)`, proving the wrap
  kept the original inspectable.
- No partial commit happened. This is the SUBAGENT shape only:
  `opts.PreparationManager == nil`, the one shape item C.11 translates.

**Chat-shape sibling, required.** Drive the same oversized turn with a
`PreparationManager` wired, the chat shape. Assert:

- The surfaced error is NOT `agent.ErrPromptBudgetExceeded`. It stays
  `ErrCompactionFailed` over `plan.ErrRetentionOverflow`.
- The turn still adopts its visible partial history, through
  `finishErroredContextTurn`'s `!loop.HasPreparation` branch
  (`internal/chat/turn_finish.go:141`).
- The user's question is not dropped.

Without this sibling, item C.11's scoping is unproven and a later
widening of the translation would silently reintroduce the data-loss
path review round 2 found.

Add a sibling test that records behaviour-change item 4: the same
oversized tool result under the pre-flip wiring produces an
`elisionNoticeWithRef` message with a resolvable spool ref, and under
the post-flip wiring produces none. This test documents the loss. It
asserts the pre-flip ref exists, so it fails if the legacy path's
spool wiring ever silently breaks.

### T.11 — the subagent double mechanism

Covers Finding F.3's unanalysed half.

Drive a subagent-shaped turn through `loop.Run`: no
`PreparationManager`, a positive `MaxContextTokens`, and a history
sized just under the preflight's admission bar. The preflight admits a
history it pruned with `PruneMessagesKeepTurns`. The Window then
re-plans it under `plan.Compact`'s different mandatory-retention rule.

Assert the outcome is either a successful turn or
`agent.ErrPromptBudgetExceeded`. Assert it is never a bare
`ErrCompactionFailed`. Item C.11's translation is what makes the
second branch hold.

### T.12 — the abandoned attempt emits no phantom compaction

Covers item C.9.

Drive an adopted, summarizer-less turn whose completer rejects with
`provider.ErrPromptTooLong` on every call. The SDK Window compacts
structurally, `confirmSDKCompaction` grounds it, and the host then
reruns because item C.7's flag records the skip.

Assert:

- Exactly one compaction event is emitted for the whole turn, not two.
- After the turn, `l.InjectedSummary()` reports no summary.
- `l.LastPreparation.Compacted` reflects the second attempt only.

### T.13 — a wired summarizer that skips still gets host recovery

Covers item C.7 directly.

Wire a summarizer. Make `BuildSummaryRequest` refuse, which is the
live redaction-refusal path. Drive a turn that hits
`provider.ErrPromptTooLong`.

Assert the host rerun fires and exactly one `EventPrune` is emitted.

---

## Tests whose assertions invert

Every entry was found by grep. No test is deleted to make the change
pass. Where a fixture must move, the plan names the new fixture.

### The raw `summaryProbeOptions` sweep

Review round 1 found revision 1's tables incomplete. The cause is
`summaryProbeOptions` (`internal/agent/summary_inject_test.go:99`). It
sets `MaxContextTokens`, a `PreparationManager`, AND a
`SummaryConfig.Summarizer`, so every caller adopts under the new gate.
On an adopted turn `sdkCompactionObserver` runs `Prepare` but never
calls `injectSummaryAfterPrepare`, so no host summary reaches the
request. `summaryMessagesPerRequest`
(`internal/agent/summary_memo_test.go:43-54`) then calls `t.Fatalf`.

The raw unfiltered sweep is `grep -rn "summaryProbeOptions("
--include=*.go .`. It returns 18 hits: the definition plus 17 call
sites. Every hit is dispositioned below.

**Disposition rule.** `summary_memo_test.go` and the legacy half of
`summary_inject_test.go` keep testing the LEGACY path. Item C.2 keeps
that path alive at `MaxContextTokens == 0`. Their fixtures therefore
move to a zero ceiling. Test T.7's port is ADDITIONAL coverage on the
SDK path, never a substitute.

Add one helper beside `summaryProbeOptions`:

```
summaryProbeOptionsLegacyPath(t, summarizer, probe) Options
```

It calls `summaryProbeOptions(t, summarizer, probe, 0)` and THEN sets
`PreparationInput.Budget` explicitly to `100_000`. The explicit set is
required: `summaryProbeOptions` assigns `Budget: maxContextTokens`
(`internal/agent/summary_inject_test.go:112`), so passing 0 would
otherwise leave `Budget: 0`. Revision 2 said the budget was "left at a
positive value", which contradicted itself; review round 2 was right.

A zero ceiling keeps `applySDKTrim` installing `Trim`, because
`sdkPrepareTrim` gates only on `PreparationManager != nil`
(`internal/agent/sdk_prepare.go:92-95`). `injectSummary` therefore
still runs. `SummaryRequestBudget(0)` returns
`defaultSummaryRequestBudget` (`internal/agent/summary_inject.go:40-47`),
so the summary request stays valid.

| Call site | Test | Action |
|---|---|---|
| `summary_memo_test.go:71` | `TestSummaryInjectionOneSummarizePerCompactionAcrossSteps` | Switch to `summaryProbeOptionsLegacyPath`. Every assertion unchanged. |
| `summary_memo_test.go:126` | `TestSummaryInjectionSecondCompactionResummarizesOnce` | Same. |
| `summary_memo_test.go:172` | `TestSummaryTransientFailureRetriesAcrossStepsThenStopsAtTheCap` | Same. |
| `summary_memo_test.go:200` | `TestSummaryTransientFailureRecoversOnTheNextStep` | Same. |
| `summary_memo_test.go:248` | `TestSummaryNonRetryableFailureIsNotReattemptedAcrossSteps` | Same. |
| `summary_memo_test.go:267` | `TestSummaryOverBudgetDropReportsItsOwnReason` | Cannot move to a zero ceiling. `SummaryOverBudget` (`summary_inject.go:59-64`) treats a non-positive budget as unbounded, so the premise needs the 400 ceiling. **The conversion is NOT verbatim.** Today's assertion is `anyRequestCarriesSummary(completer.requests)`: the summary never reached THE PROVIDER. A direct `injectSummary` call can only assert absence from a RETURN VALUE, a different artefact, which is the wrong-artefact trap the `two_paths_execute_a_tool_call` memory names. Keep BOTH arms: (a) the direct unit call asserting the returned slice carries no summary and `SummaryFailureReason` equals `SummaryReasonOverBudget`, and (b) a driver-level arm that still runs `loop.Run` on the LEGACY path at a ceiling small enough to be over budget but large enough that the SDK Window does not adopt, asserting `anyRequestCarriesSummary` is false. When no such ceiling exists, say so in the test comment and name arm (a) plus test T.7's over-budget row as what covers the guarantee. |
| `summary_memo_test.go:289` | `TestSummaryFailureReasonClearsAfterALaterSuccessfulCompaction` | Switch to `summaryProbeOptionsLegacyPath`. Every assertion unchanged. |
| `summary_inject_test.go:190` | `TestSummaryInjectionSentRequestCarriesSummary` | Switch to `summaryProbeOptionsLegacyPath`. Assertion unchanged. |
| `summary_inject_test.go:237` | `TestSummaryInjectionDoesNotTouchDurableState` | Same. |
| `summary_inject_test.go:356` | `TestSummaryInjectionSummarizerErrorFallsBackStructural` | Same. |
| `summary_inject_test.go:372` | `TestSummaryInjectionRedactionRefusalFallsBack` | Same. Keeping it on the legacy path also keeps it a live pin for item C.7's cited refusal case. |
| `summary_inject_test.go:401` | `TestSummaryInjectionOverBudgetFallsBack` | Same problem as `summary_memo_test.go:267`, same two-arm fix. This test carries a THIRD assertion the conversion would drop: `loop.Run` returns no error, the end-to-end proof that an over-budget summary degrades instead of failing the turn. Keep that assertion on the driver arm. Do not label this conversion verbatim. |
| `summary_inject_test.go:416` | `TestSummaryInjectionNonCompactedNeverInjects` | Switch to `summaryProbeOptionsLegacyPath`. Assertion unchanged and no longer vacuous. |
| `summary_inject_test.go:303` | `TestSummaryInjectionIdempotencyKeyStableAcrossRuns` | Switch to `summaryProbeOptionsLegacyPath`. Both assertions survive with no retune. |
| `summary_inject_test.go:592` | `TestSummaryInjectionToolFactsReachLaterRequest` | Currently skipped (`summary_inject_test.go:585`). Switch the fixture and leave the skip. Do not add or remove a skip. Update the skip text only if it names the flipped gate. |
| `summary_inject_test.go:630` | `TestSummaryInjectionTurnStateFactsReachProvider` | Switch to `summaryProbeOptionsLegacyPath`. Assertion unchanged. Test T.7 adds the SDK-path equivalent; this original is NOT removed. |
| `summary_inject_test.go:709` | `TestSummaryInjectionCarriesDroppedContent` | Switch to `summaryProbeOptionsLegacyPath`. Assertion unchanged. Test T.7 adds the SDK-path equivalent through `SourceExcerpts`. |
| `prefix_cache_stability_test.go:323` | `TestPrefixCacheStabilityCompactionDivergesOnceThenNextTurnStabilizes` | Cannot move to the legacy path: its whole subject is the SDK-path prefix behaviour after the flip. See the next section. |

Revision 1 wrote "lower `summaryProbeOptions`'s ceiling". Review round
1 correctly rejected that: the ceiling is a parameter, shared by 17
call sites. The table above is per call site instead.

### The prefix-cache test

`TestPrefixCacheStabilityCompactionDivergesOnceThenNextTurnStabilizes`
(`internal/agent/prefix_cache_stability_test.go:299`) cannot be
re-anchored by moving the summary's expected index. Review round 1
proved two assertions fail before any placement change:

- `assertTurn1CompactionInvariants` (`:367`) splits each step with
  `splitTrailingNamed` (`:381`). It finds the summary only because the
  host re-appends it at the TAIL. The SDK injects at index 1 through
  `injectAfterSystem`, so the frame lands in the structural half and
  `findNamedMessage` returns not-found.
- `firstDivergingIndex(structuralByStep[i], structuralByStep[i+1]) !=
  len(structuralByStep[i])` (`:384-387`) demands the earlier history
  be an exact prefix of the next step's. It passes today only because
  `stepKeyedCompactingProbe` (`:558-573`) drops nothing: it calls
  `CapturePreparation` with `input.Messages` unchanged and marks
  `Compacted: true`. Making the SDK Window actually cross requires
  real drops, which breaks prefix extension by construction.

**The property that survives.** Prefix-cache stability does not
require a monotone prefix across every step. It requires that the
prefix breaks at most once per turn, and that the break is bounded and
predictable. Assert that instead:

1. The system message and the `tools` array stay byte-identical at
   every step. Unchanged from today (`:369-378`).
2. Exactly one step boundary diverges inside the earlier history.
   Every other boundary extends the prefix exactly, as today.
3. At that one boundary, the divergence index equals the count of
   leading messages the window preserves: the system message plus the
   `PreserveNames` frames. That number is derivable from the fixture,
   so assert the exact integer, not an inequality.
   **Ordering is load-bearing.** The exact-index formula holds only
   when the name-based extraction has ALREADY removed the summary
   frame from the structural slices. `injectAfterSystem` places the
   frame inside the leading region, so comparing structural slices
   that still contain it would put the divergence at the frame's own
   index instead. Extract by name first, compare second. State this
   ordering in the helper's doc comment.
4. From the compaction step onward, the summary frame sits at the
   SDK's fixed slot and its bytes never change.
5. The simulated next turn re-establishes a clean, monotone prefix.
   Unchanged from today.

Replace `splitTrailingNamed` with a name-based extraction that finds
the frame at any index. Rename `assertTurn1CompactionInvariants` to
`assertTurn1CompactionPrefixInvariants` so the new contract is not
mistaken for the old one.

This is stronger in one respect and weaker in another. It is stronger
because it pins the exact divergence index rather than only "no
divergence". It is weaker because it permits one divergence where
today permits none. That trade is forced by real drops. State it in
the test comment.

The fixture must also cross the trigger. Lower the ceiling at
`prefix_cache_stability_test.go:323` from `100_000` to a value the
fixture's history exceeds. Replace `stepKeyedCompactingProbe` with a
probe that returns `input.Messages` unchanged, since the
`PreparationManager` no longer compacts. Verify the crossing step
matches the probe's old `compactOn` step, so the "exactly one
boundary" assertion still has one boundary.

If no deterministic single crossing can be produced, rollback
criterion 3 applies. Stop and return to plan review.

### Compile failures

| Test | Path:line | Cause | Action |
|---|---|---|---|
| `TestSDKCompactionAdoptedRequiresOptInWithPreparationManager` | `internal/agent/sdk_summarizer_adapter_test.go:271` | `base.PreferSDKCompaction = true` | The premise is gone. Replace with `TestSDKCompactionAdoptedNeedsOnlyACeiling`: a ceiling alone adopts; a zero ceiling does not. |
| `TestAdoptSDKCompactionEffectiveThresholds` | `internal/agent/sdk_summarizer_adapter_test.go:302` | `PreferSDKCompaction: true` in the literal | Delete that one line. Every window assertion stays unchanged. |

### Adoption assertions

| Test | Path:line | Assertion today | Replacement |
|---|---|---|---|
| `TestBuildAgentLoopOptions_NoWindowWithPreparationManager` | `internal/agent/agentloop_adapter_test.go:110`, assert `:127` | `Window != nil` fails | Rename to `TestBuildAgentLoopOptions_WindowAdoptsWithPreparationManager`. Assert `Window != nil`, `Summarizer` is a `*sdkSummarizerAdapter`, `ObserveRequest != nil`, `Trim == nil`. Four assertions replace one. |
| `TestContextWindowForwardedOnlyOnAdoptedCompaction` | `internal/agent/agentloop_adapter_test.go:138`, asserts `:144`, `:147`, `:162` | three `want 0` cases | Invert all three to `want 1000`. Add a `MaxContextTokens: 0` case asserting 0, the only unforwarded case left. |
| `TestBuildAgentLoopOptions_SDKCompactionNeedsSummarizer` | `internal/agent/agentloop_adapter_test.go:338`, asserts `:348`, `:351` | `Window == nil` without a summarizer | Rename to `TestBuildAgentLoopOptions_SDKCompactionAdoptsWithoutASummarizer`. Assert the triple IS wired, and that `Summarize` on the wired adapter returns `plan.ErrSummarySkipped`. This proves Finding F.1's mechanism. |
| `TestBuildAgentLoopOptions_NeverWiresWindow` | `internal/agent/sdk_advertised_test.go:206`, assert `:213` | passes only because `Options{}` has a zero ceiling | Rename to `TestBuildAgentLoopOptions_NoWindowWithoutACeiling`. Assertion unchanged; the comment becomes true. |
| `TestApplySDKTrimStandsDownOnAdoptedCompaction` | `internal/agent/agentloop_adapter_test.go:87`, asserts `:98`, `:107` | the `unadopted` fixture carries `MaxContextTokens: 1000` | Change the `unadopted` fixture to `MaxContextTokens: 0` with a `PreparationManager`. Both assertions keep their direction. This is Finding F.2's case, now tested on purpose. |
| `TestBuildAgentLoopOptions_AdoptionRows` | `internal/agent/agentloop_adapter_test.go:236` | passes; silently becomes an adopted turn with a nil host summarizer | Add an explicit assertion that the wired `Summarizer` returns `plan.ErrSummarySkipped`. No assertion is removed. |
| `TestPromptTooLongOnAdoptedWindowDoesNotRerun` | `internal/agent/agentloop_recovery_sdk_compaction_test.go:43` | the host rerun does not fire | Its fixture must wire a summarizer that does NOT skip, or item C.7's gate makes the host rerun and the test fails. Verify the fixture and retune it. Tests T.12 and T.13 cover the two skip cases. |

### Preparation-driven compaction fixtures

These stop compacting because the observer runs `Prepare` with
`Budget = math.MaxInt`. Each is re-anchored so the SDK Window
compacts. The assertion moves from "the PM compacted" to "the request
was compacted", the fact that always mattered.

| Test | Path:line | Assertion today | Replacement |
|---|---|---|---|
| `TestPrepareSDKHistoryCompactsThroughManager` | `internal/agent/agentloop_options_test.go:450`, assert `:475` | `seen == 3`, the manager's keep count | Rename to `TestPrepareSDKHistoryCompactsThroughTheWindow`. Set the ceiling at this call site so the Window crosses. Assert the request is shorter than the input and the newest turn survives. |
| `TestPrepareSDKHistoryInjectsSummary` | `internal/agent/agentloop_options_test.go:556`, assert `:598` | tail message `Name == SummaryMessageName` | Set the ceiling at this call site so the Window crosses. Assert the summary frame sits at the SDK's fixed slot, directly after the leading system message. An exact index is stronger than "the tail". |
| `TestPrepareSDKHistoryNoSummarizerCompactsOnly` | `internal/agent/agentloop_options_test.go:604` | `lastName != SummaryMessageName` | Keep the assertion. Add that the request IS shorter, so it is no longer vacuous. |
| `TestAgentTurnDiscardsFailedPreparation` | `internal/agent/context_loop_test.go:74`, assert `:96` | discard or no preparation | Verify. The observer runs `Prepare` before the `Chat` call, so the path holds. Re-run and record. |
| `TestAgentTurnRetainsSuccessfulPreparationForSessionCommit` | `internal/agent/context_loop_test.go:99`, assert `:127` | `loop.HasPreparation` | Verify. Same reason. |
| `TestAgentDeadlineDoesNotUseBackgroundPreparationFallback` | `internal/agent/context_loop_test.go:127` | `probe.calls == 1`; the fixture has NO ceiling | This is Finding F.2's case. It keeps working because item C.2 keeps `applySDKTrim`. No change. |
| `TestLoopAccumulatesElisionAcrossSteps` | `internal/agent/elision_accumulation_test.go:72`, asserts `:99`-`:110` | exact `BeforeTokens`, `AfterTokens`, `ElidedMessages`, `ElidedBytes` | Item C.10 changes which fields survive. **Do not re-derive the numbers from the run and restate them**: revision 2 said that, and review round 2 correctly called it the fixture-disables-the-guard pattern `.agents/rules/20-agent-quality.md` names. Derive each expected number from the FIXTURE instead — the message counts and byte lengths the probe elides — so a wrong accumulator fails rather than being baked in. Keep exact equality; do not relax to an inequality. Test T.5's two-compaction sub-assertion and mutation proof six are the primary detectors for item C.10; this test is the secondary one. |
| `TestLoopResetsTurnCompactionOnNewRun` | `internal/agent/elision_accumulation_test.go:114` | reset across runs | Verify. `resetTurnCompaction` gains fields but keeps its contract. |
| `TestPrepareStepWiresCalibrationRatio` | `internal/agent/loop_calibration_test.go:107`, asserts `:127`, `:143` | `probe.lastInput.CalibrationRatio` | Verify. `buildPrepareInput` feeds the observer too. `Budget` is clobbered; the ratio is not. |
| `TestLoopRealPrepKeepsElisionAcrossNonCompactingStep` | `internal/agent/elision_structural_loop_test.go:20` | real structural elision at a chosen budget | The observer's `math.MaxInt` defeats the fixture. Move it to a direct `StructuralPreparationManager` unit test that does not go through the loop. The elision behaviour it pins is real and unchanged; only its driver is no longer reachable. State that in the test comment and cite behaviour-change item 1. |
| `TestSubagentParentSteerCompactsWithoutDTOError` | `internal/subagents/multi_step_steer_sdk_test.go:170` | status completed, with `Force: true` to compact each step | `Force` no longer compacts. Re-anchor: assert the SDK Window compacts and steering still completes. Without this the test goes green while testing nothing. |
| `TestSDKSessionTurnRetriesAfterPromptTooLong` | `internal/chat/session_sdk_backend_test.go:237`, asserts `:256`, `:259`, `:268` | reply `recovered`, one `EventPrune` | This is Finding F.1. With item C.7's corrected gate the adapter skips, so the host rerun still fires and all three assertions pass unchanged. No test change. Keep it green; it is the regression detector. |
| `TestAgentLoopCompactionSummarySurvivesTheTurnBoundary` | `internal/clichat/context_summary_persistence_test.go:105`, asserts `:126`, `:129`, `:145` | summary in the request, in Active, and in the next turn | `driveCompactingTurn` tightens the prompt budget. That now drives the SDK Window. Verify the crossing still happens; retune the budget when it does not. Assertions unchanged. |
| `TestContextSummaryIntegrationEndToEnd` | `internal/clichat/context_summary_integration_test.go:183` | end-to-end summary | Same harness, same verification. Also fix its stale doc comment (item B.4). |
| `TestContextSummaryIntegrationDegradesOnBadReply` | `internal/clichat/context_summary_integration_test.go:224` | degrade path | Same harness, same verification. |
| `TestCompactionChannelsAutomaticEventOmitsSummaryAndPersistsToCheckpoint` | `internal/clichat/context_summary_channels_integration_test.go:293` | event shape | Same harness, same verification. |
| `TestCompactionChannelsStructuralOnlyNamesTheMissingCondition` | `internal/clichat/context_summary_channels_integration_test.go:349` | `enabled = false` sends no summary request | An item B.2 inversion. The fixture config is now a load error. Re-anchor onto a remaining honest cause: no resolved endpoint. The assertion that the notice names the real missing condition is unchanged. Also fix the package doc at `:12-16`. |
| `TestAutoCompactionSummarySurvivesRestart` | `internal/clichat/context_summary_auto_resume_integration_test.go:27`, assert `:46` | tail contains the host-injected marker | Verify the crossing still happens. Assertion unchanged. |
| `TestCompactionKeepsMemoryFrameInCommittedAndRestoredContext` | `internal/chat/context_memory_frame_compaction_test.go:38` | the memory frame survives | The protector becomes `PreserveNames` (item C.3). Assertion unchanged. This is item C.3's regression detector. |
| `TestOneShotRejectsIrreduciblePrompt` | `internal/subagents/context_policy_test.go:16` | `agent.ErrPromptBudgetExceeded` | Unchanged by item C.8, and item C.11 keeps the vocabulary stable when the Window fails instead. Re-run and record. |
| `TestMultiStepHandlerCarriesPromptBudgetToAgentLoop` | `internal/subagents/multi_step_test.go:132` | budget carried | Unchanged. Re-run and record. |
| `TestOneShotHandlerPreflightAndOutputReserve` | `internal/subagents/oneshot_test.go:216` | preflight | Unchanged. Re-run and record. |
| `TestOneShotHandlerDoesNotChargeOutputReserveAgainstPromptBudget` | `internal/subagents/oneshot_test.go:247` | reserve accounting | Unchanged. Re-run and record. |

### Config-gate inversions

These rows are CUT unless the rebase shows otherwise. The concurrent
slice modified `internal/config/context_summary_test.go` and the
`internal/clichat` setup files, so it very likely owns these
inversions too. Re-run each named test after the rebase. Act on a row
ONLY when its test still fails.

| Test | Path:line | Assertion today | Replacement |
|---|---|---|---|
| `TestContextSummaryExplicitOptOut` | `internal/config/context_summary_test.go:18` | `enabled = false` disables | Rename to `TestContextSummaryExplicitOptOutIsALoadError`. Assert the load error names the key. An error is a harder contract than a boolean. |
| `TestContextSummaryExplicitOptIn` | `internal/config/context_summary_test.go:28` | `enabled = true` resolves enabled | Assert the config loads without error. `SummaryEnabled` is gone. |
| `TestContextSummaryDefaultsOn` | `internal/config/context_summary_test.go:10` | absent key resolves on | Assert an absent key loads and wires a summarizer, through `summaryWiring`. Moves the assertion from the resolved struct to observable behaviour. |
| `TestSummaryWiringDisabledByDefault` | `internal/clichat/context_summary_setup_test.go:37`, assert `:40` | `enabled=false` wires nothing | Delete the premise; rename to `TestSummaryWiringNeedsAnEndpoint`. Assert a missing `BaseURL` is the remaining disabling cause. |
| `TestSummaryDisabledReasonNamesTheMissingCondition` | `internal/clichat/context_summary_reason_test.go:33`, case `:39` | `flag off` names `context.summary` | Delete the `flag off` case. Add a `no binding` case naming the binding. Case count stays at two; both are reachable states. |
| `TestSummaryWiringDoesNotRequireRedaction` | `internal/clichat/context_summary_setup_test.go:55` | no `[privacy]` still wires | Unchanged. Keep. |
| `TestSummaryDisabledReasonIgnoresMissingRedaction` | `internal/clichat/context_summary_reason_test.go:63` | no `[privacy]` is not a cause | Unchanged. Keep. |

### Skips

`internal/agent/summary_inject_test.go:585`,
`internal/agent/loop_context_fix_test.go:216`, and
`internal/agent/loop_retry_steer_test.go:199` carry skips. Read each
reason. Update the wording only where it names the flipped gate. Do
not add a skip. Do not remove one without restoring the test.

---

## Invariants

`.mivia/invariants.md` needs three updates in the same commit.

- **INV-AG-39** ("A compaction's summary outlives the turn that
  produced it", line 70). Its clause "One compaction event costs at
  most ONE summarizer call" is today proven by `summary_memo_test.go`.
  Under the raw-sweep disposition those seven tests keep their names
  and their assertions, so no listed name goes stale. Add the SDK-path
  names the concurrent slice's `sdk_summarizer_memo_test.go` carries,
  plus any test T.7 adds, since the invariant now has two mechanisms.
  Read the shipped file for the real names after the rebase; do not
  invent them. `TestSummaryOverBudgetDropReportsItsOwnReason` keeps its
  name and gains a second arm, so the manifest entry stays valid.
- **INV-AG-41** ("An automatic compaction announces itself on every
  surface", line 72). Confirm the SDK path reaches
  `emitContextCompaction` through `confirmSDKCompaction`. Add tests
  T.1 and T.12.
- **INV-AG-42** ("A `[context.summary]` provider/model override is
  real, not decorative", line 73). It lists
  `TestSummaryWiringDisabledByDefault`, which this plan renames. Update
  the name. The override contract does not change.

Run `make validate-invariants` after the edits. It fails on a stale
test name.

---

## File size budget

`internal/agent/agentloop_adoption.go` is 451 lines. The soft limit is
500. Items C.1, C.3, C.10, and C.11 add lines. Move
`sdkCompactionObserver` and `confirmSDKCompaction` into a new file
`internal/agent/sdk_compaction_observer.go` BEFORE adding anything.
Item C.10's field-by-field overlay lives in that new file, beside
`confirmSDKCompaction`. Item C.11's translation lives beside
`finishAgentLoopTurn`, which stays in `agentloop_adoption.go`; check
the file again after the move and split further when it is still near
the limit.

`internal/agent/sdk_summarizer_adapter.go` grew under the concurrent
slice. Item A.4 adds three lines to it and one to
`internal/agent/sdk_summarizer_memo.go`. Re-measure both files after
the rebase.

`internal/agent/summary_inject_test.go` and
`internal/agent/summary_memo_test.go` grow by the fixture switch.
Check both against the 800-line test soft limit after the edit.

Keep every function at or below 80 lines. Item A.2's retry loop goes
in its own method.

---

## Verification

Run these in order. Record every result in the completion report.

1. `go build ./...`
2. `go vet ./...`
3. `go test -run 'TestSDK|TestSummary|TestCompaction|TestPrefixCache|TestAdopt|TestBuildAgentLoopOptions|TestPrepareSDK|TestLoop|TestAgentTurn|TestContextSummary|TestApplySDKTrim|TestObserver|TestPromptTooLong|TestSubagent|TestOneShot|TestMultiStep' ./internal/agent/... ./internal/config/... ./internal/clichat/... ./internal/chat/... ./internal/subagents/... ./internal/composition/...`
4. `go test -race` on the same package set, same filter.
5. `make invariants`
6. `make validate-invariants`
7. `make verify`

Do not run a package-wide or `./...` test run. This repo's memory
`workflow-rules-no-big-test-suites-absolute-local-sdk-replace-shared-tree-etiquette`
prohibits it.

Do not run `go test -fuzz` with default parallelism.

Do not run a live e2e workflow. `scripts/e2e_context_compaction.py`
changes under item B.2, but running it needs the user's explicit
request in the session.

Never bypass a Git hook.

### Mutation proofs

`.agents/rules/20-agent-quality.md` requires a mutation proof for
every guard. Five guards land here. Review round 1 added the last two.

| Guard | Mutation | Test that must fail |
|---|---|---|
| The item C.3 `PreserveNames` union | Drop the memory-context name from the union | `TestCompactionKeepsMemoryFrameInCommittedAndRestoredContext` |
| The item C.7 observed-skip term | Replace `!l.sdkCompactionSkipped` with `opts.SummaryConfig.Summarizer != nil` | T.13, and `TestSDKSessionTurnRetriesAfterPromptTooLong` must stay green |
| The item C.9 abandoned-attempt reset | Delete the `LastPreparation` and `HasPreparation` lines from `resetAbandonedSDKAttempt` | T.12 |
| **Item C.10's assign-not-add rule** | Change `ElidedMessages = pending.elidedMessages` to `+= pending.elidedMessages` | T.5's two-compaction sub-assertion |
| **Item C.11's scoping** | Delete the `opts.PreparationManager == nil` term, restoring revision 2's blanket translation | T.10's chat-shape sibling |

The revision 2 rows for the item A.1 memo and the `enabled = false`
load rejection are both dropped: the concurrent slice owns those guards
and ships their proofs. Verify after the rebase
that the shipped test suite carries one, and say so in the report
rather than claiming it.

`TestLoopAccumulatesElisionAcrossSteps` can no longer serve as item
C.10's detector, because its expected numbers are being re-derived in
this same commit. That is why mutation proof five above names T.5's
sub-assertion instead.

Apply each mutation, confirm the named test fails, revert, and record
the result. An inspection-only proof is invalid.

---

## Plan scorecard

| Criterion | Result |
|---|---|
| Compiles | PASS. No new package. No import-direction change. |
| No cycles | PASS. Item C.3 may need a constant instead of a `chat` import; the plan says so. |
| No breaking API change | FAIL by design. `Options.PreferSDKCompaction` and `ContextSummaryConfig.SummaryEnabled` are deleted. Both are internal. Neither has an external consumer. |
| Testable in isolation | PASS. Items A, B, and C each have their own tests. |
| Backward-compatible config | FAIL by design. `enabled = false` becomes a load error. Item B.2 records the decision and the migration. |
| Every function has a test | PASS. |
| Tree green at end of commit | CONDITIONAL. The raw-sweep table dispositions all 17 `summaryProbeOptions` call sites, which revision 1 left red. It is not provable while the concurrent slice is uncommitted, because that slice moves line numbers in every table here. The rebase rule is the precondition. |
| No collision with concurrent work | FAIL until the rebase. Items A.1 to A.3 are cut. Section B is disputed on evidence; the rebase rule settles it. |

## Rollback criterion

Roll back to plan review when any of these holds:

- Test T.1 shows the post-flip transcript loses named content the
  pre-flip transcript kept, beyond behaviour-change items 4 and 5, and
  `PreserveNames` cannot protect it.
- The elision loss named under "Behaviour change this plan accepts"
  turns out to be load-bearing for a shipped surface.
- The prefix-cache fixture cannot produce a deterministic single
  compaction crossing on the SDK Window.
- Item C.11's translation cannot keep `agent.ErrPromptBudgetExceeded`
  as the surfaced error for the subagent shape, so test T.11 cannot
  pass.

## Out of scope

A later slice deletes what this plan makes dead. This plan deletes
none of it:

- `sdkPrepareTrim`, `prepareSDKOnce`, and `injectSummaryAfterPrepare`.
- The `injectSummary` loop path.
- The pre-canceled-context special case in `sdkInitialHistory`.
- `Options.ObserveRequestHistory`.
- `sdkPromptBudgetPreflight` and its narrowing.

Three corrections to that list, verified against the tree:

- `Options.ObserveRequestHistory` is NOT dead after this plan.
  `internal/chat/session_turn_options.go:96` wires it, and
  `sdkCompactionObserver` (`internal/agent/agentloop_adoption.go:303`)
  calls it on every adopted turn. Only `sdk_prepare.go:115`'s call
  becomes unreachable on a ceiling-carrying turn.
- `sdkPromptBudgetPreflight` is not narrowed here. See Finding F.3.
- The legacy summary path is not dead either. Item C.2 keeps it
  reachable at `MaxContextTokens == 0`, and the raw-sweep disposition
  keeps its whole test suite exercising it.

`RenderSummaryMessage` and `InjectSummaryMessage` STAY. Manual
`/compact` (`internal/chat/compact_summary.go:60`) and plain chat
(`internal/chat/summary_inject.go:52`, `:56`) both use them. Neither
path goes through the agent loop.
