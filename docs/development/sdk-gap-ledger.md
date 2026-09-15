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
| agentloop_budget.go | **adopted** | SDK `release/v0.7.0` 9f9a6cc changed `WorkBudget.Refund` to carry the failure cause (breaking, api locks regenerated). Host 20a4ac5c adopts it: the meter refunds fully on cancellation and prompt-too-long (the recovery re-reserves, so keeping the failed attempt double-charges) and keeps the reservation consumed on ordinary provider errors (`TestProviderErrorKeepsWorkLimitReservation` unskipped and green). `clampedMaxTokens` stays once-per-turn (accepted gap). |
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
| agentloop_steer.go | **adopted** | SDK v0.7.0 (361af35) gates steered stops on `ContinueOnStop`; the host gate returns nil (host stop authority) and `runOnceSDK`'s bounded steered-continue re-runs the turn, draining the mailbox via the re-run's iteration-top injector. The shared cooldown window (`Loop.steerCooldownUntil`, reset at turn start) spans every bridge of the turn, restoring the intra-turn contract across re-runs. Triggers are gated on `HasActiveCall()` to avoid the poison-arm loop. Gen-gated ack and reset-surviving injector are otherwise correctly adopted. |
| sdk_turn_state.go | keep | Run-scoped carrier (pass1Map, surface rotation, cancel registry). SDK Options/Extensions cover none of it. |
| sdk_summarizer_adapter.go (+memo) | keep | `agentloop.Summarizer` is a one-method interface (`plan.Summarizer` is the struct implementing it) — redaction/evidence capture cannot ride it, so the adapter is already the minimal seam. Memo is the smallest correct dedup. |
| internal/remainder/spool.go | keep | Correct mechanism/policy split: SDK `memory.Spool` is the mechanism; INV-AG-10/CE-07 invariants and durable cross-restart grants are irreducibly host policy. Mechanism swap possible later only as a wrapper preserving the invariants. |

## Real gaps (adoption blockers)

1. **WorkBudget zero-Usage refund** (`agentloop_budget.go`): RESOLVED. SDK
   `release/v0.7.0` 9f9a6cc changed `WorkBudget.Refund` to carry the failure
   cause (breaking, api locks regenerated). Host 20a4ac5c adopts it: the
   meter refunds fully on cancellation and prompt-too-long (the recovery
   re-reserves, so keeping the failed attempt double-charges) and keeps the
   reservation consumed on ordinary provider errors.
   `TestProviderErrorKeepsWorkLimitReservation` is unskipped and green.
2. **Steer soft-continue — RESOLVED** (`agentloop_steer.go`): SDK
   `release/v0.7.0` 361af35 gates steered stops on `ContinueOnStop`
   (breaking behavior change for injector-only consumers, no signature
   change, api locks unchanged). The host gate returns nil — host stop
   authority — and `runOnceSDK`'s bounded steered-continue re-runs the
   turn on the carried history, draining the mailbox via the re-run's
   iteration-top injector. The shared cooldown window
   (`Loop.steerCooldownUntil`) spans every bridge of the turn.
   `TestLoopSteerDuringPromptTooLongRetryInterruptsTheRetry` is
   unskipped and green; exhaustion is pinned by
   `TestSteeredContinueExhaustsBudgetSurfacesInterrupt`.

Both real gaps are now closed; no SDK-repository candidate remains
open for the `release/v0.7.0` branch.

## Reconciliation status

`docs/development/sdk-backend-field-mapping.md` is reconciled against the
v0.6.0 surface (§1, §2, and §4 updated for Steer soft-continue, WorkBudget
zero-Usage refunds, and once-per-turn `clampedMaxTokens`).

## Outstanding Stage 0 items

- Step 2: done (this reconciliation, date-less).
- Step 3: one remaining skip with unchanged root cause: `summary_inject`
  (`ChangedSurfaces` never wired — host-fixable, follow-up slice). Two
  skips are gone: `loop_steer_worklimit` via 20a4ac5c, and
  `loop_retry_steer` is unskipped and green (Steer soft-continue gap
  resolved, SDK 361af35 + host steered-continue adoption).
- Step 4: done — SDK ToolCallKey exported (release/v0.7.0 b00f76e), four host copies retired (1f3e4cf8), parity pinned by TestToolCallKeyParityAgainstSDKVectors.

## Stage resolution

- **Stage 1 admission** — already adopted (`DecideApproval` + `OnToolCallError` are the correct seams; `ScopeOptions.Approve` rejected because it cannot represent standing decisions/resource keys/deferred path).
- **Stage 2 timeouts** — keep (host per-call deadlines with `timeout_seconds` raises and `exit=timeout` envelope exceed static `ExecutionProfile`).
- **Stage 3 spool** — keep (correct mechanism/policy split).
- **Stage 4 events** — keep (wire vocabulary).
- **Stage 5 layout** — no longer gap-blocked; awaits the Stage 5
  reassessment itself (coupling measurement and an extract-or-not
  verdict).
