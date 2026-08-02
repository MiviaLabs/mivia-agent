# Plan 37 - Implementation overview

Parent plan: `../37-reasoning-effort.md`

Status: blocked by the parent plan's 2026-08-02 Step 0 re-audit. Do not execute
these phases; they are retained only as superseded design context.

Goal: carry one model-scoped, provider-neutral reasoning level through config,
model binding, direct chat, and agent-loop requests, mapping it to the provider's
wire dialect while omitting sampling parameters when the dialect requires it.

Required order:

1. Complete the normal ADLC Step 0 challenge before production implementation.
2. Land phases 01-02 before config or request propagation.
3. Land phases 03-04 in order; phase 04 depends on the completed request seam.
4. Land phase 05 only after focused tests and full verification pass.

Non-goals: Responses API support, verbosity, reasoning-content history changes,
per-model capability matrices, or undocumented z.ai disable workarounds.

Cross-phase invariant: an unset level produces the pre-change request shape;
an active level produces dialect fields and suppresses sampling only when the
dialect says so; a nil dialect never emits reasoning fields.
