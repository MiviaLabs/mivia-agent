# SDK backend field mapping (B.2 #8 inner-loop swap)

The CLI `agent.Options` field set splits into two groups on the SDK
path (carried, and accepted semantic gaps below) - a third group,
fields the SDK path could not carry at all, existed historically but
is now empty; see §3. Since the SDK convergence flip, `Backend: ""`
(the default) and `Backend: "sdk"` both take the SDK path.
`internal/chat/session.go` sets `Backend: "sdk"` explicitly and relies
on surface rotation bridging (§1's `Surface` row) rather than pinning
legacy. No production caller sets `Backend: "legacy"` today; only
test call sites still pin it to exercise `Loop.Run` directly.

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
| `Reasoning` | completer turn defaults | the wrapper injects `Options.Reasoning` level and dialect; the SDK's 4-value `ReasoningEffort` vocabulary is never used; reasoning output is bridged via `emitReasoning` emitting `EventThinking` to `OnEvent`/`EventBus` |
| `Dispatcher` (tool hooks, gate, dedup) | `applyDispatcherShim` | every converted tool's Run routes through `Options.Dispatcher.Invoke` with `Kind: tool`, `Step` stamped from the shared per-Chat counter, and `SkipDedup` from the tool's capability class, mirroring `loop_tool_exec.go` |
| `MaxToolResultChars` | `applyDispatcherShim` | the shim applies the legacy pass-1 cap (`effectiveResultCap` + `CapWithSpoolRef`) and records pass-1 parts so the turn shaper's degrade reports the ORIGINAL total and pages the original bytes |
| `ToolTimeout` | `applyDispatcherShim` | per-call timeout with the tool's own larger request timeout honored, clamped to the deadline |
| `ToolRunTimeout` | `buildSDKToolRegistry` -> `sdktools.WithDefaultRunTimeout` | SDK registry-wide run backstop for tools with no declared `Capability.Timeout` (`[tools] tool_run_timeout_seconds`); `<= 0` maps to `TimeoutNone` (no cap) so the SDK's hardcoded 10-minute default can never undercut the dispatcher's own per-call deadlines; a declared `Capability.Timeout` is published through `ProfiledTool` on every registered wrapper layer, with the dispatcher shim mapping declared `> 0` to `TimeoutNone` because it arms that budget (plus per-call `timeout_seconds` raises) as a real deadline itself |
| `LastFinishReason` | completer `onFinish` callback | the wrapper reports each response's finish reason onto `Loop.LastFinishReason`; the truncation-aware corrective turn keys on it |
| `MaxSteps` | `Options.MaxIterations` | passed through; SDK's `unboundedOrSet` maps `0` → `math.MaxInt32` so `MaxSteps = 0` (unbounded) reaches the SDK unchanged. `MaxTurns` (when > 0) clamps to the smaller of the two positive values, and stays applied over an unset `MaxSteps` (see §2) |
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
| `WorkLimits.MaxPromptTokens` / `MaxOutputTokens` / `MaxOutputPerCall` (Item 8) | `Options.WorkBudget` | `newSDKWorkBudget`/`newSDKWorkBudgetHook` (`agentloop_budget.go`) bridge the SDK's Reserve-before-call/Refund-after-outcome hook onto the SAME `workLimitMeter` the legacy loop uses (`work_limits.go`); no policy is forked, only the call points differ |
| `WorkLimits.MaxToolCalls` | `Options.ToolBudget` | `newSDKToolBudget` (`agentloop_toolbudget.go`) bridges the SDK's per-turn Reserve hook onto the SAME `workLimitMeter`'s `reserveToolBatch`. Accepted approximation: the SDK calls Reserve with the RAW `resp.ToolCalls` count, before per-call malformed-argument filtering or in-turn dedup (both happen later, inside the SDK's own `runToolCalls`), where the legacy `processToolCalls` charged only the validated, batch-cap-clamped count. This can only exhaust the cumulative cap SOONER than exact accounting would, never later. |
| `PreserveWorkLimits` | shared meter reset rule | `newSDKWorkBudget` applies the same reset rule `runOnceLegacy` did: a nil meter, a non-preserved run, or changed `WorkLimits` rebuilds the meter; the flag now means the same thing on both backends, covering all four reservation fields above |

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
- **`MaxSteps <= 0`** — the legacy loop treats 0 as unbounded, and
  the SDK does too: `agentloop.New` runs `unboundedOrSet(opts.MaxIterations)`,
  which maps `0` to `math.MaxInt32` so the run loop's `iterations >=
  l.maxIterations` check at `agentloop/run.go:89` never fires. The
  adapter passes `opts.MaxSteps` straight through
  (`internal/agent/agentloop_adapter.go`); the historical
  `defaultSDKMaxIterations = 25` substitution was removed because it
  capped the SDK path at a value the legacy loop never honored,
  breaking parity. An unbounded run is now possible on the SDK path.
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
- **A `Surface` rotation that changes `Registry` without also changing
  `Dispatcher`** — no real caller does this (every production Surface
  hook pairs them, e.g. `internal/chat/session_turn_surface.go`'s
  `resolveTurnExecutionSurface`), but the two backends behave
  differently if one ever did: the legacy `executeToolsParallel`
  re-scopes a fresh dispatcher per batch over whatever registry is
  current when `Options.Dispatcher` is nil, so a Registry-only
  rotation still dispatches against the new registry; the SDK path's
  dispatcher (`ensureSDKDispatcher`) is scoped once at run start over
  the ORIGINAL registry and does not follow a Registry-only rotation.
  Always rotate `Dispatcher` alongside `Registry`.
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
- **Event surface** — the legacy heartbeat, cache
  usage, calibration, and tool-input-preview events have no SDK
  bridge; `EventThinking` is bridged via `emitReasoning`, while
  `EventStep`/`EventToolStart`/`EventToolEnd` carry the SDK's string
  payload, not the legacy typed details.

## 3. Fail-closed today

Every CLI Options field whose semantics the SDK path cannot carry
returns an error naming the field from `buildAgentLoopOptions`, so a
caller learns the boundary at the call instead of silently losing
behavior. As of the `ToolBudget` bridge, nothing in this list remains:
every `Options` field the legacy loop honored is now carried on the
SDK path, historical entries kept below for the record.

- **`Surface` rotation** — carried since the SDK grew its own
  per-iteration `Options.Surface`; see the §1 carrier row. (The
  historical fail-closed note — the SDK read `Options.Tools` once at
  `agentloop.New` — no longer applies.)
- **`WorkLimits` token and tool-call reservations** — `MaxPromptTokens`,
  `MaxOutputTokens`, `MaxOutputPerCall`, and `MaxToolCalls` are all
  carried now; see the §1 rows for `Options.WorkBudget` and
  `Options.ToolBudget`. (The historical fail-closed note — that the
  CLI's refuse/refund reservation model was "fundamentally different"
  from the SDK's cumulative cap — no longer applies: `WorkBudget` and
  `ToolBudget` are host-callable hooks with no SDK-owned policy, so the
  legacy `workLimitMeter` methods run unchanged on either backend.)
  `MaxTurns` and `DeadlineAt` are carried too (§1). `PreserveWorkLimits`
  passes: it preserves the same meter's cumulative counters across a
  corrective re-entry, on either backend.

## 4. Known bugs requiring follow-up

Unlike §2 (deliberately accepted differences), these are real defects
found while removing the legacy engine and forcing its pinning tests
onto the SDK path. They already affect production today (the SDK path
has been the default since the convergence flip; nothing here is a
regression from removing legacy) - they were simply never exercised by
a test that could catch them until the legacy path stopped being an
option.

- ~~**Summary injection is not ephemeral on the SDK path**~~ — **fixed.**
  The legacy `injectSummary` builds a request-only clone that never
  touches `l.Messages`; on the SDK path, `sdkPrepareTrim`'s `Trim`
  closure (`sdk_prepare.go`) returned the summary-injected slice, and
  the SDK's own `run.go` (`*history = trimmed`, `agentloop/run.go:142`)
  adopted that return value AS the run's carried history, which became
  `Result.History` and got written back onto `l.Messages` wholesale
  (`runOnceSDK`'s write-back rule, §1's "turn history" row) - the
  injected summary frame leaked into durable history, and
  `commitContextTurn` then appended a SECOND copy on top (it assumed
  the first would have vanished). On a multi-step turn each further
  step's `Trim` call re-injected on top of the still-leaked prior copy
  with no dedup, so an N-step post-compaction turn could carry N+1
  duplicate copies of the same summary text before the turn even ended.
  Fixed at the two seams that actually needed it, per the fix sketch
  this section used to carry: `InjectSummaryMessage`
  (`summary_inject.go`) now drops any message already carrying
  `SummaryMessageName` before appending the fresh one, making injection
  idempotent per request instead of accumulating across steps; and
  `writeBackSDKHistory` (`loop_dispatch.go`) now runs
  `stripInjectedSummaryFrames` on the converted `res.History` before it
  becomes `l.Messages`, restoring "the loop itself never writes it into
  l.Messages" at the one seam the SDK's `Trim` contract violated,
  without reaching into the external SDK package. Confirmed by
  `TestSummaryInjectionDoesNotTouchDurableState` and
  `TestSummaryInjectionOneSummarizePerCompactionAcrossSteps`
  (`internal/agent`), both un-skipped and passing.
  `TestSummaryInjectionToolFactsReachLaterRequest` stays skipped for a
  separate, newly-found gap this fix uncovered rather than caused:
  `contextmgr.TurnState.AddChangedSurface` has no production caller
  anywhere outside its own file, so a real tool call never lands in a
  compaction summary's `ChangedSurfaces` - wiring that is a
  feature-sized change (threading `TurnState` into the tool-dispatch
  path), tracked separately, not part of this fix.
- **A steer that fires during a prompt-too-long retry does not resume
  the run afterward** — `runSDKPromptTooLongRecoverable` implements
  the retry as a second, independent `sdkagentloop.New` +
  `RunSteerable` call. A steer that cancels that retry's in-flight
  call produces a graceful `StopSteered` result with a nil error;
  `runSDKPromptTooLongRecoverable`'s retry-condition
  (`err == nil || ...`) treats a nil error as "done" and returns
  immediately - there is no continuation to a further call the way the
  legacy single continuous step-loop provided (a soft interrupt there
  just continues the SAME loop to its next iteration). Confirmed by
  `TestLoopSteerDuringPromptTooLongRetryInterruptsTheRetry`
  (`internal/agent/loop_retry_steer_test.go`, currently skipped
  pending a fix - it hangs 5s waiting for a "recovered" third call that
  never happens). A related, already-fixed goroutine race in the same
  area: `bridgeSteerSignals` (`agentloop_steer.go`) now takes a
  `sync.WaitGroup` so `runSDKSteerable` waits for its bridge goroutines
  to fully exit before returning, closing a window where a stale
  goroutine from the first (failed) attempt could win the race for an
  interrupt signal meant for the retry.
- **`WorkBudget`'s refund does not distinguish a steer-canceled call
  from a call that failed for its own reason** — the SDK's `Refund`
  contract (`mivia-ai-sdk/agentloop/budget.go`) only receives
  `(ctx, req, used Usage)`; a zero `Usage` means "never consumed,"
  refunded unconditionally by `sdkWorkBudget.refund`
  (`agentloop_budget.go`). The legacy `workLimitMeter.refundProvider`
  was called ONLY on the steer-interrupt path
  (`steerInterruptOutcome`), not on a plain provider error, on the
  reasoning that a call that failed for its own reason still consumed
  real work. This widens (never narrows) a finite
  `WorkLimits.MaxOutputTokens`/`MaxPromptTokens` budget after an
  ordinary provider error - a leniency bug, not a safety one. Confirmed
  by `TestProviderErrorKeepsWorkLimitReservation`
  (`loop_steer_worklimit_test.go`, currently skipped pending a fix).
  Fix sketch: thread a per-call "was Trigger fired for THIS call" flag
  from `agentloop_steer.go`'s `fireSteer` through `sdkTurnState` for
  `agentloop_budget.go`'s `refund` to consult.

## See also

- `internal/agent/agentloop_adapter.go` — the mapping code itself.
- `internal/agent/loop_dispatch.go` — the dispatcher that selects
  between legacy and SDK backends and writes the SDK history back.
- `internal/agent/refonly_shim.go` — the ref-only shim and its known
  divergences from the legacy `refOnlyTier`.
