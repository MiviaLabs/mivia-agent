# tools/07 - Privileged tier gating: defer the session-tool block

**Status:** ❌ **REJECTED AT STEP 0** (2026-08-03). Do not implement from this
document. Two of three hostile reviewers returned REJECT; the decisive finding
is §8 C3 - the compiled default agent prompt mandates every tool this plan
withholds and never mentions `load_tools`, so for the shipped population the
feature is **+397 tokens/request worse than HEAD** and trips this plan's own
rollback criterion.
**Retained because:** the measurements in §3 are correct and were reproduced
exactly by an independent reviewer, and §8.2 records what a successor must do.
The *goal* survived review; the *mechanism* did not.
**Sections §4-§7 are superseded** - they describe the rejected design and are
kept only as the record §8 disposes of.
**Date:** 2026-08-03
**Depends on:** plan `tools/05` (shipped).
**Blast radius:** claimed MEDIUM-HIGH; review established it is higher - a
successor pulls in prompt composition (§8.2 item 1) and a prerequisite fix to
`tools/05`-era handle keying (§8.2 item 4).

## 1. Goal

Withhold the schemas of the privileged orchestration tools and the run-history
readers from the advertised surface at the point the dispatcher registers them,
so a session that never delegates stops paying 2,607 tokens per request for
delegation it does not use.

## 2. Why this slice exists

Plan `tools/05` §14.3 measured deferred tool loading as net-negative: agent
scoping via `EffectiveTools`, already shipped, saves more than the mechanism
does. But it also found the reason, and the reason is actionable. Two
independent facts make a 2,897-token block unreachable by **both** mechanisms:

- `scopeAdmits` at `ScopeRoot` returns `true` for any `PrivilegedTool` before
  it consults the allowlist (`internal/tools/scope.go:138-141`), so
  `EffectiveTools` scoping structurally cannot exclude the six orchestration
  tools. That is deliberate: delegation must stay available to the root.
- `state.ToolBase = sess.Tools.Clone()` runs at `internal/cli/chat_repl.go:252`,
  before `NewSessionDispatcher` at `:134`. Every session tool - privileged and
  ledger alike - is registered *after* that snapshot, so `planToolTiers` never
  sees one and `tools/05` can never nominate one as a deferred candidate.

Deferred loading and agent scoping compete everywhere else and scoping wins.
Here they do not compete: scoping cannot reach this block at all, so a
mechanism that can is strictly additive.

## 3. Measured baseline (2026-08-03, at HEAD)

Methodology is `tools/05` §14.3's: `provider.EstimateToolSchemaCost` over the
real `NewDefaultRegistry` surface after a real `NewSessionDispatcher`. Verified
additive - the sum of per-tool costs equals the whole-array estimate exactly
(2,532 = 2,532), so per-tool figures compose without a framing correction.

| Block | Tools | Tokens | Reachable by scoping? | Reachable by tools/05? |
|---|---|---|---|---|
| Workspace | 11 | 2,532 | yes | yes |
| Privileged orchestration | 6 | 2,013 | **no** | **no** |
| Ledger / run-history | 3 | 884 | **no** | **no** |
| **Advertised total** | **20** | **5,429** | | |

Per tool: `dispatch_tasks` 611, `spawn_agent` 607, `delegate` 390, `join_run`
167, `inspect_agents` 123, `cancel_run` 115; `ledger_read` 300,
`list_run_events` 294, `read_output` 290.

These supersede §14.3's figures (2,029 / 884 / 5,179): descriptions have since
changed and `find_references` was added. The ledger block is unchanged.

**Deferral machinery, measured, not estimated.** `load_tools`' schema is
fixed-size - it does not embed the candidate list - at **208** tokens. The
frozen index costs **189** tokens for an 8-tool set (211 for 9). Total
overhead **397**.

### 3.1 The number, stated before any code

Deferred set = 6 privileged + `ledger_read` + `list_run_events`
(`read_output` stays core - see §5.1). Withheld: **2,607**. Overhead: **397**.

> **Net saving: 2,210 tokens per request** for a session that loads none of it
> - 40.7% of the whole tool block.

**Corrected after review — read with §8.** The 2,210 figure itself reproduced
exactly under independent measurement, and the estimator bias runs 46% in its
favour (§8, economics round). But three of the four claims originally made
around it were wrong, and the third is what killed the slice:

1. **Composition with scoping: stands, for the wrong stated reason.** All nine
   session tools are added *after* scoping runs, so scoping cannot reach them -
   but §2's `scopeAdmits`/`ScopeRoot` argument never fires, because privileged
   tools are not in `base` when `ScopedRegistry` runs. Registration order alone
   carries it. The "~1,543 narrow agent" figure is unauditable (~30 workspace
   subsets land in that band) and applies to narrow *root* bindings only:
   `ScopeSpawned` already drops the privileged block from spawned agents.
2. **~~Break-even is 7 of 8~~ - WRONG. It is 5 of 8.** Break-even is a token
   threshold, not a count: unloaded mass below 397 (15.2% of the set). The set
   is skewed (611/607/390 vs 115/123/167) and the realistic load order is
   expensive-first, so 6 of 8 is already -159. "You must load almost everything
   before losing" is struck. The comparison against `tools/05` was also
   mis-stated: its set was ~1,532 tokens, not 2,266.
3. **~~The population is right~~ - FALSE, and decisive.** The compiled default
   prompt mandates all eight deferred tools by name and never mentions
   `load_tools` (`internal/cli/prompt.go`, 15 of 52 lines, both variants). The
   expected number of admissions for the shipped population is **all eight, on
   turn 1** - not zero. See §8 C3.
4. **The unit was never stated.** 2,210 is honest as **context-window
   occupancy** and ~6x overstated as **billed cost**: the tool block sits at the
   head of the prefix and is the most reliably cached region of the request, so
   the cached steady state is ~221/request. This slice would have bought context
   headroom, not materially cheaper requests. §1's "stops paying" read as a
   billing claim and was misleading.

**Rollback criterion.** If the realistic deferred set falls below **~1,200 net
tokens of context-window occupancy** - the figure `tools/05` already beat with a
shipped mechanism - kill the slice rather than tune it.

> **This criterion fired.** Under claim 3's true population the set is fully
> admitted, putting the feature at **-397 tokens/request against HEAD**. The
> plan is rejected on its own stated terms.

## 4. Mechanism — SUPERSEDED, see §8

> Everything from here to §7.2 is the **rejected** design, retained as the
> record. §8 disposes of it finding by finding. Read §8 first.

### 4.1 Where the split moves to

Not to a new place in the sequence - to a new **argument**. `RegisterTool`
installs a handler that executes `r.Execute(name, …)` against the registry it
closed over (`internal/runtime/tools.go:56-67`), so advertisement and
execution read the same object. A tool cannot be "registered but unadvertised"
by filtering the outgoing spec list; withholding it means not putting it in the
advertised registry at all.

That is already how admission works. `widenAgentSurface` rebuilds the registry
*and* constructs a fresh dispatcher, which re-runs all four registration sites
from scratch. So the deferred session-tool set is a **parameter of the
rebuild**, and the existing stage → turn-boundary → precondition-checked
publish → clamp path carries it with no new machinery. This is the whole
design; if it grows a second publication path, that is a defect.

Proposed surface, in `internal/cli/dispatcher.go`:

```go
// SessionToolTier decides, per session tool, whether it is advertised to the
// root model or registered into authority only. A nil tier advertises
// everything, which is byte-identical to today.
type SessionToolTier struct {
    Deferred map[string]struct{} // names withheld from the advertised registry
}

func (t *SessionToolTier) advertises(name string) bool

// SessionDispatcherOpts gains one field:
//   SessionTools *SessionToolTier
```

and one helper replacing the direct `reg.Register` in the four sites:

```go
// registerTieredSessionTool registers tool on the dispatcher and on exactly
// one of the advertised or authority registries. Authority registration is
// unconditional: deferring changes what the root model is shown, never what
// the session is authorized to delegate.
func registerTieredSessionTool(d *runtime.Dispatcher, advertised, authority *tools.Registry,
    tier *SessionToolTier, tool tools.Tool, privileged bool) error
```

Call sites: `registerDelegationTools` (dispatcher.go:391,394),
`registerOrchestrationTools` (orchestrate.go:418), `registerLedgerTools`
(ledger_tools.go:401), `registerLoadToolsTool` (dispatcher.go:255).

### 4.2 The authority trap

`adoptSessionTools` (dispatcher.go:233) copies advertised → authority, and it
is the *only* way ledger tools reach the authority registry today. A deferred
ledger tool therefore silently disappears from every nested principal's scope
- a real availability regression, not a theoretical one, because
`registerLedgerTools` documents these three as deliberately reachable by
sub-agents. `registerTieredSessionTool` must write to authority directly and
unconditionally; `adoptSessionTools` then becomes a no-op for anything it
handled. **Named test: `TestDeferredLedgerToolStillReachesASpawnedAgent`.**

### 4.3 Placement of an admitted tool, computed

`tools/05` §14.3 finding 3 says the admitted tail lands before the privileged
block, invalidating 79% of the tool block. Under this design the rebuild
reconstructs the array in registration order, so an admitted privileged tool
lands in its *canonical* slot - after the workspace core block. Concretely,
admitting `delegate` re-materializes positions 11-19 plus `load_tools`.

That is better than `tools/05` and still not free. The cache-optimal placement
is to append every admitted session tool **after** `load_tools`, at the
absolute end of the array, so an admission invalidates nothing that precedes
it. Nothing depends on canonical order except goldens.

Irreducible residue, stated honestly: implicit prefix caching serializes tools
before the system prompt and conversation (§14.3), so **any** widening
re-processes the prompt and transcript once regardless of placement. The
defensible claim is not "admission is free" but "admission is rare here, and
when it happens it costs one prefix re-process instead of one plus 79% of the
tool block." Step 0 must not let this be rounded up into a saving.

**Named test: `TestAdmittedSessionToolAppendsAfterLoadTools`** - a golden over
the serialized tool block asserting the chosen order, in the shape of
`tools/05`'s step-2 gate.

## 5. The four questions the goal requires answered

### 5.1 Is class B deferrable at all?

**Partly. `ledger_read` and `list_run_events` yes; `read_output` no in v1.**

`read_output` resolves a ref minted by the agent loop's own truncation notice.
The loop mints that notice without the model asking, so deferring it creates a
window where the host hands the model `ref=…, call read_output` while
`read_output` is not advertised - the notice text is then false, and recovery
costs a `load_tools` call plus a turn. `tools/05`'s D6 accepted a wasted turn
for a tool the *model* chose to want; it is a different bargain when the host
creates the need unprompted.

Cost of the conservative call: 290 tokens, 11% of the deferred set. Cheap.

The strictly better answer is host-side auto-admission: the notice mint stages
`read_output` through the existing `StageToolAdmission` path, so the tool
arrives without the model spending a call. It reuses the machinery exactly and
costs nothing when nothing truncates. It is **deliberately not in v1** because
it adds a second trigger for staging, and every recurring defect class in
`tools/05` §10-§14 was a staging/publication interaction. Recorded as the first
follow-up.

`ledger_read` and `list_run_events` carry no such coupling: they are
model-initiated, and they read the history of runs that only exist if the
session already delegated - by which point the privileged block is admitted
anyway.

### 5.2 What breaks if a rebuild runs at each point in the new ordering?

The ordering does not change. `state.ToolBase` stays a pre-registration
snapshot; `planToolTiers` keeps operating on workspace tools only; the session
tier is a separate, statically-known set resolved from config at bind time.
Two tiers, two owners, no reordering - this is the main reason to prefer the
parameter over moving the split.

What must be re-verified against the four rebuild triggers:

| Trigger | Question | Named test |
|---|---|---|
| `/agent` switch | Does the new binding recompute the session tier, and does an in-flight session-tool stage die on the generation bump like a workspace stage does? | `TestAgentSwitchDropsAStagedSessionToolAdmission` |
| `/model` switch | Does the surface rebuild preserve the admitted session-tool set, as it preserves the workspace one? | `TestModelSwitchPreservesAdmittedSessionTools` |
| Admission publish | Does publishing a session-tool admission close exactly one dispatcher, with no coordinator or run handle still holding it? | `TestSessionToolAdmissionDoesNotCloseAHeldDispatcher` |
| Resume | Does a persisted admitted set that names a session tool survive, and does a digest mismatch drop it fail-closed? | `TestResumeReplaysAnAdmittedSessionTool` |

The resume row is the sharp one. `AdmissionDigest` currently fingerprints the
workspace tier split only. A persisted set naming `delegate` must not survive a
config change that stopped deferring `delegate`, so **the digest must cover the
session tier too** - which is exactly the mutation that survived round 5
(§14.1). Do not re-introduce it.

`initCoordinator` and `storeOrchestrationHandle` key on the dispatcher pointer
(`orchestration_state.go:127-159`, `:288`). An admission replaces the
dispatcher, so admitting `spawn_agent` mid-session must not orphan handles a
prior dispatcher registered. `tools/05` already survives this for workspace
tools; the difference is that here the admitted tool *is* the orchestration
surface. **Named test:
`TestAdmittingSpawnAgentLeavesPriorRunHandlesControllable`.**

### 5.3 How does a deferred privileged tool stay unreachable from a nested agent?

Untouched, and that is the point. Nested principals scope from the **authority**
registry (`registerMultiStepHandler(d, authority, …)`, dispatcher.go:195), and
`scopeAdmits` under `ScopeSpawned` returns `false` for any `PrivilegedTool`
before consulting anything else (scope.go:163). Deferring changes only which
registry a tool is written to; §4.2 writes privileged tools to authority
exactly as today, where `ScopeSpawned` drops them exactly as today.

Three guards must be shown still to bite, because "unchanged" is a claim, not
evidence:

- `registerSessionTool`'s startup rejection of an unmarked session tool must
  survive the refactor into `registerTieredSessionTool`. A deferred tool that
  quietly lost its marker check is the failure that matters.
- `TestRestrictedRegistryDropsPrivilegedAndDelegationTools` and
  `TestRestrictedRegistryNeverReExpandsPastItsInput`
  (`internal/subagents/spawned_scope_guard_test.go`) must pass unchanged.
- `ScopedRegistryWithTail` already refuses a denylisted name in both modes
  (round-5 fix, scope.go:112-117). The six deferred privileged names **are**
  `CompiledMandatoryDenylist` verbatim. So if a session-tool admission is ever
  routed through the workspace tail rather than through re-registration, every
  one of them is silently dropped. **Named test:
  `TestSessionToolAdmissionDoesNotRouteThroughTheWorkspaceTail`** - this is the
  most likely way to build the feature and have it appear to do nothing.

### 5.4 Cache cost

Answered in §4.3. Summary: one prefix re-process per admission event,
irreducible; the tool-block share is eliminated by end-placement; expected
admissions for the target population is zero.

## 6. Config

```toml
[tools]
defer_session_tools = false   # v1 default: OFF, feature inert
```

Inert by default, as `tools/05` shipped. `[tools] core` is untouched - its
step 8 stays untaken. A per-agent override is **out of scope for v1**: it is
speculative generality until an operator asks, and `tools/05` §13's
simplification backlog is already carrying debt of that shape.

## 7. Test strategy beyond the named cases

- **Inertness golden:** `defer_session_tools = false` produces a byte-identical
  tool block to HEAD. Non-negotiable; it is what makes the rollback cheap.
- **INV-CE-05-A:** advertised implies invocable, asserted over the deferred
  surface including mid-admission.
- **Mutation testing is a gate, not a bonus.** Round 5 found a 29% escape rate
  at 100% diff-coverage (§14.1), and two tests written to pin round-3 fixes
  passed for the wrong reason. Definition of done for this slice includes a
  mutation pass over the changed lines with every survivor killed or argued
  equivalent in writing.
- **Diff coverage** via `scripts/diff_coverage.py`; structure gate via
  `check_go_structure.py --strict` (files ≤500 LOC soft, funcs ≤80).

## 7.1 Files and dependency waves

**Modify** (no new production files - the point of §4.1 is that this is a
parameter, not a subsystem):

| File | Change |
|---|---|
| `internal/cli/dispatcher.go` | `SessionToolTier`, `registerTieredSessionTool`, `SessionDispatcherOpts.SessionTools`; `registerSessionTool` delegates to the new helper keeping its marker check |
| `internal/cli/orchestrate.go` | site `:418` routes through the helper |
| `internal/cli/ledger_tools.go` | site `:401` routes through the helper; authority write is unconditional (§4.2) |
| `internal/cli/chat_repl.go` | resolve the session tier at attach; pass it into `SessionDispatcherOpts` |
| `internal/cli/agent_switch.go` | carry the tier through `surfaceBuildRequest` so every rebuild reproduces it |
| `internal/tools/tier.go` | `AdmissionDigest` covers the session tier (§5.2) |
| `internal/config/*` | `[tools] defer_session_tools` |
| `.mivia/mivia.toml.example`, `docs/product/agent.md` | owned-doc updates |

**Waves:**

- **Wave 1** - `SessionToolTier` + `registerTieredSessionTool` + config knob.
  Foundation; no call site moved yet, so the tree stays green.
- **Wave 2** - route the four registration sites; the authority write of §4.2.
  Gated on `TestDeferredLedgerToolStillReachesASpawnedAgent`.
- **Wave 3** - wire attach and every rebuild path (`/agent`, `/model`,
  admission, resume); extend `AdmissionDigest`.
- **Wave 4** - end-placement of admitted session tools (§4.3) + goldens.
  Separable: waves 1-3 are correct without it, it is purely a cache
  optimisation, and isolating it keeps the ordering change revertible on its
  own.

Each wave gates on `go build ./... && go test -race ./internal/cli/...
./internal/tools/... ./internal/subagents/...`.

## 7.2 Plan scorecard

| Criterion | Score | Basis |
|---|---|---|
| Compiles | PASS | No new package, no new interface; one struct and one helper in an existing file |
| No import cycles | PASS | `SessionToolTier` lives in `internal/cli` beside its only consumers; nothing new is imported |
| No breaking API change | PASS | New `SessionDispatcherOpts` field; nil means today's behaviour |
| Testable in isolation | PASS | The tier is a pure predicate; the registration helper takes explicit registries |
| Backward-compatible config | PASS | `defer_session_tools` defaults false; inertness golden in §7 |
| Every function has a test | PASS | Named cases in §4.2, §4.3, §5.2, §5.3 cover both new functions and each modified site |
| **Measured before built** | **PASS** | §3.1, re-derived at HEAD rather than inherited from §14.3 |

No FAIL. Per ADLC's rollback table a single FAIL rejects the plan; the
rollback criterion in §3.1 is the separate, evidence-based kill switch.

## 8. Step 0 disposition

### Round 1, structural review (verdict: REJECT, resubmit with §4 rewritten)

**S1 (BLOCKER) - the plan priced a deferral and specified a deletion.
CONFIRMED, independently verified.** `CloneForGenerationExcluding` skips every
`PrivilegedTool` (`internal/tools/tools.go:120-122`) and the three ledger names
by argument, and it is the base for **all four** rebuild sites
(`agent_switch.go:317,332`, `model_binding.go:93,164`). So a session tool can
never enter `base.List()` in `planToolTiers`, never becomes a `TierCandidate`,
never reaches `DeferredIndex` or `loadToolsTool.candidates`, and
`resolveRequested` refuses its name. Worse, `registerLoadToolsTool` early-returns
on an empty candidate set (`dispatcher.go:248-251`), so with the shipped default
(`[tools] core` unset) `load_tools` is not registered at all. As written, waves
1-3 ship a session that has lost delegation and run-history with no index entry,
no discovery tool and no recovery - while §3.1 prices the change as though all
three existed.

**Accepted in full. §4 is rewritten below as "one tier, two candidate
sources".** The session-deferred names must enter `toolTierPlan.Candidates`, the
frozen index, `loadToolsTool.candidates` and the admitted-set arithmetic; the
withholding predicate becomes `deferred \ admitted`, recomputed per rebuild, not
a static config set. This also dissolves S4, S5, S6 and S8.

**S2 (MAJOR) - `model_binding.go` omitted from §7.1. CONFIRMED.** There are
three production `SessionDispatcherOpts` sites (`chat_repl.go:134`,
`agent_switch.go:384`, `model_binding.go:98`) and three `surfaceBuildRequest`
sites (`agent_switch.go:319,333`, `model_binding.go:165`). §7.1 named only
`agent_switch.go`. This is plan `tools/05` §11 round-2 #1 reproduced verbatim in
the successor plan's own file table. Accepted: `model_binding.go` added, plus a
Wave-3 gate that enumerates all construction sites mechanically rather than
trusting a hand-maintained list. `unscopedModelSurface`'s justifying comment
(`model_binding.go:86-89`) is falsified by a config-driven tier and must be
corrected.

**S3 (MAJOR) - the "no new files" cost claim fails the plan's own `--strict`
gate. CONFIRMED by measurement.** `dispatcher.go` is 467 lines against a soft
500; `attachSessionDispatcher` is 77 lines and `buildSurfaceFromBase` 78,
against a soft 80 - the two functions §7.1 puts the new plumbing in. Accepted:
a new `internal/cli/session_tool_tier.go`, and the attach/rebuild plumbing
extracted into helpers rather than inlined. The "parameter, not a subsystem"
claim survives as a description of the *mechanism*; it was wrong as a claim
about *file count*.

**S4 (MAJOR) - two tier concepts is a duplicated boundary. ACCEPTED,** and the
proof is a contradiction in this plan's own scorecard: §7.1 required
`internal/tools`' `AdmissionDigest` to cover the session tier while §7.2 scored
"no import cycles: PASS - `SessionToolTier` lives in `internal/cli`".
`internal/tools` cannot import `internal/cli`. Subsumed by the S1 rewrite: one
tier, owned by `internal/tools` + `tool_tiers.go`, two sources of candidates.

**S5 (MAJOR) - telemetry goes blind. CONFIRMED.** `measureSchemaMass` derives
`Deferred`/`HeldTokens` solely from `plan.Candidates`
(`tool_schema_mass.go:33-60`), so `/tools` would report `tools_deferred=0` while
`tools_advertised` silently dropped by eight - no evidence for the 2,607 tokens
this plan is selling. `tool_schema_mass.go` added to §7.1; free under the S1
rewrite.

**S6 (MINOR) - `SessionToolTier` is speculative generality. ACCEPTED and cut.**
The plan applied that test to per-agent config overrides (§6) and not to its own
type.

**S7 (MINOR) - withheld is not unreachable. CONFIRMED and important.**
`Dispatcher.register` sets `policy.Allow[k][name] = true` unconditionally
(`internal/runtime/dispatcher.go:264`), and `Invoke` checks only handler
presence and that flag (`:302-306`). Since §4.2 requires the handler to be
registered regardless, a root model that names `delegate` without seeing its
schema still executes it. This is **not** an escalation - root is authorized to
delegate (`scope.go:138-141`) - but it means deferral here is an advertising
convention, not a boundary, and the plan must say so rather than let §7's
INV-CE-05-A assertion imply a guarantee it does not make. Accepted: stated in
§5.3, plus `TestWithheldSessionToolIsStillInvocableByName` pinning the behaviour
deliberately.

**S8 (MINOR) - digest churn. ACCEPTED,** subsumed by S1: once session names are
part of `Tiers.Deferred` proper, the digest change follows from the data instead
of being a special case that invalidates unrelated workspace admissions on a
global bool flip.

**Rejected findings: none.** Every finding was verified against the code before
disposition; the three I re-derived personally (S1's clone behaviour, S7's
unconditional allow, S3's line counts) matched exactly.

**What survived review:** the goal, and §2's argument that agent scoping
structurally cannot reach this block (`scope.go:138-141` vs `chat_repl.go:252`
preceding `:134`). Also verified sound by the reviewer and retained: §4.1's
claim that the rebuild genuinely re-runs all four registration sites, and §4.2's
`adoptSessionTools` finding, which is if anything understated.

### Round 1, correctness review (verdict: REJECT)

**C1 (BLOCKER) - duplicate of S1, reached independently.** Same evidence, same
amendment. Two reviewers converging on this from different directions is
itself the finding: §5.2's "two tiers, two owners, no reordering" is
incompatible with the code that consumes `plan.Candidates`, which is
simultaneously the registration gate, the frozen index, and the authority for
what `load_tools` may stage (`load_tools_tool.go:131-152`).

**C2 (BLOCKER) - the deferred set contains the controls for the state that
blocks their own admission. CONFIRMED, and new - not a `tools/05` hazard
reopened.** `PublishPendingAdmission` defers on `CheckSwitchAllowed()` error
(`admission.go:385-390`); the installed guard is
`orchestrationSwitchGuard(sess.SessionID)` (`chat_repl.go:104`), which errors
whenever any non-`Done` handle exists for the session
(`orchestration_state.go:161-188`). So: admit `spawn_agent`, start a run, then
stage `join_run`/`cancel_run`/`inspect_agents` - and every boundary defers for
the run's entire lifetime, going silent after two notes
(`admission.go:19,410-418`). The tools arrive exactly when they are useless.
`cancel_run` is the runaway-run kill switch, so this is a safety regression,
not an ergonomics one. `tools/05` never hit this because workspace tools have
no coupling to the guard's precondition; `tools/07` defers precisely the tools
whose need is created by the state that blocks them.

**C3 (BLOCKER, DECISIVE) - the compiled default prompt mandates every deferred
tool by name and never mentions `load_tools`. CONFIRMED by direct
inspection.** `internal/cli/prompt.go` names `dispatch_tasks`, `spawn_agent`,
`inspect_agents`, `cancel_run`, `join_run`, `delegate` and `ledger_read` as
required process across 15 of its 52 lines, in **both** prompt variants
(`:37,41,45,47,52-64,70` and the `:99-132` duplicate). `load_tools` appears
nowhere in the file. The Failure-recovery block instructs retry and "NEVER fall
back to sequential work".

This is decisive on the plan's own terms. §3.1 asserted "a single-purpose agent
session never delegates … the expected number of admissions is zero." For the
shipped default prompt the expected number is **all eight, on turn 1**. §3.1
also states that loading the entire set costs **+397 tokens/request forever**.
So for the shipped population the feature is *worse than HEAD*, and §3.1's own
rollback criterion fires.

The schema-mass measurement was sound - three reviewers reproduced it exactly.
The **population** assumption behind it was never measured, and it is falsified
by a file in the same package as the code being changed. §7.2's "Measured
before built: PASS" was therefore scored against the wrong claim.

**C4-C8 (MAJOR), all confirmed and accepted:** publishing *any* admission
destroys run handles registered under the replaced dispatcher, because
`storeOrchestrationHandle` hangs deletion off `dispatcher.OnClose`
(`orchestration_state.go:136-141`) and `orchestrationHandleAccessible` requires
pointer identity (`:190-193`) - and §5.2's claim that "`tools/05` already
survives this for workspace tools" is unsupported; the named test
`TestSessionToolAdmissionDoesNotCloseAHeldDispatcher` would have **passed while
the defect was present**, because it asserts dispatcher count rather than handle
survival (C4). §4.2's "authority registration is unconditional" collapses to
"advertise it" wherever `AuthorityRegistry` is nil, which includes the unlisted
fourth site `unscopedModelSurface` (C5, converging with S2). Deferring
`ledger_read` reproduces exactly the dangling-reference class §5.1 used to
justify keeping `read_output` core, because `readHint` mints "use ledger_read
with this ref" into every delegate/dispatch/lifecycle result
(`synopsis.go:136-142`) - host-minted, not model-chosen, which is §5.1's own
stated unacceptable bargain applied to the wrong tool; that leaves only
`list_run_events` (294 tok) genuinely deferrable in class B (C6).
`registerTieredSessionTool(…, privileged bool)` converts a type assertion into a
caller-supplied parameter, and its duplicate-name check would run against a
registry that by construction never contains the deferred tool (C7).
`AdmissionDigest` needs the session tier *and* an explicit session-tier clamp,
because `clampToAuthorized` cannot see session-tool names at all (C8).

**C9-C11 (MINOR), accepted.** §5.3's stated mechanism for the tail-routing
hazard is **wrong**: the six privileged names never reach
`ScopedRegistryWithTail`'s denylist check because `clampToAuthorized` removes
them first, so the test I named would have passed for the wrong reason - the
round-5 failure mode, reproduced in the successor plan's test design (C9).
`RemainderSpoolFromRegistry` reads the advertised registry, so §5.1's declared
follow-up would break every outstanding ref (C10). Wave 4's end-placement
contradicts two documented ordering contracts that must be updated in the same
wave (C11).

---

## 8.1 Verdict: REJECTED

Two of three reviewers returned REJECT. The economics reviewer returned
SOUND-WITH-CORRECTIONS but was not given the prompt file, and C3 falsifies the
one assumption it had already flagged as load-bearing.

**The slice is rejected on C3.** Not on a mechanism defect - C1, C2 and C4-C11
are all amendable. It is rejected because the feature's central premise, that
the target session admits nothing, is contradicted by the compiled default
prompt, and under the true population the feature is measurably worse than
doing nothing.

Corrected economics, for the record: the saving is **context-window occupancy,
not billed cost** (~221/request in cached steady state); break-even is a token
threshold of 397, reached at **5 of 8** tools under realistic expensive-first
loading, not 7 of 8; and the feature is net-negative for any session that
admits, requiring a delegation rate below ~0.23-0.57.

## 8.2 What a successor slice would have to do

The *goal* survived review intact. `tools/05` §14.3's conclusion still holds:
the privileged block is where the schema mass is, and agent scoping structurally
cannot reach it. What died is this mechanism, not the target.

A successor must do all of these, and they are not independent:

1. **Tier the prompt on the same switch as the schemas.** Measured: the whole
   default prompt is 3,973 bytes / ~993 tokens, of which the 15 lines naming a
   deferred tool are ~495 tokens. The extra 495 is not the point - making the
   "zero admissions" premise *true instead of false* is the point. Without this
   there is no viable slice.
2. **One tier, two candidate sources** (S1/C1) - session names merged into
   `toolTierPlan.Candidates` before the index, the registration gate and
   `load_tools`' staging authority read it.
3. **Atomic admission unit** for the six privileged names plus `ledger_read`
   (C2/C6), so admission can only happen before the first run exists.
4. **Fix the dispatcher-pointer handle keying first, as its own slice** (C4).
   It is a pre-existing `tools/05`-era defect that this plan declared a non-goal
   (§9) and that a successor cannot build on top of.

Item 4 alone makes this at minimum two slices, and item 1 puts prompt
composition in scope - which is a different blast radius from the "parameter on
an existing rebuild" this plan claimed. Re-enter Step 0 from scratch; do not
amend this document into the successor.

### Round 1, economics review (verdict: SOUND-WITH-CORRECTIONS)

Every measured figure in §3 and §3.1 reproduced **exactly** against an
independent harness: 2,532 / 2,013 / 884 / 5,429, all per-tool costs, the 2,607
deferred set, `load_tools` 208, index 189 and 211, and the 2,210 net. The
attacks that failed are recorded so they are not re-run:

- **The estimator attack failed, 46% in the plan's favour.** `estimateTokens` is
  `len(s)/4` (`context.go:12-20`), uniform over JSON and prose. Measured
  composition shows the deferred schemas are 14.9% punctuation at 2.78
  chars/token under a BPE proxy, against 4.5% and 3.77 for the index prose - so
  `len/4` *understates* dense JSON by ~44% and prose by ~7%. Corrected, the net
  is **3,226**, not 2,210, and the index-vs-schema ratio is 18.9x, not 13.8x.
  Tokenizer-free bound: net falls below the 1,200 rollback floor only if JSON
  tokenizes at 6.1 chars/token, which is not reachable.
- **Config sensitivity failed.** Across seven registry configurations
  (Tavily on/off, allowlist unset, tools disabled) the absolute saving is
  **exactly 2,210 in every one**, because the whole deferred set is registered
  unconditionally by the dispatcher while `NewDefaultRegistry` varies only the
  workspace block. Only the percentage moves: 39.1% (Tavily) to 56.3%.

**E1 (BLOCKER) - the headline has no stated unit. ACCEPTED.** §4.3 invokes
implicit prefix caching to price admission as a cost, while §1 and §3.1 ignore
that the same caching discounts the saving. Tools serialize at the head of the
prefix, making the tool block the most reliably cached region of the request, so
2,210/request is honest as **context-window occupancy** and roughly 6x
overstated as **billed cost** (1.25x on request 1, ~221/request in cached steady
state; 6,962 effective tokens over 20 requests, not 44,200).

Resolution: **the claim is context-window occupancy, and the plan says so.**
That is the unit plan `51-harness-context-economics` is written in and the unit
`tools/05` §14.3's 1,408-vs-1,200 comparison used, so the comparison stays
apples-to-apples and the ~1,200 rollback floor stands. The billed-cost figure is
published beside it rather than buried: **this slice buys context headroom, not
materially cheaper requests.** Anyone reading §1's "stops paying" as a billing
claim was being misled by my wording.

**E2 (MAJOR) - admission is net-negative immediately. ACCEPTED.** An admitting
session pays `1.15·P` for the re-billed prefix plus a whole extra `load_tools`
round-trip the plan never counted, against 397/request of overhead forever.
There is **no request count at which an admitting session recovers**. The real
precondition is therefore not "expected admissions is zero" but a delegation
rate below roughly **0.23-0.57**, tightening as transcripts grow. §5.4 compressed
§4.3's honesty back out and must not; the constraint goes in §1 as a stated
precondition of the feature, not a footnote.

**E3 (MAJOR) - "break-even is 7 of 8" is wrong. ACCEPTED.** Break-even is a
token threshold, not a count: it is reached when unloaded mass falls below 397
(15.2% of the set). Because the set is skewed (611/607/390 vs 115/123/167) and
the realistic load order is expensive-first, that is **5 of 8**, with 6 of 8
already at -159. "You must load almost everything before losing" is false and is
struck.

**E4-E10 (MINOR), all accepted:** §3's "verified additive" is circular -
`EstimateToolSchemaCost` is a per-tool sum by construction, so the check cannot
fail; it is struck rather than reworded. The ~1,543 narrow-agent figure is
unauditable (~30 workspace subsets land in that band) and applies only to narrow
*root* bindings, since `ScopeSpawned` already drops the privileged block from
spawned agents. §2's "two independent facts" overstates: the
`scopeAdmits`/`ScopeRoot` fact never fires, because privileged tools are not in
`base` when `ScopedRegistry` runs - registration order alone carries the whole
argument, and this converges with S1 from the structural round. §3.1's "2,266
tokens heavy" for `tools/05`'s set is wrong (~1,532), which makes the comparison
*stronger* than claimed. The index is 5 tokens conservative. The throwaway
harness has a dead assignment and hand-builds its spec.

**Not a correction, recorded for later:** if `tools/05` step 8 is ever taken,
`load_tools`' 208 is shared and this slice's net rises to 2,418.

## 9. Non-goals

- Revisiting `tools/05`'s shipped behaviour.
- Its simplification backlog SR-1..SR-10 (§13 records why they were deferred).
- Changing the default `[tools] core`; step 8 stays untaken.
- Per-agent session-tier overrides (§6).
- `read_output` auto-admission (§5.1) - first follow-up, not v1.
