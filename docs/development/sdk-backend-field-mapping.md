# SDK backend field mapping (B.2 #8 inner-loop swap)

The CLI `agent.Options` field set splits into three groups on the SDK
path. The groups are deliberately explicit so a caller knows exactly
what behavior changes. Since the SDK convergence flip, `Backend: ""`
(the default) and `Backend: "sdk"` both take the SDK path; the only
production caller that still depends on a legacy-only option
(`internal/chat/session.go` for surface rotation) sets
`Backend: "legacy"` explicitly. `internal/subagents/multi_step.go`
no longer pins legacy: the parent-mailbox drain rides the SDK's
Steer injector, and `SoftInterruptCooldown` plus partial-text
survival are recorded as accepted semantic gaps rather than pins.

## 1. Carried today

The SDK path consumes these directly:

| CLI field | SDK carrier | Notes |
|---|---|---|
| `Model` | `Options.Model` | pass-through |
| `Temperature` | `Request.Temperature` | translator in `agentloop_completer.go` |
| `MaxTokens` | completer turn defaults | the SDK loop's request never sets it; `mergeTurnDefaults` injects `Options.MaxTokens` per call |
| `Temperature` | completer turn defaults | same carrier as `MaxTokens` |
| `RequestTimeout` | completer turn defaults | fills `Request.Timeout` when the SDK left it zero |
| `DisableProviderReplay` | completer turn defaults | OR-merged into the request |
| `Reasoning` | completer turn defaults | the wrapper injects `Options.Reasoning` level and dialect; the SDK's 4-value `ReasoningEffort` vocabulary is never used |
| `Dispatcher` (tool hooks, gate, dedup) | `applyDispatcherShim` | every converted tool's Run routes through `Options.Dispatcher.Invoke` with `Kind: tool`, `Step` stamped from the shared per-Chat counter, and `SkipDedup` from the tool's capability class, mirroring `loop_tool_exec.go` |
| `MaxToolResultChars` | `applyDispatcherShim` | the shim applies the legacy pass-1 cap (`effectiveResultCap` + `CapWithSpoolRef`) and records pass-1 parts so the turn shaper's degrade reports the ORIGINAL total and pages the original bytes |
| `ToolTimeout` | `applyDispatcherShim` | per-call timeout with the tool's own larger request timeout honored, clamped to the deadline |
| `LastFinishReason` | completer `onFinish` callback | the wrapper reports each response's finish reason onto `Loop.LastFinishReason`; the truncation-aware corrective turn keys on it |
| `MaxSteps > 0` | `Options.MaxIterations` | clamped by `MaxTurns` when set; `MaxSteps <= 0` caps at the adapter default 25 (see §2) |
| `SessionID` | `Options.SessionID` | required when Usage is set |
| `AdvertisedToolSpecs` | turn-state advertised snapshot + completer override | the snapshot seeds `sdkTurnState.advertised` (request 0, the legacy `initialToolSpecs` contract) and each surface rotation's non-nil `ToolSpecs` replaces it; the completer's `applyAdvertisedTools` REPLACES the wire request's registry-derived tools with the live snapshot, so deferred tools outside the registry reach the wire from request 0 (see `internal/agent/sdk_advertised.go` for the recovery-request safety note: the SDK's Window-gated recovery never fires because the host wires no Window) |
| `MaxToolCallsPerBatch` | `Options.MaxCallsPerTurn` | positive only |
| `MaxConcurrentTools` | `Options.MaxConcurrentTools` | parallel dispatch worker pool; call context threads tool call IDs so per-call pass-1 parts and event synthesis do not race |
| `BatchResultBudgetBytes > 0` | host-side shaping wrapper | `applyTurnShaping` charges one shared per-turn counter and applies the legacy degrade tiers (fit / re-cut with notice / notice alone); the SDK's omit-on-budget `TurnResultBudget` stays unset |
| `MaxContextTokens` | host-side compaction | `prepareSDKHistory` calls `PreparationManager.Prepare`; SDK's `Window` stays nil |
| `SummaryConfig.Summarizer` | host-side inject | `prepareSDKHistory` runs `Loop.injectSummary` once pre-run; SDK sees the summary frame |
| `StagedToolMessage` / `UnadmittedToolHandler` | per-call wrapper | `sdkadapter.ConvertToolRegistryWithAdmission` on registered tools; denial renders as `RoleTool` |
| `RefOnlyTools` / `RemainderSpool` | per-call wrapper | `applyRefOnlyShim` calls the CLI `*remainder.Spool` directly |
| `OnEvent` / `EventBus` | `Options.Bus` | via `bridgeAgentLoopEvents` (3 kinds mapped) |
| `UsageWriter` | `Options.Audit` | via `bridgeUsageAudit` |
| `FinalWriter` / `RequireFinalText` | post-run finalize | via `finalizeSDKTurn` |
| `MaxTurns` | clamps `MaxIterations` | pre-default so 0 means "any limit wins" |
| `DeadlineAt` | narrows ctx | pre-Run |
| `InterruptCh` | steer bridge | one-shot goroutine; gated on `MailboxPendingInterrupt` when that predicate is set (a bare `InterruptCh` with no mailbox gate is an explicit interrupt) |
| `MailboxPending` | steer bridge | watchdog poller; continuous across repeated steers, exits on a run-scoped done channel closed in `RunAgentLoopOnce`'s defer |
| `MailboxPendingInterrupt` | steer bridge | strict signal-branch poller; continuous across repeated steers, exits on the run-scoped done channel |
| `BeforeStep` | Steer injector | `RunAgentLoopOnce` installs `opts.BeforeStep` as `Steer.SetInjector`; the SDK drains it at the top of every iteration (BEFORE the MaxIterations check, matching `context.go:15-19`) and at every steered-stop downgrade point. A non-empty return appends to history and the run CONTINUES; an empty return keeps existing Trigger semantics. The `ackTriggered` at the downgrade point is load-bearing: without it the next iteration's Chat call would arm a still-triggered Steer and cancel instantly. |
| `Surface` | `Options.Surface` bridge | `bridgeSDKBridgeSurface` maps the CLI per-step hook onto the SDK's own per-iteration `Options.Surface` (consulted from the second iteration on, the legacy skip-step-1 rule). The rotation's `Dispatcher`/`RemainderSpool` land in the run's `sdkTurnState` (per-call shim reads), the `Registry` rebuilds through the ONE construction path `buildSDKToolRegistry` (shared shaping counter), and `ToolSpecs` re-advertise as SDK definitions. A conversion failure at a rotation records into the turn state and fails the run after `RunSteerable` returns. Accepted gap: step 1 advertises registry-derived definitions (no `Description`), so the pinned snapshot with descriptions applies from step 2 on; and a call to an advertised-but-unregistered name degrades to the SDK's `[tool-error]` `RoleTool` body instead of the legacy `UnadmittedToolHandler` auto-stage denial (the handler still fires for registered tools). |
| `BatchResultBudgetBytes < 0` | host-side derivation | `applyTurnShaping` now resolves the negative form via `derivedBatchBudget(opts.MaxContextTokens)` (shared with `effectiveBatchBudget` in `shape_batch.go:493`); constants (`bytesPerToken`, `derivedBudgetShare`, `derivedBatchBudgetFloorBytes`, `maxDerivableTokens`) cannot drift between the legacy and SDK paths. |
| turn history | `Result.History` | `runOnceSDK` writes the SDK history back onto `Loop.Messages`, including the turn's assistant and tool messages, and falls back to the last assistant text when the final step produced none. The legacy `lastText` contract at `loop.go:143-179` is mirrored on every graceful-cancel path: the steered-stop branch returns the in-scope partial via `sdkSteeredStopPartial`, the cancel branch (errors.Is `context.Canceled`/`context.DeadlineExceeded`) and the graceful-empty fallback both walk history with the same `sdkCurrentTurnStart` Content-match helper. Streamed bytes inside an in-flight cancel are still lost — the SDK cancels `Completer.Chat` wholesale on Trigger — but assistant messages appended to history before the cancel point survive. |

## 2. Accepted semantic gaps

The set value passes through (or the knob is simply absent), but the
SDK interprets it differently or not at all. A `Backend: "sdk"`
caller accepts the difference.
- **`SoftInterruptCooldown`** — the SDK's `bridgeSteerSignals` now
  caps `Steer.Trigger` fires with a shared `cooldownUntil atomic.Int64`
  over all three sites (the `InterruptCh` one-shot, the strict
  `MailboxPendingInterrupt` poller, the loose `MailboxPending`
  poller), matching the legacy's `Loop.steerCooldownOK` semantics.
  Two divergences remain: the gate is intra-`RunAgentLoopOnce` only
  (a local atomic.Int64 here, not the legacy's cross-call
  `Loop.softInterruptAt`), so a multi-turn SDK session resets the
  gate at every turn; and the gate's effective minimum spacing is
  bounded by the strict poller's `pollInterval` (250ms default when
  `WatchdogInterval` is zero), so a sub-`pollInterval` cooldown is
  not portable to the SDK path. The subagent pre-blob wiring sets
  `SoftInterruptCooldown = 5s` so the effective floor is the poller
  interval, not the caller's cooldown.
- **`MaxSteps <= 0`** — the legacy loop treats 0 as unbounded; the
  SDK's `Validate` requires a positive `MaxIterations`, so the adapter
  substitutes `defaultSDKMaxIterations = 25`. An unbounded run is
  impossible on the SDK path.
- **`BatchResultBudgetBytes`** — the positive form is carried by the
  host-side turn shaping wrapper (§1), which degrades with an honest
  notice like the legacy batch shaper. The negative form is also
  carried now: `applyTurnShaping` resolves via the shared
  `derivedBatchBudget`. One divergence remains: the D8 per-batch
  status line (composed into the LAST degraded result) has no
  sequential analogue and is omitted; per-call budget charging
  happens in call order (the SDK executes sequentially) rather than
  after the whole batch resolves.
- **Same-batch dedup** — the legacy dispatcher collapses identical
  read-class calls within one batch; the SDK executes every call.
- **Conclude-steer nudges** — the legacy loop injects a conclude
  message when budgets or the deadline are nearly exhausted; the SDK
  path has no equivalent injection.
- **Soft-interrupted partial text survives as final reply** — the
  legacy `steerInterruptOutcome` carries the streamed partial from
  an interrupted Completer call into the post-steer step's `lastText`
  (and into `Loop.Messages` via `recordInterruptedPartial`), so a
  steered stop can deliver that partial as the turn's final reply.
  The SDK cancels `Completer.Chat` wholesale on Trigger, so any
  streamed partial the Completer had already produced is dropped —
  the SDK's `Result.Final` on a steered stop is the zero value, by
  design. The drain-after-Steer injector downgrade keeps the run
  going instead of stopping, which is the correct SDK behaviour for
  a non-empty mailbox drain; for an empty drain the SDK still stops
  with `Stop == StopSteered` and `Final` empty, where the legacy path
  would have surfaced the partial. The dispatcher at
  `loop_dispatch.go:77` (delegating to `sdkSteeredStopPartial`)
  walks `res.History` and returns the most recent in-scope assistant
  text along with `errSteerInterrupt`, mirroring the legacy
  `lastText` contract for what was already appended to history
  before the cancel. Bytes the Completer had streamed inside the
  canceled call itself are still lost.
- **Prompt-too-long retry** — the legacy loop retries once with a
  compacted prompt on `ErrPromptTooLong`; the SDK path fails the turn.
- **Malformed tool-call repair** — the legacy path synthesizes IDs for
  unidentified calls and records malformed arguments verbatim as a
  paired tool result; the SDK path hard-fails schema-invalid calls.
- **Event surface** — the legacy `EventThinking`, heartbeat, cache
  usage, calibration, and tool-input-preview events have no SDK
  bridge; `EventStep`/`EventToolStart`/`EventToolEnd` carry the SDK's
  string payload, not the legacy typed details.

## 3. Fail-closed today

Every CLI Options field whose semantics the SDK path cannot carry
returns an error naming the field from `buildAgentLoopOptions`, so a
caller learns the boundary at the call instead of silently losing
behavior.

- **`Surface` rotation** — carried since the SDK grew its own
  per-iteration `Options.Surface`; see the §1 carrier row. (The
  historical fail-closed note — the SDK read `Options.Tools` once at
  `agentloop.New` — no longer applies.)
- **`WorkLimits` token reservations** — `MaxPromptTokens`,
  `MaxOutputTokens`, `MaxOutputPerCall`, and `MaxToolCalls`. The CLI's
  reservation model (refuse to start a turn that would exceed; refund
  on completion, tracked in `internal/runtime/work_limits.go`) is
  fundamentally different from the SDK's in-loop cumulative
  `MaxTotalTokens` cap; mapping one onto the other would change the
  contract. Callers keep tracking reservations outside the loop and
  gating the call. `MaxTurns` and `DeadlineAt` are carried (§1).
  `PreserveWorkLimits` passes: it only preserves those four
  reservation counters, and each of those already fails closed by
  name, so with them all zero the flag is inert and rejecting it
  alone would fail-close a no-op.

## See also

- `internal/agent/agentloop_adapter.go` — the mapping code itself.
- `internal/agent/loop_dispatch.go` — the dispatcher that selects
  between legacy and SDK backends and writes the SDK history back.
- `internal/agent/refonly_shim.go` — the ref-only shim and its known
  divergences from the legacy `refOnlyTier`.
