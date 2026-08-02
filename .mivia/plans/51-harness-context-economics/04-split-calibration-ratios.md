# 51.04 - Split the token calibration ratio by content class

**Status:** DESIGN - ADLC Step 0 not run.
**Date:** 2026-08-02
**Part of:** program `51` (`00-overview.md`).
**Depends on:** nothing.
**Blast radius:** LOW - estimation accuracy only. No message shape, no
storage, no tool surface.

## 1. Goal

Make the calibrated token estimate accurate per *kind* of content instead of
on average, so the compaction trigger fires at the intended fraction of the
budget for tool-heavy turns as well as prose-heavy ones.

## 2. Verified baseline

- `estimateTokens` is `len(s)/4` for every string, whatever it contains
  (`internal/provider/context.go:11`).
- `EstimateRequestCost` sums that estimator over message content, role,
  name, tool-call ID, tool-call arguments, and marshalled tool schemas, plus
  fixed framing constants (`internal/provider/context.go`).
- `Calibration` maintains a single EWMA `Ratio` from observed provider usage
  and `applyCalibration` scales the whole estimate by it
  (`internal/contextmgr/calibration.go`, `internal/contextmgr/planner.go:84`).
- The ratio is threaded through `PlanInput.CalibrationRatio` and applied in
  `Plan`, `retainMessages`, `calibratedCost`, and `promptOverflow`.

## 3. The defect

`len/4` has very different error on different content:

- English prose is close to 4 chars/token.
- Minified JSON tool arguments and JSON Schema are punctuation-dense; real
  tokenizers split them far more finely, so `len/4` **under**-counts.
- Long identifier-heavy source excerpts sit somewhere between.

A single EWMA ratio fits the *mix* of the recent turns. When the mix shifts -
a burst of tool calls after a stretch of discussion - the ratio is wrong in a
direction that depends on the shift, and the compaction trigger drifts off
80%. The correction is real (it is better than no calibration) but it is
averaging away a systematic, classifiable error.

## 4. Design

### 4.1 Classes

Three classes, chosen because they are structurally distinguishable at the
call site with no content inspection:

| Class | Covers |
|-------|--------|
| `ClassProse` | `Message.Content`, `Role`, `Name` |
| `ClassToolArgs` | `ToolCall.Function.Arguments`, `ToolCallID`, tool-call framing |
| `ClassSchema` | marshalled `ToolSpec` JSON |

Tool *results* are `Message.Content` on a `RoleTool` message. Whether they
belong in `ClassProse` or a fourth class is an **open decision** (§6.1).

### 4.2 Estimator shape

`EstimateRequestCost` already walks these fields separately. It gains a
per-class accumulator and returns a `Cost` value carrying the three subtotals
plus the framing constants, with a `Total()` for existing callers. The
`len/4` heuristic itself does not change in this plan.

### 4.3 Calibration

`Calibration` holds three EWMA ratios instead of one. Attribution of an
observed provider total across three classes is the hard part; the honest
method is least-squares over recent samples, which is more machinery than
this plan should carry. The tractable method:

- Update a class ratio only from turns where that class dominates the
  estimate (share above a fixed threshold, e.g. 70%), attributing the whole
  residual to the dominant class.
- Turns with no dominant class update nothing.
- Each class keeps the existing `Samples` counter and the existing
  "unratioed until `Samples > 0`" behaviour, so a cold start is identical to
  today.

This converges slower than a single ratio but never attributes a residual to
a class that did not cause it.

### 4.4 Compatibility

`PlanInput.CalibrationRatio float64` is replaced by a `CalibrationRatios`
value with the three fields. Zero value means 1.0 for every class, which is
exactly today's "no correction" behaviour, so every existing test that
supplies no ratio keeps its meaning.

## 5. Invariants

- **INV-CE-04-A.** With all three ratios at 1.0, every cost this plan
  computes equals the pre-change estimate exactly. The refactor is
  behaviour-neutral before calibration data exists.
- **INV-CE-04-B.** A class ratio is only ever updated from a sample where
  that class dominated the estimate. No cross-attribution.
- **INV-CE-04-C.** `Cost.Total()` is the sum of the class subtotals and the
  framing constants, with no double counting - proved by a test that sums
  the parts against a direct total over the same input.

## 6. Open decisions for Step 0

1. **Are tool results their own class?** They are `Message.Content` like
   prose, but they are frequently structured output (grep hits, JSON) whose
   tokenization resembles `ClassToolArgs`. A fourth class is more accurate
   and slows convergence further. Recommendation: start with three, measure,
   and only split if the residual on tool-result-dominated turns is
   materially worse than on prose-dominated ones.
2. **Is the 70% dominance threshold defensible, or is it a magic number?**
   It needs either a derivation or a config knob, and a config knob for an
   estimator internal is probably worse than a documented constant.
3. **Does this justify its own complexity before `05` lands?** `05`
   (schema gating) removes most of `ClassSchema`'s mass from the request
   entirely. If schemas stop dominating, one of the three classes may be
   near-irrelevant.

## 7. Delivery slices

1. `Cost` value + per-class accumulation in `EstimateRequestCost`, all
   callers on `Total()`. No calibration change. Behaviour-identical.
2. `CalibrationRatios` type, dominance-gated EWMA updates, `PlanInput`
   migration.
3. Telemetry: record per-class estimate vs observed total so the dominance
   threshold can be evaluated against real sessions.

## 8. Required tests

- Behaviour-neutrality: golden costs for a fixed message set, ratios at 1.0,
  identical to the pre-change values.
- Sum-of-parts equals total, over a table of messages exercising every
  field the estimator charges for.
- A dominated-class sample updates exactly one ratio; a mixed sample updates
  none.
- Planner: a tool-args-dominated history with a low `ClassToolArgs` ratio
  triggers compaction at the same *calibrated* fraction as a prose history
  with a low `ClassProse` ratio.
- Cold start: zero-value ratios reproduce the existing planner tests.

## 9. Out of scope

- Replacing `len/4` with a real tokenizer.
- Per-provider or per-model ratios.
- Least-squares attribution across classes.
