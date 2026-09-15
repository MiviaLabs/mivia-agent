# SDK Gap Ledger (Stage 0)

Companion to [public-sdk-integration-cleanup-plan.md](public-sdk-integration-cleanup-plan.md).
Per-file verdicts for `internal/agent`'s SDK-bridge layer against
`mivia-ai-sdk v0.6.0` (HEAD `5d73261`). Every SDK fact below was read from the
checkout; host facts cite `internal/agent` directly.

Verdict semantics:

- **adopted** — the file already rides the SDK capability correctly; no
  duplicate logic exists and nothing further to do.
- **keep** — Mivia policy (numbers, thresholds, UX, persistence, wire
  vocabulary). Must stay host-side regardless of SDK capability.
- **candidate** — further adoption is possible; not urgent; carries a stated
  risk.
- **gap** — adoption is blocked by a concrete SDK behavioural divergence,
  named below. Nothing moves until the divergence is resolved or accepted in
  writing.

## Ledger

| File | Verdict | Basis |
|---|---|---|
| agentloop_run.go | keep | WorkLimits deadline narrowing, preflight ordering, empty-response retry bound (2), finalize/FinalWriter contract. No SDK counterpart. |
| agentloop_adapter.go | keep | Options projection is host contract: MaxTurns clamp, shim chain, Surface-rotation restatement around SDK's unconditional Advertised replace. |
| agentloop_adoption.go | keep | Compaction wired manually for exact 80%/50% host-style numbers; SDK's raw pair prices against Budget and diverges effectively (64%/40%). ApplySDKTrim correctly honours Validate's Trim/Window exclusivity (options.go:331-343). |
| agentloop_budget.go | **gap** | Host `refund` has a zero-Usage full-refund branch (agentloop_budget.go:112-120); SDK `settleWork` never calls Refund on zero Usage (SDK budget.go:102-110), so a cancelled/timeout call permanently consumes its reservation — diverging from the documented steer-cancel refund contract. Also `clampedMaxTokens` is once-per-turn (accepted gap). |
| agentloop_toolbudget.go | adopted | Reserve-only delegate onto the SDK ToolBudget; raw pre-filter count is the documented conservative approximation. No Refund exists to diverge from. |
| agentloop_recovery.go | keep | 16K target, MaxContextTokens/4 clamp, model-visible notice, single retry — legacy policy numbers the SDK recovery does not replicate. |
| agentloop_completer.go | keep | `translatePromptTooLong` is the seam SDK Window recovery depends on; ChatTurn translation and usage callback are host contract. |
| agentloop_convert.go | adopted | Pure shape conversion, no Mivia policy embedded. `!Reported → zero Usage` matches SDK "no observation" semantics. Deletable only if the SDK ever ships a converter — not worth a release on its own. |
| sdk_dispatcher_shim.go | keep | Approval decision lives in the shared `sdkadapter.DecideApproval` (standing decisions, resource keys, policy-in-context, EmitPending UI); SDK Scope covers only registry-resolved calls and has no deferred-path representation. Host timeout arms richer per-call deadlines (`timeout_seconds` raises, "exit=timeout" envelope) than a static `ExecutionProfile`. |
| sdk_shaping.go | keep | Three-tier degrade contract, index-ordered cross-parallel charging, per-tool caps, degrade floors. No SDK shaping primitive exists on this path. |
| refonly_shim.go | keep | SDK `memory.SpoolTool` exists but requires `WithPrincipal` ctx no SDK call site attaches, and cannot express the floor gate, name list, ephemeral-skip, notice wording, or `remainder.Spool` mint. Principal/expiry guarantees are unused host properties. |
| sdk_prepare.go | keep | Trim closure bridging host PreparationManager. Window deliberately never wired (Validate excludes Trim+Window). |
| sdk_advertised.go | candidate | SDK `Extensions.Surface` replaces Advertised from iteration 2 and is already wired; host still pins the request-0 union separately. Foldable, but the pinned-union contract and the recovery structural gate make the host carrier safer. Low urgency. |
| agentloop_tool_error.go | adopted | Denial wording, staged-tool precedence, malformed-JSON fall-through already ride the SDK's `OnToolCallError` ErrorFunc — the correct seam. |
| agentloop_events.go | keep | Translation into the Mivia wire vocabulary (session stamping, attribution, failed-detail for vetoed calls). Nothing re-derives SDK-native semantics. |
| sdk_tool_events.go | keep | Legacy two-start/one-end wire shape, pinned by `cli/characterization_test.go`. 100% wire-vocabulary policy. |
| agentloop_steer.go | **gap** | SDK with an injector installed soft-continues *every* steered stop ("emptiness gates nothing", steer.go:74-82); Mivia expects a steered stop gated by the caller. Host cooldown is intra-turn only (accepted gap, agentloop_steer.go:36-40) and triggers are gated on `HasActiveCall()` to avoid the poison-arm loop. Gen-gated ack and reset-surviving injector are otherwise correctly adopted. |
| sdk_turn_state.go | keep | Run-scoped carrier (pass1Map, surface rotation, cancel registry). SDK Options/Extensions cover none of it. |
| sdk_summarizer_adapter.go (+memo) | keep | `agentloop.Summarizer` is a one-method interface (`plan.Summarizer` is the struct implementing it) — redaction/evidence capture cannot ride it, so the adapter is already the minimal seam. Memo is the smallest correct dedup. |
| internal/remainder/spool.go | keep | Correct mechanism/policy split: SDK `memory.Spool` is the mechanism; INV-AG-10/CE-07 invariants and durable cross-restart grants are irreducibly host policy. Mechanism swap possible later only as a wrapper preserving the invariants. |

## Real gaps (adoption blockers)

1. **WorkBudget zero-Usage refund** (`agentloop_budget.go`): SDK never refunds
   on zero Usage. Host's contract refunds the full reservation when a call was
   cancelled before consuming. Fix belongs in the SDK (`settleWork`) or the gap
   is accepted in writing with the reservation-leak documented.
2. **Steer soft-continue** (`agentloop_steer.go`): SDK injector semantics
   continue every steered stop; Mivia's mailbox model needs stop authority.
   Fix belongs in the SDK (an injector mode) or the divergence stays documented
   as it is today.

Both are SDK-repository candidates for the `release/v0.7.0` branch named in the
plan — each needs the plan's Decision Log entry before any SDK change.

## Reconciliation status

`docs/development/sdk-backend-field-mapping.md` predates v0.6.0 surface
verification; its accepted-gap list should be cross-checked against the two
gaps above and the once-per-turn `clampedMaxTokens` note. Not done in this
slice.

## Outstanding Stage 0 items

- Step 2: reconcile `sdk-backend-field-mapping.md` against v0.6.0 (above).
- Step 3: the three skipped defect tests (`summary_inject_test.go`,
  `loop_retry_steer_test.go`, `loop_steer_worklimit_test.go`) remain skipped;
  two of them pin exactly the steer/budget gaps recorded here. Fix or accept
  with an owner before Stage 1.
- Step 4: tool-call keying duplication inventory not yet measured; only
  relevant if a shared helper is later proposed.
