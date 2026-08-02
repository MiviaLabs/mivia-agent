# 51.04 - Class-aware token cost (content-class estimation)

**Status:** DESIGN LOCKED - ADLC Step 0 complete (2026-08-02). Ready for
Step 1 micro-task breakdown / implementation.
**Date:** 2026-08-02 (revised after challenge)
**Part of:** program `51` (`00-overview.md`).
**Depends on:** nothing.
**Blocks:** nothing. Informs residual measurement for a possible later
multi-ratio follow-on (explicitly deferred; see §10).
**Blast radius:**
- **Product:** LOW - estimation accuracy only. No message shape, no durable
  storage, no tool surface.
- **Engineering:** MEDIUM - `provider` cost walk, planner golden totals,
  calibration denominator fixtures, token-usage telemetry fields for class
  shares (optional slice).

## 0. Step 0 disposition (hostile challenge)

Panel: architecture-review (BLOCK on original multi-ratio design),
correctness audit (NEEDS-REWORK), call-site / mass audit (evidence).

### Original design (rejected as primary)

Three EWMA ratios + dominance-gated residual attribution against a single
provider `InputTokens` total.

| Finding | Severity | Disposition |
|---------|----------|-------------|
| Multi-ratio EWMA is higher complexity than a static per-class estimator bias requires | BLOCK | **Accepted.** Ship class-aware base estimation + keep one residual EWMA. |
| Tool results are `Message.Content`; putting them in ClassProse makes “tool-heavy” train the prose ratio and starves ClassToolArgs | BLOCK | **Accepted.** Tool-result bodies are **structured**, not prose. |
| Scalar `applyCalibration` + `EstimateMessageTokens` path underspecified for multi-ratio | BLOCK | **Accepted for multi-ratio; N/A for locked design.** Class-aware costs apply inside the estimator; single residual ratio still multiplies totals on every planner path. |
| Dominance residual math undefined / biased at 70% purity | BLOCK | **Accepted.** Multi-ratio deferred until residual mix-shift is measured after Stage A. |
| Cold start “identical to today” false under dominance gates | Confirmed | **Accepted.** Multi-ratio cold-start claim retracted with the design. |
| Incomplete field→class map (call.ID, Type, Function.Name, frames) | Confirmed | **Closed** in §4.2. |
| `adoptCalibration` multi-counter merge undefined | Confirmed | **N/A** - single `Samples` retained. |
| Blast radius “LOW” understated engineering surface | MEDIUM | **Accepted.** Header split product vs engineering. |
| Breaking `EstimateRequestCost` return type | MEDIUM | **Rejected breakage.** Additive `Cost` breakdown; `(int, error)` wrappers stay. |
| ClassSchema EWMA throwaway if `05` gates schemas | MEDIUM | **Accepted.** No per-class EWMA; schemas use structured divisor only. |
| `promptBudgetErrorWithTools` ignores calibration | Pre-existing | **Out of primary scope** but recorded: same raw estimator; residual ratio still not applied on that path. Do **not** expand scope to fix unless a micro-task fits; document in residual risk. |

### Locked thesis

The systematic error is **classifiable at the field walk** (JSON / tool bodies
/ schemas tokenize denser than English prose). Fix it **in the base
estimator** so every turn is corrected without needing a pure-class sample.
Keep the existing single EWMA as a **residual** correction for model /
provider drift that no fixed divisor absorbs.

Multi-ratio learning against one observed total is underdetermined and is
**not** Stage A.

## 1. Goal

Make request token estimates accurate enough that the compaction trigger and
retention budget track the intended fraction of the model budget for both
prose-heavy and tool-heavy agent turns - without inventing three learned
ratios or a real tokenizer.

## 2. Verified baseline (re-read at Step 0)

Facts are code evidence, not the overview snapshot alone.

- `estimateTokens` is `max(1, len(s)/4)` for every string
  (`internal/provider/context.go`).
- `EstimateRequestCost` walks roles, content, names, tool-call fields,
  framing constants, and marshalled tool schemas; returns `(int, error)`.
- `EstimateMessageTokens` mirrors the per-message walk (no schemas, no
  request frame) and is the **only** production marginal cost for planner
  tail-fill (`planner.go` retain loop).
- `EstimateToolSchemaCost` exists but has **no production callers**; schema
  cost is paid only inside `EstimateRequestCost`.
- Single EWMA: `contextmgr.Calibration` (`Ratio`, `Samples`, `Alpha`);
  `applyCalibration` treats `ratio <= 0` as identity.
- Update site: `agent.Loop.requestStep` uses **reserve-free**
  `EstimatePromptCost` vs `TokenUsage.InputTokens` (pinned by
  `TestLoopCalibrationUsesReserveFreePromptEstimate`).
- Planner applies the scalar ratio to `before`, `after`, `calibratedCost`,
  overflow checks, and **incremental** unit costs.
- `Calibration` is session-RAM only (not in `FileSessionStore` meta).
- Typical agent mass order (structural): tool-result bodies ≫ schemas ≫
  tool args ≫ user prose, once tools run (uncapped tool results by default;
  full registry schemas every step).

## 3. The defect

`len/4` has different error by content kind:

| Kind | Heuristic vs real BPE-ish tokenizers |
|------|--------------------------------------|
| English prose | ~4 chars/token - often close |
| JSON tool args, schemas, grep/code tool bodies | punctuation/symbol dense - `len/4` **under**-counts |

A single EWMA ratio fits the *mix* of recent turns. When the mix shifts, the
global residual is wrong in a direction that depends on the shift, and the
compaction trigger drifts. Multi-ratio EWMA tried to learn that away from one
total; the simpler fix is to **stop using the same divisor for every class**.

## 4. Locked design

### 4.1 Content classes (two, not three)

| Class | Constant | Covers |
|-------|----------|--------|
| `ClassProse` | `charsPerTokenProse = 4` (today) | `RoleUser` / `RoleAssistant` / `RoleSystem` **Content**; `Role`; non-tool `Name` when present on non-tool messages |
| `ClassStructured` | `charsPerTokenStructured = 3` | `RoleTool` **Content** (tool results); all tool-call fields (`ID`, `Type`, `Function.Name`, `Arguments`); `ToolCallID`; marshalled `ToolSpec` JSON |

**Open decision §6.1 closed:** tool results are **structured**. They are the
dominant mass on tool-heavy turns and are the main reason a “prose” class
would train on JSON/code if left as Content-default.

**Why not a third Schema class for estimation?** Schemas are the same
JSON-ish tokenization as other structured text. A separate divisor without
measurement is speculative. Plan `05` may shrink schema mass; the structured
divisor still applies to whatever list is priced.

**Why 3 chars/token for structured?** Middle ground between prose (4) and
aggressive JSON (~2). It **raises** structured estimates (corrects under-
count) without claiming a measured tokenizer. Residual EWMA absorbs residual
bias. If Stage A telemetry shows structured still systematically low or high,
adjust the constant in one place - not a new learning system.

### 4.2 Exhaustive field → bucket map

Every addend in `EstimateRequestCost` / `EstimateMessageTokens` /
`EstimateToolSchemaCost` maps to exactly one place:

| Charged term | Bucket |
|--------------|--------|
| `requestFrameTokens` | Framing (fixed; not class-estimated) |
| `outputReserve` | Reserve (fixed; **never** in calibration denominator) |
| `messageFrameTokens` | Framing |
| `message.Role` | ClassProse via `estimateTokensClass(..., ClassProse)` |
| `message.Content` | ClassProse if role ≠ tool; ClassStructured if `RoleTool` |
| `message.Name` | ClassProse (short identifiers; same as today) |
| `message.ToolCallID` | ClassStructured |
| `toolFrameTokens` | Framing |
| `call.ID`, `call.Type`, `call.Function.Name`, `call.Function.Arguments` | ClassStructured |
| `schemaFrameTokens` | Framing |
| marshalled tool JSON | ClassStructured |

**Framing rule:** frame constants are fixed integers. They are **not** run
through `estimateTokens` and are **not** scaled by a class divisor. They
appear as their own addend in `Cost` so `Total()` cannot double-count them.

### 4.3 Estimator shape

```text
// conceptual - exact names may match package style

type TokenClass int
const (
  ClassProse TokenClass = iota
  ClassStructured
)

type Cost struct {
  Prose      int
  Structured int
  Framing    int // request + message + tool + schema frames
  Reserve    int // output reserve only; 0 for EstimatePromptCost
}

func (c Cost) Total() int { return c.Prose + c.Structured + c.Framing + c.Reserve }

func estimateTokensClass(s string, class TokenClass) int  // max(1, len/chars) for non-empty
func EstimateRequestCostBreakdown(...) (Cost, error)
func EstimateMessageCost(msg Message) Cost               // message frames + fields; no schema/reserve
func EstimateToolSchemaCostBreakdown(tools []ToolSpec) (Cost, error)
```

**Compatibility wrappers (must remain):**

```text
EstimateRequestCost(...) (int, error)  = breakdown.Total()
EstimatePromptCost(...) (int, error)   = EstimateRequestCost(..., 0)
EstimateMessageTokens(msg) int         = EstimateMessageCost(msg).Total()
EstimateToolSchemaCost(...) (int, error)
RequestTokens(request) (int, error)
```

All wrappers share the **same field walk** as the breakdown functions (no
re-aggregation of concatenated strings before `estimateTokensClass`). That is
how INV-CE-04-A survives min-1 per field.

### 4.4 Calibration (unchanged shape)

- `Calibration` stays one EWMA (`Ratio`, `Samples`, `Alpha`).
- `Update(estimated, reported int)` stays; `estimated` is still reserve-free
  prompt total from the **new** estimator.
- `PlanInput.CalibrationRatio` / `PrepareInput.CalibrationRatio` stay
  `float64`.
- `applyCalibration` stays scalar; still applied on every planner path that
  applies it today (`before`, `after`, `calibratedCost`, overflow,
  incremental unit costs).

Class-aware estimation changes the **denominator quality**; residual EWMA
does not need to learn class structure.

### 4.5 Planner incremental path

No multi-ratio apply. `EstimateMessageTokens` / `EstimateMessageCost` must use
the **same** class routing as the full request walk so that:

```text
sum_i EstimateMessageTokens(msgs[i]) + requestFrame + schemaCost
  == EstimatePromptCost(msgs, tools)   // framing accounting as today
```

Exact equality of “sum of message costs + schema + request frame” must be
pinned by test (today’s framing layout: request frame once; per-message and
per-call frames inside message cost; schema separate).

### 4.6 Telemetry (Stage A, minimal)

Keep `TokenUsageEvent.CalibrationRatio` as the residual EWMA ratio (unchanged
JSON field meaning).

**Add** optional diagnostic fields only if they stay backward-compatible for
consumers that ignore unknown JSON (bus is internal):

- `estimated_prose`, `estimated_structured`, `estimated_framing` (ints), **or**
- a single log/debug path in tests without event schema churn.

**Preference for Stage A:** avoid event schema churn unless needed for an
integration assertion. Prefer unit-level breakdown tests + one helper used by
`requestStep` only if a follow-on needs live shares. Document measured shares
as a **post-ship** observation task, not a gate for Stage A merge.

### 4.7 Explicit non-goals of Stage A

- Multiple EWMA ratios / dominance thresholds.
- Changing `PlanInput` ratio type.
- Persisting calibration to disk.
- Real tokenizer / tiktoken / model-specific tables.
- Fixing uncalibrated `promptBudgetErrorWithTools` (pre-existing; residual
  risk).

## 5. Invariants

- **INV-CE-04-A.** With the new divisors, golden totals for a **prose-only**
  fixture (no tools, no tool roles) equal the pre-change `EstimateRequestCost`
  for the same messages (structured mass = 0). Structured-bearing fixtures
  **intentionally** change totals upward relative to HEAD.
- **INV-CE-04-B.** `Cost.Total()` equals the sum of Prose + Structured +
  Framing + Reserve with no double count; proved by table tests over every
  field the estimator charges.
- **INV-CE-04-C.** Same field walk: no “concatenate then estimate once” that
  would change min-1 behaviour vs per-field estimation.
- **INV-CE-04-D.** Calibration denominator remains reserve-free
  (`EstimatePromptCost`), not `RequestTokens`.
- **INV-CE-04-E.** Program INV-CE-A: any content priced for admission still
  goes through the same `EstimatePromptCost` / planner cost functions; no
  parallel shadow estimate.
- **INV-CE-04-F.** Zero-value `Calibration` and `CalibrationRatio == 0` still
  mean no residual correction (`applyCalibration` identity).

## 6. Closed decisions (were § open)

| # | Decision | Lock |
|---|----------|------|
| 1 | Tool results class | **ClassStructured** (not prose). |
| 2 | 70% dominance | **N/A** - multi-ratio deferred. |
| 3 | Complexity before `05` | **Justified:** structured divisor helps args, results, and schemas; `05` only changes *how many* schemas are priced, not the pricing rule. |
| 4 | Multi-ratio EWMA | **Deferred** to §10; not Stage A. |
| 5 | Structured chars/token | **3** (named constant `charsPerTokenStructured`). Adjust only with residual telemetry or tokenizer evidence. |
| 6 | `EstimateRequestCost` API | **Additive breakdown**; keep `(int, error)` wrappers. |
| 7 | Event multi-ratio fields | **Not required** for Stage A. |

## 7. Delivery slices (implementation)

### Slice 1 - Class-aware estimate + Cost breakdown (behaviour change for structured)

1. Introduce `TokenClass`, `charsPerToken*`, `estimateTokensClass`, `Cost`.
2. Refactor `EstimateRequestCost` / `EstimateMessageTokens` /
   `EstimateToolSchemaCost` to share one walk; wrappers return `Total()`.
3. Export breakdown helpers used by tests (and optionally by the loop later).
4. **No** calibration API change.

### Slice 2 - Golden + planner integration

1. Prose-only goldens: identical to HEAD totals.
2. Structured fixtures: tool args JSON, RoleTool bodies, schemas - totals
   strictly ≥ HEAD (and > HEAD when structured non-empty after min-1).
3. Planner tests that use absolute budgets may need re-baselining where
   fixtures include tools/tool results (update expected trigger behaviour,
   do not weaken assertions into no-ops).
4. Calibration tests still pass (denominator changes with estimator; ratios
   still form as reported/estimated).

### Slice 3 - Residual risk documentation only (optional code)

1. Comment near `promptBudgetErrorWithTools` that it remains uncalibrated
   residual path (or small follow-up to apply `Calibration.Ratio` if a
   one-file fix is clean - **not required** to close 04).

## 8. Required tests

### Provider (`internal/provider`)

- `estimateTokensClass` table: empty → 0; short non-empty → min 1; prose vs
  structured differ for same string length when `len >= 4`.
- Prose-only request golden: `EstimateRequestCost` equals recorded HEAD
  values for fixed messages (lock numbers in test).
- Structured tool-result message: Content on `RoleTool` uses structured
  divisor; same bytes on `RoleUser` use prose divisor - costs differ.
- Sum-of-parts: `Cost` fields sum to `Total()` for a table covering every
  charged field (including frames and schemas).
- `EstimateMessageTokens` consistency with message portion of
  `EstimateRequestCost` (same messages, no tools): message totals +
  `requestFrameTokens` match full cost.
- Marshal error paths unchanged for bad tool specs.

### Contextmgr / agent

- Existing calibration EWMA tests still pass.
- `TestLoopCalibrationUsesReserveFreePromptEstimate` still holds under new
  estimates.
- Planner: prose-heavy history still compactable; a tool-result-heavy
  fixture crosses trigger earlier than the same **byte** history would under
  pure `len/4` (proves Stage A moves the needle on tool-heavy mass).
- Cold residual: `CalibrationRatio` 0 still identity.

### Non-goals for tests

- Synthetic “ClassToolArgs dominates at 70%” multi-ratio tests (deferred).
- Real provider tokenizer comparison (optional manual; not CI).

## 9. API surface (exact targets)

| Symbol | Package | Change |
|--------|---------|--------|
| `estimateTokens` | provider | Prefer thin wrapper to `estimateTokensClass(..., ClassProse)` **or** delete if fully replaced; no dual heuristics. |
| `estimateTokensClass` | provider | **New** unexported or exported as needed by tests. |
| `Cost` | provider | **New** value type + `Total()`. |
| `EstimateRequestCostBreakdown` | provider | **New** |
| `EstimateMessageCost` | provider | **New** (or keep tokens helper only if breakdown stays internal - prefer exported for planner tests). |
| `EstimateRequestCost` / `EstimatePromptCost` / `EstimateMessageTokens` / `EstimateToolSchemaCost` / `RequestTokens` | provider | Behaviour change for structured fields; signatures unchanged. |
| `Calibration`, `applyCalibration` | contextmgr | **Unchanged** signatures. |
| `PlanInput.CalibrationRatio` | contextmgr | **Unchanged**. |

### Files expected to change

| File | Why |
|------|-----|
| `internal/provider/context.go` | Class-aware walk + `Cost` |
| `internal/provider/context_test.go` | Goldens, class routing, sum-of-parts |
| `internal/contextmgr/planner_test.go` | Re-baseline any absolute costs that include tools/tool results |
| `internal/contextmgr/planner_elision_test.go` / related | Only if fixtures assert absolute token counts with tool bodies |
| `internal/agent/loop_calibration_test.go` | Only if numeric wants depend on old structured estimates |

No change required to: session persistence, events sealed constructors
(unless optional telemetry), tool packages, durable contextstate.

## 10. Deferred: multi-ratio Stage B (not this delivery)

Revisit **only if** after Stage A ships, residual EWMA still shows large
mix-shift error (prose-only residual ≪ tool-heavy residual for the same
model). Then design, as a new plan revision:

1. Measured class-share histograms from real sessions.
2. Closed-form residual update with bias bound at purity τ.
3. Multi-ratio apply on **all** planner paths including incremental units.
4. `adoptCalibration` merge policy.
5. Event fields for per-class ratios.

Until that evidence exists, multi-ratio is **speculative generality**.

## 11. Out of scope

- Real tokenizer replacement.
- Per-provider / per-model divisor tables (beyond residual EWMA).
- Least-squares multi-class attribution.
- Schema gating (`05`) - independent; this plan prices whatever tools list it
  is given.
- Token-capped recent tail (`06`) and other program members.

## 12. Plan scorecard (Step 0 lock)

| Criterion | Result |
|-----------|--------|
| Compiles as a design against current APIs | PASS |
| No package dependency cycles | PASS |
| No breaking exported cost signatures | PASS |
| Testable in isolation (provider unit tests first) | PASS |
| Backward-compatible residual calibration | PASS |
| Every new function has a named test scenario | PASS |
| Behaviour change for structured content is intentional and tested | PASS |
| Tool-result class closed | PASS |
| Multi-ratio complexity deferred with trigger | PASS |
| Rollback criterion defined | PASS |

## 13. Rollback criterion

Kill or revert Stage A if:

- Prose-only estimates diverge from HEAD without structured content (INV-CE-04-A fail), or
- Compaction thrash appears on pure-chat sessions (structured divisor leaking into prose), or
- Calibration denominator includes output reserve again, or
- Cost double-counts frames vs class subtotals.

## 14. Micro-task waves (Step 1 draft for implementers)

Wave rules: 1 file per task; test before prod; reviewer every 2-3 prod tasks.

| ID | Wave | Type | File | Work | Verify |
|----|------|------|------|------|--------|
| t1 | 1 | test | `internal/provider/context_test.go` | RED: class routing + prose golden + structured RoleTool differ | `go test -run 'TestEstimate.*Class\|TestCost' ./internal/provider/` fail |
| t2 | 1 | prod | `internal/provider/context.go` | GREEN: `Cost`, `estimateTokensClass`, refactor walks | same tests pass |
| t3 | 2 | test | `internal/provider/context_test.go` | Sum-of-parts + message/request consistency + min-1 fields | pass |
| t4 | 2 | prod | `internal/provider/context.go` | Fix any equality gaps from t3 | pass |
| t5 | 3 | test | `internal/contextmgr/planner_test.go` | Tool-result-heavy trigger moves vs prose-equivalent bytes; fix broken absolutes | pass |
| t6 | 3 | review | - | Read provider+planner diffs for double-count, RoleTool routing, reserve-free cal | PASS/FAIL note |
| t7 | 4 | test | `internal/agent/loop_calibration_test.go` (+ others if red) | Reserve-free + wire ratio still green | `go test ./internal/agent/ -run Calibration` |
| t8 | 4 | verify | - | `go test ./internal/provider/ ./internal/contextmgr/ ./internal/agent/ ./internal/chat/` and race on contextmgr | all pass |

**Context scope per task:** ≤5 files (provider pair; planner tests; agent cal tests).

## 15. Residual risk

- Structured divisor `3` is a reasoned constant, not a fit to production
  tokenizers; residual EWMA is the safety net.
- `promptBudgetErrorWithTools` remains uncalibrated (pre-existing INV-CE-A
  soft spot when PreparationManager is nil).
- Planner/integration tests with hard-coded token budgets that include tool
  bodies will need careful re-baseline - risk of silent assertion weakening
  if implementers only “make green.”
- No live mass histogram yet; Stage B stay deferred until measured.

## 16. Completion report template (after implement)

- Outcome
- Changed files
- Verification commands + results (never claim pass without run)
- Residual risk / Stage B trigger status
