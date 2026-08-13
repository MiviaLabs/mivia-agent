# Spec: Auto-split of oversized delivered diffs into a commit stack of follow-up PRs

Status: **Draft for review** (root-authored design, revised after review)
Audience: engineers implementing the feature; reviewers of the design
Related: `docs/architecture/workflows.md`, `docs/product/workflows-guide.md`

Revision note: this replaces an earlier draft that hung the feature off a new
LLM-authored `.mivia/split/deferred-v1.json` manifest. Review (code
fact-check, adversarial architecture challenge, external prior-art research,
simplification audit — see §9) found that design untrustworthy (schema
validation checks shape, not truth; the manifest can drift from the real
diff by the time a days-later human review lets the parent PR merge) and
over-built (10+ touched files, a second chunk-plan producer, a new schema/
gate/ledger field for a signal already derivable from existing state). This
revision replaces the manifest with **git commits as the hand-off** — the
same approach used by Graphite, ghstack, git-spice, and Phabricator's
stacked-diff tooling. A commit can't hallucinate a path or a line count; it
either applies or it doesn't.

Second revision note: a follow-on driver audit plus industry research (§12)
found that the correction mechanism above (§5.2–§5.3) is the *fallback*, not
the primary path — the primary path is preventing oversized chunks in the
first place via a DAG of small, disjoint, concurrently-executed chunks,
which matches how Devin, Claude Code worktrees, Conductor, and Graphite
Agent structure this problem in practice. This repo already has DAG
modeling, worktree isolation, and per-chunk PR delivery; it is missing
concurrent execution of independent chunks and has a chunk-count ceiling
(12) that cannot represent large plans. §12 makes parallel DAG execution a
v1 prerequisite, not a future enhancement, and removes the fixed low ceiling
in favor of incremental, wave-scoped decomposition.

## 1. Problem

Today, when a chunk's delivered diff exceeds the stacking hard limit
(`stacking.hard_lines`), the system recovers reliably but **shrinks the PR in
place**:

1. Delivery measures the staged worktree diff vs the base commit
   (`internal/workflows/delivery/stacking.go`, `MeasureChunkDiffSize`). Over
   `hard_lines` → a `DiffSizeError` (a repairable class, never a refusal or
   transport fault; also defined in `stacking.go`, not `deliver_diff.go` —
   corrected from the prior draft).
2. `routeDeliveryRepair` (`internal/workflows/localengine/engine_deliver.go`)
   maps the error to a repair step via `delivery.RepairTarget(err, policy)`:
   `on_diff_size_failure` → fallback `on_failure`. The run is reopened at that
   step (`delivery.ReopenForRepair`, bounded by `MaxRepairs`, clamped default
   5).
3. The repair agent **reverts the least-essential part of the change** in the
   worktree and records what it deferred in its `summary`
   (`.mivia/workflows/templates/repair.md`, `bugfix-repair.md`).
4. The run re-runs panel → review → gates → success → delivery. The now-smaller
   diff delivers as **one PR**.

The deferred remainder is **recorded but never delivered automatically**. There
is no mechanism that turns an already-oversized diff into PR #2, #3, … —
verified: nothing re-runs `decompose` on a size rejection
(`internal/cli/stack_*.go`, `internal/workflows/delivery/stacking.go`).

## 2. Goal and non-goals

**Goal.** When a chunk's estimated or delivered diff is too big, the excess
scope is delivered as **additional small PRs, stacked on the first, without
operator intervention** — bounded, ordered, rebase-aware, and never silently
lost.

**Non-goals.**

- Changing shrink-first repair semantics for the *first* PR in a stack:
  producing a review-sized PR #1 is still the primary action.
- Re-decomposing an in-flight worktree mid-pipeline beyond what §5.1 already
  describes (splitting a chunk into a stack at decompose time is in-scope;
  arbitrary re-planning is not).
- Splitting PR-metadata repairs (title/summary defects stay on the same run).
- Allowing unbounded stack length (bounded in §5.4).
- Building a general-purpose rebase-conflict resolver. Conflicting rebases
  are a known gap (§8) scoped as follow-on work, not silently assumed away.

## 3. Current behavior (verified ground truth)

| Component | Location | Role |
|---|---|---|
| Decompose loop | `internal/workflows/compiler/synthesis.go` | Engine-synthesized `decompose` ↔ `chunk_plan_validate` (loop `decompose_repair`, max 3) when the workflow declares stacking; outputs `chunk_plan` (schema `.mivia/workflows/schemas/chunk-plan-v1.json`); reserved inputs `stack_mode`, `chunk`, `pr_base`, `stack_part`, `chunk_plan` |
| Delivery policy | `internal/workflows/delivery/policy.go` | `Policy{OnFailure, OnPRMetadataFailure, OnDiffSizeFailure, MaxRepairs, StackingHardLines}`; empty failure-step names fall back to `OnFailure`; `MaxRepairs` clamped to `DefaultMaxDeliveryRepairs` (5) |
| Size enforcement | `internal/workflows/delivery/stacking.go` | `MeasureChunkDiffSize`, `DiffSizeError` class, `checkChunkDiffSize` |
| Repair routing | `internal/workflows/localengine/engine_deliver.go` | `routeDeliveryRepair` → `RepairTarget` → `ReopenForRepair(ctx, repo, runID, step, MaxRepairs, err, ...)` |
| Delivery record content refs | `internal/workflows/ledger/types.go` (`DeliveryRecord`) | Already has `ErrorRef`/`DiffRef` — the precedent this design reuses for any new content ref, rather than inventing a parallel pattern |
| Stack driver | `internal/cli/stack_admit.go`, `stack_drive.go`, `stack_merge.go`, `stack_reconcile.go`; `delivery/stacking.go`; `controller/stacking.go` | Admits one chunk run per chunk, topological order, merge policy, reconcile |
| Repair templates | `.mivia/workflows/templates/repair.md`, `bugfix-repair.md` | Shrink + record deferred scope in `summary` today; this spec changes what "shrink" produces (§5.2) but keeps the existing structured-output/schema mechanism the templates already use |

## 4. Design overview

Two entry points, no new file-based contract:

**A. Prevention (the common case).** Decompose already estimates chunk size
(`est_diff_lines` in `chunk_plan`). When an estimate exceeds `hard_lines`,
decompose splits that chunk into an **ordered sequence of smaller chunks**
before any code is written — same `chunk_plan` schema, same
`chunk_plan_validate` gate, same stacking driver that already handles
multi-chunk runs. Nothing new to build here beyond a split rule inside
decompose's existing planning step.

**B. Correction (the estimate was wrong).** If a chunk still delivers
oversized despite passing decompose, the repair step — instead of reverting
the excess and only writing prose to `summary` — restructures the worktree
into an **ordered stack of commits**: commit 1 is the review-sized slice,
commits 2..N are the rest, each independently buildable. Commit 1 delivers as
PR #1 today, unchanged. The stacking driver then treats the **remaining
commits already sitting in the branch** as the follow-up chunk sequence — no
manifest file, no new schema, no new ledger field. Git is the hand-off; a
commit either applies cleanly or it doesn't, so there is nothing to validate
for truthfulness beyond "does it build."

```
chunk estimated oversized at decompose time
  → decompose splits into an ordered chunk_plan sequence (existing schema/gate)
  → stacking driver admits chunk 1..N as it does today

chunk delivers oversized despite a good estimate
  → reopen at repair step (unchanged, MaxRepairs-bounded)
  → repair agent shrinks AND restructures remaining scope into commits on the
    same branch (no manifest file)
  → commit 1 re-delivers as PR #1
  → run settles succeeded; ledger records "stack_remaining_commits: N"
    (a count, derived from `git log`, not an LLM-authored claim)
  → stack driver reconcile sees remaining commits on the branch
  → admits chunk N+1 from commit N+1, pr_base = PR #1's merge commit
  → repeats until the branch has no unshipped commits, bounded by
    split_max_chunks
  → if PR #1 changes under review, `git rebase --onto` the remaining stack
    before the next chunk is admitted (§5.3)
```

## 5. Detailed design

### 5.1 Prevention: decompose-time chunk splitting

- `internal/workflows/compiler/synthesis.go`'s decompose step gains a rule:
  when a proposed chunk's `est_diff_lines` exceeds `stacking.hard_lines`,
  split it into 2+ chunks with `depends_on` ordering, same as any other
  multi-chunk plan. This is the existing `chunk_plan` shape — no schema
  change.
- `chunk_plan_validate` already rejects malformed plans; a plan whose
  individual chunks still exceed `hard_lines` after the split rule is a
  validation failure like any other, retried within the existing
  `decompose_repair` loop (max 3).

### 5.2 Correction: repair produces a commit stack, not a manifest

- `.mivia/workflows/templates/repair.md`, `bugfix-repair.md` are updated:
  when shrinking a diff-size-triggered repair, the agent commits the
  review-sized slice first, then commits the remaining deferred scope as one
  or more additional commits on the same branch, each a coherent,
  independently buildable unit. The existing `summary` output still
  describes what was deferred and why, for human readers — but the stacking
  driver does **not** parse `summary` for follow-up scope. It reads commits.
- No new file (`.mivia/split/deferred-v1.json`), no new schema
  (`deferred-v1.json`), no new synthesis-compiled validation gate. The
  build/test gates a normal chunk run already runs against each admitted
  commit are the validation — if a trailing commit doesn't build standalone,
  it fails those gates like any other chunk, not a bespoke manifest check.

### 5.3 Engine and driver: read the branch, not a record

- After a size-repair re-delivery succeeds, the engine records
  `stack_remaining_commits` (an integer, from `git rev-list --count` between
  the delivered commit and the branch tip) on the delivery record — reusing
  the existing content-ref pattern (`ErrorRef`/`DiffRef` on `DeliveryRecord`)
  rather than adding a free-text "note." Zero means no split; nothing
  downstream changes from today's behavior.
- `stack_reconcile` (already the driver's convergence pass) admits chunk N+1
  once PR #1 (chunk N) merges, with `pr_base` = the parent's merge commit,
  `chunk` = the next unshipped commit. This is the existing chunk-run
  admission protocol (`stack_admit.go`) — no new admission path.
- **Rebase before admission.** If the parent PR's branch was force-pushed or
  amended during review (commit hash for chunk N's tip changed), the driver
  rebases the remaining stack (`git rebase --onto <new-parent-tip>
  <old-parent-tip> <stack-branch>`) before deriving chunk N+1's diff. A clean
  rebase proceeds automatically; a conflicting rebase surfaces as a per-chunk
  error for operator resolution (§8 — not solved automatically in v1).
- **Merge:** `stack_merge` applies unchanged to follow-up PRs, squash-merging
  each into the base, same as today.

### 5.4 Policy knobs (new)

`[stacking]` table (compiled in `compiler/delivery_validate.go` with the same
validation rigor as today's knobs):

| Knob | Type | Default | Meaning |
|---|---|---|---|
| `split_deferred` | bool | `false` | Enable follow-up PR creation from a repair-produced commit stack (opt-in; shipped workflows can opt in explicitly) |
| `split_max_chunks` | int | `4` | Max follow-up PRs admitted per oversized chunk (caps stack length) |
| `split_min_lines` | int | `10` | A trailing commit at/under this size is folded into the previous PR's stack entry instead of becoming its own PR |
| `decompose_split_hard_lines` | bool | `true` | Enable §5.1 prevention (decompose splits oversized-estimate chunks up front). Independent of `split_deferred` — prevention can run even if correction-time splitting is off. |

Bounds:

- `split_max_chunks` caps stack length. If a repair produces more trailing
  commits than the cap allows, the excess folds into the last admitted
  chunk's PR as an over-limit exception logged to the run, not silently
  dropped — same "never silent" guarantee as before, without needing a
  depth concept.
- No `split_max_depth` knob: because there is no recursive manifest, there is
  no second-order split to bound. A follow-up chunk that is itself oversized
  repairs exactly like any chunk does today (§1), producing its own commit
  stack under the same `split_max_chunks` cap — the mechanism is uniform at
  every level instead of degrading to silent `summary`-only recording past
  depth 1, which was a gap in the prior design (§9).

### 5.5 Inline (non-stack) runs

A lone inline run has no driver context and cannot spawn runs. For it,
splitting is a **no-op**: the run delivers its shrunken PR; the remaining
commits sit on the branch, described in `summary` for a human to act on.
Documented operator paths: `mivia stack plan`/`drive` a follow-up for the
remainder, or (v1.1) `mivia stack split <run-id>`, which admits follow-up
chunk runs directly from the branch's remaining commits (no manifest to
read — same commit-walk logic as §5.3). **This is the honest boundary of the
feature**: auto-split delivers additional PRs when the run belongs to a
stack; a standalone run hands off, it does not self-spawn.

## 6. Failure modes and guarantees

| Failure | Behavior |
|---|---|
| No trailing commits after repair, diff now fits | No split; run settles `succeeded`; `stack_remaining_commits = 0` (today's behavior) |
| Trailing commit fails to build/test standalone | Chunk run for that commit fails its normal gates; existing driver retry/error surfacing applies — same path as any failing chunk, not a bespoke manifest error |
| Parent PR amended/rebased during review | Driver rebases the remaining stack before admitting the next chunk; conflicting rebase surfaces as a per-chunk error for operator resolution (§8) |
| Stack longer than `split_max_chunks` | Excess folds into the last admitted chunk's PR, logged, never silently dropped |
| Follow-up chunk itself oversized | Repairs and produces its own commit stack, same mechanism, same cap — no special-cased depth limit needed |

## 7. Verification plan

- **Unit**: policy knob compile + defaults + clamps; `RepairTarget` unchanged
  (no regression); decompose split rule (chunk over `hard_lines` → valid
  multi-chunk plan).
- **Integration** (pattern: `internal/cli/feature_delivery_contract_test.go`,
  fake GH): fixture repo + tiny `hard_lines`; scripted repair agent shrinks
  and commits remaining scope as 2 further commits; assert:
  1. PR #1 delivered with diff ≤ `hard_lines`;
  2. delivery record carries `stack_remaining_commits = 2`;
  3. driver admits exactly 2 follow-up chunks, `pr_base` = PR #1 merge,
     ordered;
  4. each follow-up PR ≤ `hard_lines`; all merged via `stack_merge`;
  5. no follow-ups admitted when `split_deferred=false` (byte-identical to
     today);
  6. a forced amend to PR #1 before merge triggers a clean rebase of the
     remaining stack before chunk 2 is admitted.
- **Negative**: a trailing commit that fails to build surfaces a normal
  chunk-run failure, not a silent drop; a conflicting rebase surfaces an
  operator-visible error, not a stuck or silently-abandoned stack.
- **Gates**: `go build ./...`, `go test ./internal/workflows/... ./internal/cli/...`,
  pre-commit hooks (`.mivia/hooks/`), `invariants.md` review.

## 8. Known gap: conflicting rebases

When an earlier PR in the stack changes in a way that conflicts with a later
commit's content (not just a moved base), `git rebase --onto` fails
automatically and needs judgment. Real stacked-diff tools (Graphite,
ghstack) invest real engineering here. v1 scope: detect the conflict, surface
it as a per-chunk operator-actionable error via existing driver error
reporting, and stop advancing that stack until resolved. Automating conflict
resolution is explicitly out of scope for this spec and should be its own
follow-up if it becomes a frequent operator burden in practice.

## 9. Impacted files (implementation checklist)

- `internal/workflows/delivery/policy.go` — knobs + defaults + clamps
- `internal/workflows/compiler/delivery_validate.go` (+ `delivery_policy_compile_test.go`) — knob compilation/validation
- `internal/workflows/compiler/synthesis.go` — decompose-time split rule (§5.1); no new validation gate beyond existing `chunk_plan_validate`
- `internal/workflows/localengine/engine_deliver.go` — `stack_remaining_commits` count on successful re-delivery
- `internal/workflows/ledger/types.go` (`DeliveryRecord`) — `stack_remaining_commits` field, following the existing `ErrorRef`/`DiffRef` content-ref pattern
- `internal/cli/stack_reconcile.go`, `stack_admit.go`, `stack_drive.go` — admit chunk N+1 from the branch's remaining commits; rebase-before-admit (§5.3); **plus** the concurrency and progressive-decomposition changes in §12
- `internal/workflows/delivery/stacking.go` — commit-walk helper (list unshipped commits on a branch), reused by both the driver and (v1.1) `mivia stack split`
- `internal/workflows/controller/stacking.go` — fan-out-aware step synthesis (§12.3)
- `.mivia/workflows/schemas/chunk-plan-v1.json` — remove/raise the fixed 12-chunk ceiling per wave (§12.1)
- `.mivia/workflows/templates/repair.md`, `bugfix-repair.md`, and a new/updated decompose template — instruct committing shrink + remainder as an ordered stack (§5.2), and instruct decompose to plan incrementally per wave rather than the whole DAG up front (§12.1)
- `docs/architecture/workflows.md`, `docs/product/workflows-guide.md` — document `split_deferred`/`decompose_split_hard_lines` knobs, the rebase-conflict boundary (§8), and correct the concurrency claims per §12.4 (docs must describe what actually runs, not stated intent)
- `.mivia/skills/` — new `dag-task-decomposition`-equivalent skill, if warranted (§12.5)
- Tests per §7 and §12

## 10. Prior design and why it changed

The first draft of this spec used a repair-agent-authored
`.mivia/split/deferred-v1.json` manifest, validated by a new
synthesis-compiled schema gate, referenced from the ledger via
`deferred_manifest_ref`, with follow-up `chunk_plan`s built directly from the
manifest by the driver (bypassing decompose). Review surfaced:

- **Trust boundary (blocker):** schema validation checks shape, not truth; a
  well-formed manifest can still misdescribe the real diff, and nothing
  cross-checked it against the actual reverted worktree.
- **Staleness (blocker):** follow-ups admitted only after parent-PR merge,
  which can take days; the manifest was captured at repair time with no
  re-validation before use.
- **Over-coupling (major):** 10+ files touched for what is fundamentally
  "read committed scope, admit chunk runs."
- **False pattern reuse (major):** the manifest was described as mirroring
  `chunk_plan`'s schema-gate pattern, but chunk_plan is produced *before*
  code changes against a clean base; the manifest was produced *after* a
  partial revert and consumed by a different, later run — materially
  different freshness guarantees.
- **`split_max_depth=1` didn't bound anything (major):** it only forbade
  recursive splitting; a doubly-deferred chunk had no manifest at all and
  silently vanished into `summary`, reintroducing the original bug one level
  down.
- **No external precedent:** no stacked-diff tool (Graphite, ghstack,
  git-spice, Phabricator) splits by a data manifest; all split along commit
  boundaries, which is what this revision adopts.

This revision keeps the goal, the bounded/opt-in posture, and the existing
stacking driver untouched, while replacing the manifest with git commits as
the source of truth — removing the trust-boundary and staleness blockers by
construction rather than by added validation.

## 11. Open questions for review

1. Default of `split_deferred`: `false` (opt-in, this spec) vs `true` when
   the workflow declares `[stacking]`. Recommend opt-in for v1; graduating to
   default-on is blocked on real-world evidence that rebase-conflict
   frequency (§8) is low enough not to need automation.
2. Should `decompose_split_hard_lines` (§5.1, prevention) default to `true`
   independently of `split_deferred` (§5.2, correction)? Recommend yes —
   prevention reuses existing, already-trusted machinery and has none of the
   correction path's open risks.
3. Should `mivia stack split <run-id>` (inline hand-off, §5.5) be v1 or v1.1?
   Recommend v1.1 — driver-side auto-split is the primary path.
4. PR title/summary for follow-ups: derive from the commit message of the
   corresponding trailing commit + parent PR title. Confirm acceptable for
   v1.
5. Conflicting-rebase handling (§8) is scoped out of v1 as manual/operator-
   resolved. Confirm that's acceptable, or whether a minimal auto-resolve
   (e.g., only advance the stack on clean rebases, else pause) is needed
   before v1 ships.

## 12. Prerequisite: parallel DAG execution at scale

A driver-code audit plus external research (Devin PR Stacks, Claude Code
worktrees, Conductor, Graphite Agent — see §10-style review discipline
applied to this addendum) established two things: (a) DAG-scoped small
chunks in isolated worktrees delivering independent PRs is the real-world
default shape for this problem, and (b) this repo already has three of the
four load-bearing pieces — a real, Kahn's-algorithm-checked dependency DAG
(`chunk_plan.chunks[].depends_on`, `stack_reconcile.go`), worktree-per-run
isolation generic to the engine (`Engine.ensureRunWorktree`), and per-chunk
PR delivery. What's missing, and what this section fixes, is concurrent
execution of an independent wave, and a chunk-count model that can express
large plans. Both are corrected here as v1 prerequisites for §1–§11, not
deferred enhancements — the correction mechanism (commit stacks) only
matters at meaningful scale if prevention (small, parallel, disjoint chunks)
is actually parallel and actually scales past a dozen chunks.

### 12.1 Remove the fixed chunk ceiling; decompose incrementally per wave

`.mivia/workflows/schemas/chunk-plan-v1.json` currently caps `chunks` at 12
in a single decompose output. This is not a knob to bump — a decompose agent
asked to emit a valid, dependency-correct, file-disjoint JSON array of
hundreds of chunks in one call is unreliable at that scale independent of
what `maxItems` says (long-output degradation, dependency-graph errors
compounding across hundreds of entries, no way to course-correct mid-plan).

Fix: decompose becomes **incremental and wave-scoped**, not one-shot:

- Decompose still runs through the existing `decompose_repair` loop
  (`synthesis.go`), but its unit of output changes from "the whole DAG" to
  "the next wave(s) of chunks, plus an explicit `has_more: bool` and a
  `remaining_scope` summary the next decompose call consumes as input."
  `chunk-plan-v1.json`'s per-call `chunks` cap can now stay small (e.g. 12,
  or whatever keeps a single call reliable) because it bounds *one wave*,
  not *the whole plan*.
- The stack driver requests the next wave from decompose once the current
  frontier is fully admitted (a natural fit for `stack_reconcile.go`'s
  existing convergence pass), rather than requiring the full DAG up front.
- Total plan size is now bounded only by an explicit driver-level cap (new
  knob, §12.2), not by what one LLM call can reliably emit — this is what
  actually enables "hundreds of chunks" rather than just relabeling the same
  ceiling.
- `depends_on` references across waves must resolve against chunk IDs from
  *any* prior wave, not just the current one — `stackTopologicalOrder` needs
  to operate over the accumulated cross-wave graph, not each wave in
  isolation.

### 12.2 New/changed policy knobs

| Knob | Type | Default | Meaning |
|---|---|---|---|
| `max_total_chunks` | int | `200` | Hard cap across all waves of one plan (replaces the old single-call 12-chunk ceiling as the real ceiling); driver refuses to admit past this and surfaces an operator-actionable error, never silently truncates |
| `max_wave_chunks` | int | `12` | Per-decompose-call cap (keeps one LLM call reliable) — this is the old `chunk-plan-v1.json` `maxItems`, now scoped to one wave instead of the whole plan |
| `max_concurrent_chunks` | int | `4` | Max chunk runs admitted and driven concurrently within a ready wave (§12.3) — bounds worktree/agent fan-out, distinct from `max_wave_chunks` |

### 12.3 Make wave execution concurrent, not sequential

`driveStack`'s wave loop (`internal/cli/stack_admit.go:62-85`) currently
iterates a ready wave with a plain sequential `for` loop and halts the whole
drive pass after the first chunk reaches `RunStatusDeliveryPending`
(explicit comment: `// sequential create-merge (v1): one chunk per drive
pass`). This is the one actual gap identified by the driver audit — DAG
modeling, wave computation (`nextAdmissionWave`), and worktree isolation are
already correct and reusable as-is.

Fix:

- Replace the sequential loop with bounded concurrent dispatch
  (`errgroup.WithContext`, capped at `max_concurrent_chunks`) over
  `nextAdmissionWave`'s output. Each chunk already gets its own isolated
  worktree via `admitStackChunkRun` → per-`runID` `ensureRunWorktree`, so
  concurrent admission is safe by construction — no new isolation work
  needed.
- Rework the halt-after-first-delivery-pending semantics: multiple chunks
  must be able to sit in `RunStatusDeliveryPending` simultaneously, awaiting
  publish, without the drive pass considering itself done. `stack_drive.go`
  and `stack_reconcile.go` need to track "wave in flight" state that
  survives a chunk delivering while siblings are still running.
- **Merging stays serialized**, matching both the existing topological-order
  guarantee and the external-research consensus (serialized merge queue,
  §10-equivalent findings) — only the *work* (agent execution in separate
  worktrees) is parallelized, not the *merge* (`stack_merge.go` unchanged,
  still processes merges in dependency order one at a time).
- `internal/workflows/controller/stacking.go` currently only synthesizes
  per-run steps with no fan-out concept; it needs to recognize "N chunk runs
  admitted concurrently for the same stack" as a first-class state rather
  than assuming one active chunk run per stack at a time.

### 12.3a Preliminary hardening findings (must land before §12.3's concurrency change)

An adversarial review of the current driver code, run specifically to check
whether it's safe to build concurrency on top of, found real — not
hypothetical — races and a duplicated-validation risk. These are not new
scope; they are latent bugs in the current sequential implementation that
only manifest once wave admission stops being single-threaded, so they must
be fixed in a preliminary phase, not folded into or deferred past §12.3:

1. **Two sources of truth for chunk-plan validity.**
   `internal/workflows/compiler/synthesis.go:19` wires decompose's
   `OutputSchema` to `.mivia/workflows/schemas/chunk-plan-v1.json`, which
   hardcodes `maxItems: 12` and `est_diff_lines` `maximum: 400`.
   Independently, `internal/workflows/controller/stacking.go:311`
   (`ValidateChunkPlan`) re-implements the same shape check in Go against
   `cfg.MaxChunks`/`cfg.HardLines`/`cfg.MaxFiles`, sourced from
   `StackingConfig`, not the schema file. Nothing ties the schema's literal
   numbers to `cfg`'s values — they agree today only by convention. §12.1's
   move to per-wave/cross-wave caps (`max_wave_chunks`, `max_total_chunks`)
   makes this worse: a cross-call total cap has no JSON Schema
   representation at all, so `ValidateChunkPlan` must become the sole
   quantitative gate, config-driven, with the schema stripped down to
   structural/type checks only. Resolve before §12.1's incremental-decompose
   work lands (Phase 3 below), not after.
2. **`driveChunk`'s admission check is TOCTOU — will double-admit under
   concurrency.** `internal/cli/stack_admit.go:100-106`: a ledger scan
   (`stackRunRef`) and a `TransitionTask` call happen as two unsynchronized
   steps. Safe today only because the sequential wave loop guarantees one
   goroutine calls `driveChunk` at a time. The moment §12.3 makes wave
   execution concurrent, two goroutines can both pass the "not found" check
   for the same chunk before either transitions it, producing two live runs
   for one chunk — defeating the stable-admission-key invariant the design
   depends on. **Blocking fix**: make check-and-transition atomic — a
   per-chunk lock, or a compare-and-swap transition in `tasks.Store` that
   fails if the chunk isn't in the expected pre-state.
3. **`driveStack`'s cached `byID`/`merged` maps are mutable local state
   shared across what will become concurrent goroutines.**
   `internal/cli/stack_drive.go`/`stack_admit.go:54,79-84`: computed once per
   wave, refreshed only after each chunk settles, inside a loop that assumes
   one chunk mutates state at a time. Under concurrent admission this is a
   data race (concurrent map access) or a stale-read bug (goroutines working
   from a snapshot that's gone out of date). **Blocking fix**: re-derive
   from the ledger per admission decision instead of caching, or restructure
   so goroutines only read immutable per-wave state and report completions
   through a channel the driver serializes.
4. **Attempt-count read-then-write (`reopenOrFail`/`applyReconcileAction`)
   is non-atomic.** `stack_drive.go:236-271,360-372`: attempt count is read
   via a full transition-log scan, then a separate decision, then a separate
   write — three non-transactional ledger operations. Two concurrent failure
   handlers for the same chunk could both read the same count and both
   write `reopened`, silently doubling retries or skipping the terminal-
   failure halt. **Blocking fix**: same per-chunk atomic-transition guard as
   finding 2.
5. **Minor, non-blocking**: `allMerged` (`stack_drive.go:320-330`) appears to
   have no callers in the reviewed files — confirm dead-or-live before
   reusing/deleting it in the new wave-scheduling logic, don't let it linger
   silently. `chunkPartIndex`'s linear scan (`stack_drive.go:309-316`)
   silently returns `0` (not an error) on a not-found chunk id — tighten to
   an explicit error before §12.1 introduces chunk ids across waves that an
   `order` slice built from only the current wave won't yet know about.

Findings 2 and 4 are load-bearing: without them, concurrent wave execution
has a real double-admission and double-retry race, not a theoretical one.
Finding 1 blocks §12.1 specifically. Finding 3 is resolved as a natural
consequence of fixing 2 and 4 (per-chunk-scoped reads replace the shared
cache). See Phase 1 in §13 below, which lands all of this before the
concurrency change.

### 12.4 Docs must describe what's implemented, not intent

`docs/architecture/workflows.md`'s wave-admission section currently
describes stable admission keys and wave computation without ever claiming
concurrent execution — accurate today, but it must be updated alongside
§12.3 landing so it doesn't silently become stale-and-wrong (docs describing
aspirational concurrency that isn't real is exactly the failure mode this
addendum is fixing). Update in the same PR/phase that lands the concurrency
change, not after.

### 12.5 Optional: a `dag-task-decomposition` skill

`go-mivia` has a `dag-task-decomposition` skill; `mivia-agent` has no
equivalent — the mechanism here is purely CLI-driver-level, not an
agent-invoked skill. Whether this is worth porting is a v1 open question
(§13 phase plan treats it as optional, cut if it doesn't clearly pay for
itself): the CLI driver already does the DAG mechanics; a skill would only
add value if there's a recurring human/agent workflow of *authoring* DAG
plans outside the automated decompose step. Recommend deferring unless a
concrete need surfaces during implementation.

## 13. Phased implementation plan

Each phase is scoped to be independently implementable, testable, and
mergeable — later phases depend on earlier ones landing, but no phase
requires speculative knowledge of a later phase's internals. Every phase
ends with integration tests exercising the real driver/engine (fixture
repo + fake GH, pattern: `internal/cli/feature_delivery_contract_test.go`);
the final phase adds an end-to-end test spanning the full flow. Each phase
below includes a self-contained prompt suitable for handing to an
implementing agent with no other context than this spec.

---

### Phase 0 — Ceiling and knobs only (no behavior change)

**Goal**: introduce the new knobs and raise/rescope the chunk ceiling
without changing any runtime behavior yet. Pure plumbing; de-risks
everything after it.

**Scope**: `.mivia/workflows/schemas/chunk-plan-v1.json` (rescope `chunks`
`maxItems` as a per-wave cap, still 12), `internal/workflows/delivery/policy.go`
(add `max_total_chunks=200`, `max_wave_chunks=12`, `max_concurrent_chunks=4`
with clamps), `internal/workflows/compiler/delivery_validate.go` (+ compile
test). No behavior wired up yet — knobs compile, validate, and default, and
are inert.

**Tests**: unit tests for knob compilation, defaults, and clamps
(`delivery_policy_compile_test.go`); schema still validates existing
fixtures byte-for-byte.

**Agent prompt**:
> Repo: mivia-agent (Go). Read `internal/workflows/delivery/policy.go` and
> `internal/workflows/compiler/delivery_validate.go` to learn the existing
> pattern for `[stacking]` policy knobs (e.g. `StackingHardLines`,
> `MaxRepairs`) — defaults, clamping, and compile-time validation. Add three
> new knobs to the same `Policy` struct and compilation path:
> `MaxTotalChunks` (default 200), `MaxWaveChunks` (default 12),
> `MaxConcurrentChunks` (default 4), each clamped to a sane minimum/maximum
> following the existing clamp pattern (see `DefaultMaxDeliveryRepairs`).
> Do not wire these into any runtime behavior yet — this phase only adds the
> knobs, their defaults, their clamps, and their compile-time validation.
> Update `.mivia/workflows/schemas/chunk-plan-v1.json`'s description string
> to say the `chunks` `maxItems` (still 12) is a per-wave cap, not a
> whole-plan cap — the number itself does not change in this phase. Add/
> update unit tests in `delivery_policy_compile_test.go` for the three new
> knobs' defaults and clamps. Run `go build ./...` and
> `go test ./internal/workflows/...` and confirm they pass. Do not touch
> `stack_admit.go`, `stack_drive.go`, `stack_reconcile.go`, or any decompose
> template.

---

### Phase 1 — Harden the driver for concurrency (must land before Phase 2)

**Goal**: fix the load-bearing races and the duplicated-validation risk
found by the pre-concurrency driver audit (§12.3a), so Phase 2's concurrency
change lands on solid ground instead of racing the moment it's turned on.
This is not new scope — these are latent bugs in the current sequential
code that only matter once single-threaded access stops being guaranteed.

**Scope**: `internal/cli/stack_admit.go` (`driveChunk`'s admission
check-then-transition → atomic per-chunk guard), `internal/cli/stack_drive.go`
(`byID`/`merged` caching → re-derive per decision or serialize via channel;
`reopenOrFail`/`applyReconcileAction` attempt-count read-then-write → atomic
per-chunk transition; `chunkPartIndex`'s silent-`0`-on-not-found → explicit
error), `internal/workflows/controller/stacking.go` (`ValidateChunkPlan`
becomes the sole quantitative gate for chunk-plan limits; confirm/resolve
`allMerged`'s dead-code status), `.mivia/workflows/schemas/chunk-plan-v1.json`
(strip numeric ceilings down to structural/type checks only, since §12.1
introduces a cross-wave total cap no single JSON Schema call can enforce).

**Tests**: a concurrency-focused regression suite — two goroutines racing
`driveChunk` for the same chunk id must never both succeed in admitting a
run (assert exactly one wins, one gets a clean "already admitted" outcome,
not two live runs); two concurrent failure-handling paths for the same
chunk must not double-increment/double-reopen past `MaxRepairs`; `go test
-race` across the touched packages must be clean. Unit test asserting the
schema and `ValidateChunkPlan` can never disagree (or that the schema no
longer expresses a bound `ValidateChunkPlan` doesn't also enforce).
`chunkPartIndex` returns an explicit error (not `0`) for an unknown chunk
id, with a test covering it.

**Agent prompt**:
> Repo: mivia-agent (Go). Read `docs/architecture/spec-auto-split-oversized-prs.md`
> §12.3a in full — it lists 5 specific findings from an adversarial review of
> the current stack-driver code, done specifically to find races and
> duplicated logic that would make the *next* phase (concurrent wave
> execution) unsafe. Your job is to fix findings 1-4 (5 is optional cleanup,
> do it if cheap).
>
> Finding 1 (two sources of truth): `internal/workflows/controller/stacking.go`'s
> `ValidateChunkPlan`/`validateChunkPlanEntry`/`validateChunkPlanMulti` and
> `.mivia/workflows/schemas/chunk-plan-v1.json`'s hardcoded `maxItems: 12`/
> `est_diff_lines maximum: 400` currently duplicate the same quantitative
> checks from two unrelated sources (Go `StackingConfig` vs. a static JSON
> file). Make `ValidateChunkPlan` the single quantitative authority, driven
> by config; strip the schema down to structural/type validation only (no
> numeric bounds). Add a test proving the schema can no longer silently
> diverge from the Go validator (e.g. the schema has no bound left to
> diverge on, or an explicit cross-check test if you keep a schema-side
> bound for a good reason — explain why in a code comment if so).
>
> Finding 2 (TOCTOU admission race): in `internal/cli/stack_admit.go`,
> `driveChunk`'s check-then-transition (`stackRunRef` scan followed by
> `TransitionTask`) is not atomic. Make it atomic: either a per-chunk lock
> around the check-and-transition, or push the guard into the ledger as a
> compare-and-swap transition that fails if the chunk isn't in the expected
> pre-state (check `tasks.Store` / `internal/workflows/ledger` for the
> right primitive — prefer extending existing ledger transition semantics
> over adding a new locking layer if one already exists for this purpose).
>
> Finding 3 (shared mutable cache): in `internal/cli/stack_drive.go`, the
> `byID`/`merged` maps computed once per wave and only refreshed after each
> sequential chunk settles are a data race under concurrent execution.
> Replace with either re-derivation from the ledger per admission decision,
> or restructure so per-chunk goroutines only read immutable per-wave
> snapshots and report completions through a channel the driver serializes
> — pick whichever fits the codebase's existing concurrency idioms better
> (check for precedent via `codegraph explore` before choosing).
>
> Finding 4 (non-atomic attempt-count read-then-write): `reopenOrFail`/
> `applyReconcileAction` read attempt count via a transition-log scan, then
> decide, then write, as three separate operations. Apply the same
> atomic-transition guard as finding 2, scoped per chunk task, so two
> concurrent failure handlers for the same chunk can't both read the same
> count and both reopen.
>
> Finding 5 (optional): confirm whether `allMerged` (`stack_drive.go`) has
> any callers; if truly dead, delete it. Change `chunkPartIndex`'s
> not-found case from a silent `0` return to an explicit error.
>
> Write a concurrency regression test: spin up two goroutines calling
> `driveChunk` (or the relevant admission entrypoint) for the same chunk id
> simultaneously and assert exactly one admits a run, the other observes a
> clean already-admitted outcome — never two live runs. Similarly for the
> failure-handling path: two concurrent failures for the same chunk must not
> double-increment past `MaxRepairs`. Run `go test -race ./internal/cli/...
> ./internal/workflows/...` and confirm clean, plus `go build ./...`. Do not
> add wave-level concurrency yet (that's the next phase) — this phase only
> makes the existing sequential-safe assumptions actually safe for what
> comes next.

---

### Phase 2 — Concurrent wave execution (the core fix)

**Goal**: make `driveStack` admit and drive an entire ready wave
concurrently, bounded by `max_concurrent_chunks`, instead of sequentially
halting after the first chunk reaches delivery-pending. Depends on Phase 1
landing first — this phase is the reason Phase 1's atomicity fixes exist.

**Scope**: `internal/cli/stack_admit.go` (wave loop → bounded concurrent
dispatch via `errgroup`), `internal/cli/stack_drive.go` (remove/rework the
halt-after-first-delivery-pending assumption; track in-flight wave state),
`internal/cli/stack_reconcile.go` if it holds any single-active-chunk
assumptions, `internal/workflows/controller/stacking.go` (recognize N
concurrently-admitted chunk runs per stack as valid state).

**Tests**: integration test — fixture repo, a chunk plan with 3 mutually
independent chunks (no `depends_on` between them) and `max_concurrent_chunks
= 3`; assert all 3 chunk runs are admitted and reach delivery-pending in a
single `stack drive` pass (not 3 separate invocations, which was the old
behavior); assert their worktrees are distinct paths; assert merges still
happen in the plan's declared order even though execution was concurrent. A
second test with a dependency edge (chunk B depends on chunk A) asserts B is
*not* admitted until A merges, proving concurrency didn't break ordering.
Negative test: set `max_concurrent_chunks = 1` and confirm behavior is
byte-identical to today's sequential path (regression guard).

**Agent prompt**:
> Repo: mivia-agent (Go). Read `internal/cli/stack_admit.go`,
> `internal/cli/stack_drive.go`, and `internal/cli/stack_reconcile.go` in
> full, paying attention to `driveStack`'s wave loop (around line 62-85 in
> `stack_admit.go` as of this writing — verify current line numbers) and its
> comment `// sequential create-merge (v1): one chunk per drive pass`, and to
> `nextAdmissionWave` (`stack_reconcile.go`) which computes the ready wave.
> Also read `admitStackChunkRun` to confirm each chunk run already gets an
> isolated worktree via a distinct `runID` (it does — this phase does not
> need to add worktree isolation, only concurrency).
>
> Change `driveStack`'s wave loop from sequential `for`-loop execution to
> bounded concurrent dispatch, capped at the new `MaxConcurrentChunks` policy
> knob from Phase 0 (default 4), using `golang.org/x/sync/errgroup` (check
> `go.mod` — add the dependency if not already present, or use an equivalent
> already-used concurrency primitive in this codebase if `errgroup` isn't
> idiomatic here — check for existing patterns first via
> `codegraph explore "errgroup concurrent worker pool"` or grep). Each
> goroutine in the group admits and drives exactly one chunk from the wave.
>
> The current code halts the entire drive pass as soon as one chunk reaches
> `RunStatusDeliveryPending`. Remove this assumption: the drive pass should
> only consider itself done with a wave once *all* dispatched chunks in that
> wave have either reached a terminal/pending state or failed. Multiple
> chunks must be able to sit in `RunStatusDeliveryPending` at once. Check
> `internal/workflows/controller/stacking.go` for any code that assumes one
> active chunk run per stack, and update it to tolerate N concurrent chunk
> runs for the same stack.
>
> Do NOT change merge behavior — `stack_merge.go` must still process merges
> one at a time in the plan's topological order; only chunk *execution*
> (agent work in separate worktrees) becomes concurrent.
>
> Write integration tests in the pattern of
> `internal/cli/feature_delivery_contract_test.go` (fixture repo + fake GH):
> (1) three mutually-independent chunks with `max_concurrent_chunks=3` all
> admit and reach delivery-pending within a single `stack drive` invocation,
> with three distinct worktree paths; (2) a chunk with a `depends_on` edge is
> not admitted until its dependency merges, proving ordering survives
> concurrency; (3) `max_concurrent_chunks=1` reproduces today's sequential
> behavior exactly (regression guard). Run
> `go test ./internal/cli/... ./internal/workflows/...` and `go build ./...`
> and confirm all pass before finishing.

---

### Phase 3 — Incremental, wave-scoped decompose (removes the real ceiling)

**Goal**: decompose stops trying to emit the whole DAG in one call; it emits
one wave plus `has_more`/`remaining_scope`, and the driver requests
successive waves as the frontier clears — this is what actually lets total
plan size scale to hundreds of chunks, bounded by `max_total_chunks`
(Phase 0), not by single-call reliability. Depends on Phase 1 (finding 1
made `ValidateChunkPlan` the sole quantitative gate, which this phase's
cross-wave total cap requires) and Phase 2.

**Scope**: `internal/workflows/compiler/synthesis.go` (decompose step
contract — output now includes `has_more`, `remaining_scope`),
`.mivia/workflows/schemas/chunk-plan-v1.json` (add `has_more`,
`remaining_scope` fields), `.mivia/workflows/templates/`-equivalent decompose
prompt template (instruct incremental planning), `internal/cli/stack_reconcile.go`
(request next wave from decompose once current frontier is admitted; track
cross-wave `depends_on` resolution), driver-level enforcement of
`max_total_chunks` with an operator-actionable error (never silent
truncation) when exceeded.

**Tests**: integration test — fixture scenario forcing 3 waves (e.g. a
decompose stub that only ever proposes ≤4 chunks per call but declares
`has_more=true` twice before `false`); assert the driver requests wave 2 and
3 automatically, assert a chunk in wave 3 can `depends_on` a chunk ID from
wave 1 and resolves correctly, assert total admitted chunks across all
waves is correctly tracked against `max_total_chunks`. Negative test: a
plan that would exceed `max_total_chunks` fails with a clear, operator-
visible error rather than silently truncating or hanging.

**Agent prompt**:
> Repo: mivia-agent (Go). This phase depends on Phase 0 (knobs:
> `MaxTotalChunks`, `MaxWaveChunks`), Phase 1 (driver hardening — in
> particular, `ValidateChunkPlan` is now the sole quantitative gate, which
> this phase's cross-wave `MaxTotalChunks` enforcement relies on), and
> Phase 2 (concurrent wave execution) already being merged. Read
> `internal/workflows/compiler/synthesis.go`'s
> `decompose_repair` loop and `.mivia/workflows/schemas/chunk-plan-v1.json`
> in full.
>
> Change the decompose step's output contract: add `has_more` (bool) and
> `remaining_scope` (string, free-text summary of what's left to plan) to
> `chunk-plan-v1.json`, both required fields. Update whatever prompt/template
> governs the decompose agent's instructions (grep `.mivia/workflows/` for
> the decompose step's prompt source — likely near where `chunk_plan_validate`
> is referenced) to tell it: plan only the next wave of up to `MaxWaveChunks`
> chunks, set `has_more=true` and describe what's left in `remaining_scope`
> if there's more scope than one wave can hold, otherwise `has_more=false`.
>
> In `internal/cli/stack_reconcile.go`, when the driver's convergence pass
> finds a stack whose current frontier is fully admitted but whose last
> decompose output had `has_more=true`, it must reopen/re-invoke decompose
> for the next wave, passing `remaining_scope` as context. Track cross-wave
> chunk IDs so `stackTopologicalOrder` resolves `depends_on` edges that
> reference chunks from earlier waves, not just the current one — this means
> the accumulated DAG (not just the latest wave) must be available to the
> topological sort.
>
> Enforce `MaxTotalChunks` at the driver level: if accumulated chunks across
> all waves would exceed it, refuse to admit further waves and surface a
> clear, operator-actionable error on the run/stack — never truncate a plan
> silently.
>
> Write an integration test using a stubbed/scripted decompose agent that
> returns 3 successive waves (`has_more=true, true, false`) with a
> cross-wave `depends_on` edge (a wave-3 chunk depending on a wave-1 chunk
> ID), and assert: all waves are requested automatically without operator
> intervention, the cross-wave dependency resolves and is respected in
> admission order, and total chunk count is tracked correctly. Add a
> negative test where total chunks would exceed `MaxTotalChunks` and assert
> a clear error surfaces rather than silent truncation or an infinite wave-
> request loop. Run `go build ./...` and
> `go test ./internal/workflows/... ./internal/cli/...` and confirm green.

---

### Phase 4 — Docs correction (lands with or immediately after Phase 2)

**Goal**: close the "docs describe intent, not implementation" gap
(§12.4) — must not be allowed to drift once Phase 2 lands.

**Scope**: `docs/architecture/workflows.md`, `docs/product/workflows-guide.md`.

**Tests**: `make docs-check` (per `AGENTS.md` local commands); no code
tests, this phase is documentation-only.

**Agent prompt**:
> Repo: mivia-agent. Phase 2 (concurrent wave execution, see git history/PR
> for `stack_admit.go`, `stack_drive.go`) has landed. Read
> `docs/architecture/workflows.md`'s wave-admission section (search for
> "wave" and "admission key") and update it to accurately describe that
> independent chunks within a ready wave now execute concurrently
> (bounded by `max_concurrent_chunks`), each in its own isolated worktree,
> while merges remain strictly sequential in topological order. Do not
> overstate — only claim what Phase 2 actually implemented (check the merged
> code, not this spec, as the source of truth for exact behavior). Also
> update `docs/product/workflows-guide.md` if it references chunk/stack
> execution for an operator-facing audience. Run `make docs-check` and
> confirm it passes (owned-docs machine enforcement per `docs/OWNERS.yaml`).

---

### Phase 5 — Correction path: commit-stack repair (§5 of this spec)

**Goal**: implement the commit-stack-based repair/delivery correction
mechanism from §5, now that prevention (Phases 1-3) handles the common case
and this only needs to fire when a chunk's estimate was wrong.

**Scope**: as listed in §9 for `engine_deliver.go`, `ledger/types.go`,
`stack_reconcile.go`/`stack_admit.go`/`stack_drive.go` (rebase-before-admit),
`delivery/stacking.go` (commit-walk helper), `repair.md`/`bugfix-repair.md`.

**Tests**: exactly the integration/negative tests specified in §7, plus the
rebase test in §7 item 6.

**Agent prompt**:
> Repo: mivia-agent. Phases 0-3 (chunk ceiling/knobs, driver hardening,
> concurrent wave execution, incremental decompose) are merged. Implement §5 of
> `docs/architecture/spec-auto-split-oversized-prs.md` exactly as written:
> the repair step commits a shrunk PR-sized slice plus one or more
> additional commits for deferred scope on the same branch (no manifest
> file), the engine records `stack_remaining_commits` (a `git rev-list
> --count` derived integer) on the delivery record following the existing
> `ErrorRef`/`DiffRef` content-ref pattern on `DeliveryRecord`
> (`internal/workflows/ledger/types.go`), and `stack_reconcile.go` admits
> chunk N+1 from the branch's next unshipped commit once chunk N's PR
> merges, rebasing the remaining stack (`git rebase --onto`) if the parent
> branch was amended during review. Implement the `split_deferred`,
> `split_max_chunks`, `split_min_lines` knobs from §5.4. Update
> `.mivia/workflows/templates/repair.md` and `bugfix-repair.md` per §5.2.
> Write the integration tests specified in this spec's §7 (PR #1 delivers
> ≤hard_lines, `stack_remaining_commits` recorded correctly, follow-ups
> admitted in order with correct `pr_base`, all merge via `stack_merge`, no
> follow-ups when `split_deferred=false`, and the rebase test: a forced
> amend to PR #1 before merge triggers a clean rebase of the remaining stack
> before chunk 2 is admitted) plus the negative tests (trailing commit that
> fails to build surfaces as a normal chunk failure; a conflicting rebase
> surfaces an operator-visible error per §8, does not hang or silently drop
> the stack). Run `go build ./...` and
> `go test ./internal/workflows/... ./internal/cli/...`, pre-commit hooks,
> and confirm green.

---

### Phase 6 — End-to-end test and knob defaults review

**Goal**: one full end-to-end test spanning decompose → concurrent wave
execution across 2+ waves → a chunk that overflows and triggers the
commit-stack correction path → rebase-on-amend → all merges — proving
Phases 1-5 compose correctly together, not just in isolation. Then revisit
§11's open questions with real implementation experience in hand.

**Scope**: new e2e test only; no production code changes expected unless
the e2e test surfaces an integration bug between phases (if so, fix in this
phase and note it).

**Tests**: one comprehensive e2e integration test (fixture repo + fake GH)
covering the full flow described above in one run. `make verify` and
`make test` full suite green.

**Agent prompt**:
> Repo: mivia-agent. Phases 0-5 are merged. Write one end-to-end integration
> test (pattern: `internal/cli/feature_delivery_contract_test.go`) that
> exercises the full flow in a single fixture-repo scenario: a plan that
> requires 2 decompose waves (Phase 3), where wave 1 contains 3 mutually
> independent chunks that execute concurrently (Phase 2, built on Phase 1's
> hardening) and all deliver successfully, and one chunk in wave 2 delivers
> oversized despite a good estimate, triggering the commit-stack correction
> path (Phase 5) which produces 2 follow-up PRs, one of which requires a
> rebase because its parent PR was amended during review before merging.
> Assert the entire sequence completes correctly: all PRs delivered, all
> merged in correct topological order, worktrees isolated throughout, no
> silent scope loss at any point, and no data races (`go test -race`). If
> this test surfaces any integration gap between the phases (e.g. an
> assumption Phase 5 makes that Phase 3's incremental decompose violates),
> fix it and note the fix clearly in the PR description. Then
> re-read `docs/architecture/spec-auto-split-oversized-prs.md` §11's open
> questions and, based on what was actually built, propose answers with
> reasoning grounded in the implementation (not speculation) — do not change
> defaults without flagging them for a human decision, since §11 items like
> the `split_deferred` default are explicitly a judgment call for the repo
> owner. Run `make verify` and `make test` in full and confirm green.

---

Ordering: Phase 0 → Phase 1 (hardening) → Phase 2 (concurrency) → Phase 4
(docs, lands immediately after Phase 2) → Phase 3 (incremental decompose) →
Phase 5 (commit-stack correction) → Phase 6 (e2e). Phase 1 is a hard
prerequisite of Phase 2 — the concurrency change is unsafe without it, per
§12.3a. Phase 4 is placed right after Phase 2 rather than at the end
specifically so documentation never describes unimplemented concurrency,
per §12.4's "must not drift" requirement. Phase 3 and Phase 5 have no
ordering dependency on each other beyond both needing Phases 1-2, but are
listed in this order because Phase 5 (correction) is only meaningfully
testable at scale once Phase 3 (removing the real ceiling) is in place.
