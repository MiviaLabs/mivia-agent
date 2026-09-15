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
| agentloop_budget.go | **gap** | SDK `refundWork` (budget.go:75-82) refunds with zero Usage on failed calls; `settleWork` (budget.go:102-110) skips Refund on successful zero-Usage calls. Host `refund` (agentloop_budget.go:118-120) receives zero Usage on all failures, over-refunding ordinary provider errors (widens budget when it should keep reservation; pinned by skipped `TestProviderErrorKeepsWorkLimitReservation`). Also `clampedMaxTokens` is once-per-turn (accepted gap). |
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

1. **WorkBudget zero-Usage refund** (`agentloop_budget.go`): SDK `refundWork`
   refunds zero Usage on failed calls (including cancel/timeout). SDK
   `settleWork` skips Refund only on successful zero-Usage calls. The host's
   full-refund branch (`agentloop_budget.go:118-120`) therefore fires on all
   failures. This creates an over-refund divergence. An ordinary provider
   error is indistinguishable from a steer-canceled call. The budget widens
   when it should keep the reservation (pinned by skipped
   `TestProviderErrorKeepsWorkLimitReservation`). Fix belongs in the SDK
   or the divergence remains accepted and documented.
2. **Steer soft-continue** (`agentloop_steer.go`): SDK injector semantics
   continue every steered stop; Mivia's mailbox model needs stop authority.
   Fix belongs in the SDK (an injector mode) or the divergence stays documented
   as it is today.

Both are SDK-repository candidates for the `release/v0.7.0` branch named in the
plan — each needs the plan's Decision Log entry before any SDK change.

## Reconciliation status

`docs/development/sdk-backend-field-mapping.md` is reconciled against the
v0.6.0 surface (§1, §2, and §4 updated for Steer soft-continue, WorkBudget
zero-Usage refunds, and once-per-turn `clampedMaxTokens`).

## Outstanding Stage 0 items

- Step 2: done (this reconciliation, date-less).
- Step 3: done — all three skips re-verified failing with unchanged root causes: `summary_inject` (`ChangedSurfaces` never wired — host-fixable, follow-up slice), `loop_retry_steer` (pins Steer soft-continue gap), `loop_steer_worklimit` (pins WorkBudget zero-Usage refund gap).
- Step 4: done — SDK ToolCallKey exported (release/v0.7.0 b00f76e), four host copies retired (1f3e4cf8), parity pinned by TestToolCallKeyParityAgainstSDKVectors.

## Stage resolution

- **Stage 1 admission** — already adopted (`DecideApproval` + `OnToolCallError` are the correct seams; `ScopeOptions.Approve` rejected because it cannot represent standing decisions/resource keys/deferred path).
- **Stage 2 timeouts** — keep (host per-call deadlines with `timeout_seconds` raises and `exit=timeout` envelope exceed static `ExecutionProfile`).
- **Stage 3 spool** — keep (correct mechanism/policy split).
- **Stage 4 events** — keep (wire vocabulary).
- **Stage 5 layout** — blocked on the two SDK gaps.
