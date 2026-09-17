---
id: s5_deferred_wrapper_unification_is_a_verified_no_op_residual_is_s6_layer_2_literal_dedup_94494a9c9fec7b1cc6acd39008a05358
title: 'S5 deferred-wrapper unification is a verified no-op; residual is S6 (layer-2 literal dedup)'
content: 'Improvement-plan slice S5 (extract wrapDeferredTool) was closed as a no-op at e3b49107 after planner + plan-reviewer verification: the deferred-admission wrapper chain already shares one implementation (RunUnadmittedTool rebuilds the identical stack at sdk_dispatcher_shim.go:447). The real residual duplication is registry-build-side: refOnlyShim and turnShapeWrapper composite literals each built t'
importance: medium
x-scope: project
x-verdict: good
tags: [s5, s6, deferred-tools, wrapper-chain, conformance, no-op-verdict, drift]
updated: 2026-09-17
---

# S5 deferred-wrapper unification is a verified no-op; residual is S6 (layer-2 literal dedup)

## Summary
Improvement-plan slice S5 (extract wrapDeferredTool) was closed as a no-op at e3b49107 after planner + plan-reviewer verification: the deferred-admission wrapper chain already shares one implementation (RunUnadmittedTool rebuilds the identical stack at sdk_dispatcher_shim.go:447). The real residual duplication is registry-build-side: refOnlyShim and turnShapeWrapper composite literals each built t

## What worked
- Contract-first replan worked: enumerate the wrapper contract table before judging the extraction, so the no-op verdict is falsifiable row by row.
- Adversarial reviewer loop caught 5+ wrong line citations in round 1 (stale ~8-line offset read) and forced the 13/12/nine count reconciliation and S6 registration; round 2 approved with one off-by-two nit (row 13 armExplicitCancel call is :172, not :170).

## What did not work
- Round-1 planner citations were derived from a stale read; every file:line in a plan whose dispute mechanism is "check this line" must be re-read at the pinned HEAD before shipping.

## Why
Future sessions must not re-derive S5 or re-propose wrapDeferredTool: the premise (second hand-maintained wrapper chain) is false at HEAD. Layer 1 (dispatcherShim) is one type constructed at :392 (registry) and :436 (deferred); layers 3/4 use the shared wrapRefOnly/wrapTurnShaping constructors; layer 2's approval asymmetry is a DECLARED divergence in .mivia/policy/tool-execution-conformance.json (unset-approval-policy-agrees/all), not drift. Counts that matter: 16 shim invariants in Run/composeRunOutput, 15 TestEveryPath* conformance contracts; the "nine contracts" wording in the test header is DC-35 history, not a live count.

## References
- internal/agent/sdk_dispatcher_shim.go
- internal/agent/refonly_shim.go
- internal/agent/sdk_shaping.go
- internal/cli/chat/tool_execution_conformance_test.go
- .mivia/policy/tool-execution-conformance.json
- .agents/memories/two-paths-execute-a-tool-call.md
