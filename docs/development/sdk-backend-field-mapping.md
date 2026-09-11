# SDK backend field mapping

The SDK loop is the only agent-loop backend: `Loop.runOnce` always
drives the SDK-backed loop (`internal/agent/loop_dispatch.go`), and
the former legacy engine and its `Options.Backend` field are gone.
The CLI `agent.Options` field set maps onto the SDK path in two
groups: carried (§1), and accepted semantic gaps (§2). No field fails
closed; see §3.

## 1. Carried fields

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
| `AdvertisedToolSpecs` | turn-state advertised snapshot + completer override | the snapshot seeds `sdkTurnState.advertised` (request 0, the legacy `initialToolSpecs` contract) and each surface rotation's non-nil `ToolSpecs` replaces it; the completer's `applyAdvertisedTools` REPLACES the wire request's registry-derived tools with the live snapshot, so deferred tools outside the registry reach the wire from request 0 (see `internal/agent/sdk_advertised.go` for the recovery-request safety note: the SDK's Window-gated recovery never fires on turns with a `PreparationManager`, or without a wired summarizer, because those turns keep `Window` nil; on manager-less turns with a summarizer wired and a positive `MaxContextTokens` (or, with `Options.PreferSDKCompaction` set, on manager-wired turns too), `adoptSDKCompaction` does wire a Window directly (§5)) |
| `MaxToolCallsPerBatch` | `Options.MaxCallsPerTurn` | positive only |
| `MaxConcurrentTools` | `Options.MaxConcurrentTools` | parallel dispatch worker pool; call context threads tool call IDs so per-call pass-1 parts and event synthesis do not race |
| `BatchResultBudgetBytes > 0` | host-side shaping wrapper | `applyTurnShaping` charges one shared per-turn counter and applies the legacy degrade tiers (fit / re-cut with notice / notice alone); the SDK's omit-on-budget behavior is never engaged - the host wrapper is the sole shaper |
| `MaxContextTokens` | host-side compaction, or the SDK Window | with a `PreparationManager` and `Options.PreferSDKCompaction` unset (every production turn): `sdkPrepareTrim` runs it per iteration and the SDK's `Window` stays nil; with a `PreparationManager` and `PreferSDKCompaction` set, or with no `PreparationManager` and a summarizer wired: `adoptSDKCompaction` owns the Window and the host Trim stands down (§5) |
| `SummaryConfig.Summarizer` | host-side inject | `prepareSDKHistory` runs `Loop.injectSummary` once pre-run; SDK sees the summary frame |
| `StagedToolMessage` / `UnadmittedToolHandler` | per-call wrapper | `sdkadapter.ConvertToolRegistryWithAdmission` on registered tools; denial renders as `RoleTool` |
| `RefOnlyTools` / `RemainderSpool` | per-call wrapper | `applyRefOnlyShim` calls the CLI `*remainder.Spool` directly |
| `OnEvent` / `EventBus` | `Options.Bus` | via `bridgeAgentLoopEvents` (3 kinds mapped) |
| `UsageWriter` | completer `onUsage` | consumed host-side per Chat (`l.emitTurnUsage`); `Options.Audit` is a different row - it feeds the `sdkloop-` JSONL dump (§5), and no Audit bridge exists |
| `FinalWriter` / `RequireFinalText` | post-run finalize | via `finalizeSDKTurn` |
| `MaxTurns` | clamps `MaxIterations` | pre-default so 0 means "any limit wins" |
| `DeadlineAt` | narrows ctx | pre-Run |
| `InterruptCh` | steer bridge | one-shot goroutine; gated on `MailboxPendingInterrupt` when that predicate is set (a bare `InterruptCh` with no mailbox gate is an explicit interrupt) |
| `MailboxPending` | steer bridge | watchdog poller; continuous across repeated steers, exits on a run-scoped done channel closed in `RunAgentLoopOnce`'s defer |
| `MailboxPendingInterrupt` | steer bridge | strict signal-branch poller; continuous across repeated steers, exits on the run-scoped done channel |
| `BeforeStep` | Steer injector | `RunAgentLoopOnce` installs `opts.BeforeStep` as `Steer.SetInjector`; the SDK drains it at the top of every iteration (BEFORE the MaxIterations check, matching `context.go:15-19`) and at every steered-stop downgrade point. A non-empty return appends to history and the run CONTINUES; an empty return keeps existing Trigger semantics. The `ackTriggered` at the downgrade point is load-bearing: without it the next iteration's Chat call would arm a still-triggered Steer and cancel instantly. |
| `Surface` | `Options.Surface` bridge | `bridgeSDKBridgeSurface` maps the CLI per-step hook onto the SDK's own per-iteration `Options.Surface` (consulted from the second iteration on, the legacy skip-step-1 rule). The rotation's `Dispatcher`/`RemainderSpool` land in the run's `sdkTurnState` (per-call shim reads), the `Registry` rebuilds through the ONE construction path `buildSDKToolRegistry` (shared shaping counter), and `ToolSpecs` re-advertise as SDK definitions. A conversion failure at a rotation records into the turn state and fails the run after `RunSteerable` returns. Accepted gap: step 1 advertises registry-derived definitions (no `Description`), so the pinned snapshot with descriptions applies from step 2 on; and a call to an advertised-but-unregistered name degrades to the SDK's `[tool-error]` `RoleTool` body instead of the legacy `UnadmittedToolHandler` auto-stage denial (the handler still fires for registered tools). |
| `BatchResultBudgetBytes < 0` | host-side derivation | `applyTurnShaping` resolves the negative form via `derivedBatchBudget(opts.MaxContextTokens)` (shared with `effectiveBatchBudget` in `shape_batch.go:493`); constants (`bytesPerToken`, `derivedBudgetShare`, `derivedBatchBudgetFloorBytes`, `maxDerivableTokens`) cannot drift between the legacy and SDK paths. |
| turn history | `Result.History` | `runOnceSDK` writes the SDK history back onto `Loop.Messages`, including the turn's assistant and tool messages, and falls back to the last assistant text when the final step produced none. The legacy `lastText` contract at `loop.go:143-179` is mirrored on every graceful-cancel path: the steered-stop branch returns the in-scope partial via `sdkSteeredStopPartial`, the cancel branch (errors.Is `context.Canceled`/`context.DeadlineExceeded`) and the graceful-empty fallback both walk history with the same `sdkCurrentTurnStart` Content-match helper. Streamed bytes inside an in-flight cancel are still lost — the SDK cancels `Completer.Chat` wholesale on Trigger — but assistant messages appended to history before the cancel point survive. |
| `WorkLimits.MaxPromptTokens` / `MaxOutputTokens` / `MaxOutputPerCall` | `Options.WorkBudget` | `newSDKWorkBudget`/`newSDKWorkBudgetHook` (`agentloop_budget.go`) bridge the SDK's Reserve-before-call/Refund-after-outcome hook onto the SAME `workLimitMeter` the legacy loop uses (`work_limits.go`); no policy is forked, only the call points differ |
| `WorkLimits.MaxToolCalls` | `Options.ToolBudget` | `newSDKToolBudget` (`agentloop_toolbudget.go`) bridges the SDK's per-turn Reserve hook onto the SAME `workLimitMeter`'s `reserveToolBatch`. Accepted approximation: the SDK calls Reserve with the RAW `resp.ToolCalls` count, before per-call malformed-argument filtering or in-turn dedup (both happen later, inside the SDK's own `runToolCalls`), where the legacy `processToolCalls` charged only the validated, batch-cap-clamped count. This can only exhaust the cumulative cap SOONER than exact accounting would, never later. |
| `PreserveWorkLimits` | shared meter reset rule | `newSDKWorkBudget` applies the legacy reset rule: a nil meter, a non-preserved run, or changed `WorkLimits` rebuilds the meter; the flag means the same thing the legacy loop's reset rule meant, covering all four reservation fields above |

## 2. Accepted semantic gaps

The set value passes through (or the knob is simply absent), but the
SDK interprets it differently or not at all.
- **`SoftInterruptCooldown`** — the SDK's `bridgeSteerSignals` caps `Steer.Trigger` fires with a shared `cooldownUntil atomic.Int64`
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
  (`internal/agent/agentloop_adapter.go`); the SDK path honors an
  unbounded run.
- **`BatchResultBudgetBytes`** — the positive form is carried by the
  host-side turn shaping wrapper (§1), which degrades with an honest
  notice like the legacy batch shaper. The negative form is also
  carried: `applyTurnShaping` resolves via the shared
  `derivedBatchBudget`. One divergence remains: the D8 per-batch
  status line (composed into the LAST degraded result) has no
  sequential analogue and is omitted; per-call budget charging
  happens in call order (the SDK executes sequentially) rather than
  after the whole batch resolves.
- **Same-batch dedup** — the legacy dispatcher collapses identical
  read-class calls within one batch via `SkipDedup` on the dispatcher
  shim (`internal/agent/sdk_dispatcher_shim.go`, keyed off each tool's
  capability class). The SDK offers an equivalent knob,
  `agentloop.Options.DedupWithinTurn`, but the adapter leaves it
  unset; the host's own `SkipDedup` mechanism already covers the same
  case.
- **A `Surface` rotation that changes `Registry` without also changing
  `Dispatcher`** — no caller does this (every Surface
  hook pairs them, e.g. `internal/chat`'s
  `resolveTurnExecutionSurface`), but the SDK path would misdispatch
  if one ever did: the dispatcher (`ensureSDKDispatcher`) is scoped
  once at run start over the ORIGINAL registry and does not follow a
  Registry-only rotation. Always rotate `Dispatcher` alongside
  `Registry`.
- **Conclude-steer nudges** — the legacy loop injects a conclude
  message when budgets or the deadline are nearly exhausted. The SDK
  has an equivalent field group, `agentloop.Options.Conclude`
  (`Margin`, `Deadline`, `Notice`) plus `DefaultConcludeNotice`, but
  the adapter does not set it; the host's wrap-up budget instead rides
  `Options.ContinueOnStop` (`internal/agent/continue_on_stop.go`).
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
  `loop_dispatch.go` (delegating to `sdkSteeredStopPartial`)
  walks `res.History` and returns the most recent in-scope assistant
  text along with `errSteerInterrupt`, mirroring the legacy
  `lastText` contract for what was already appended to history
  before the cancel. Bytes the Completer had streamed inside the
  canceled call itself are still lost.
- **Prompt-too-long retry** — carried: the SDK path retries once with
  a compacted prompt (`runSDKPromptTooLongRecoverable`,
  `internal/agent/agentloop_recovery.go`). One gap remains: a steer
  that fires during that retry does not resume the run (§4).
- **Malformed tool-call repair** — the legacy path synthesizes IDs for
  unidentified calls and records malformed arguments verbatim as a
  paired tool result; the SDK path hard-fails schema-invalid calls.
- **Event surface** — the legacy heartbeat, cache
  usage, calibration, and tool-input-preview events have no SDK
  bridge; `EventThinking` is bridged via `emitReasoning`, while
  `EventStep`/`EventToolStart`/`EventToolEnd` carry the SDK's string
  payload, not the legacy typed details.

## 3. Fail-closed fields

Every CLI `Options` field whose semantics the SDK path cannot carry
returns an error naming the field from `buildAgentLoopOptions`
(`internal/agent/agentloop_adapter.go`), so a caller learns the
boundary at the call instead of silently losing behavior. The set is
empty: every field the legacy loop honored is carried on the SDK path
(§1) or documented as a semantic gap (§2).

## 4. Known limitations

Unlike §2 (deliberately accepted differences), these are defects on
the SDK path.

- **Tool facts never reach a compaction summary's changed-surfaces
  list** — `contextmgr.TurnState.AddChangedSurface`
  (`internal/contextmgr/turnstate.go`) has no production caller, so a
  real tool call never lands in a compaction summary's
  `ChangedSurfaces`. `TestSummaryInjectionToolFactsReachLaterRequest`
  (`internal/agent/summary_inject_test.go`) pins this gap and is
  skipped.
- **A steer that fires during a prompt-too-long retry does not resume
  the run afterward** — `runSDKPromptTooLongRecoverable`
  (`internal/agent/agentloop_recovery.go`) implements
  the retry as a second, independent `sdkagentloop.New` +
  `RunSteerable` call. A steer that cancels that retry's in-flight
  call produces a graceful `StopSteered` result with a nil error;
  `runSDKPromptTooLongRecoverable`'s retry condition
  (`err == nil || ...`) treats a nil error as "done" and returns
  immediately - there is no continuation to a further call, so the
  steered stop ends the turn instead of continuing after the steer
  drains. `TestLoopSteerDuringPromptTooLongRetryInterruptsTheRetry`
  (`internal/agent/loop_retry_steer_test.go`) pins this gap and is
  skipped.
- **`WorkBudget`'s refund does not distinguish a steer-canceled call
  from a call that failed for its own reason** — the SDK's `Refund`
  contract only receives `(ctx, req, used Usage)`; a zero `Usage`
  means "never consumed," refunded unconditionally by
  `sdkWorkBudget.refund` (`internal/agent/agentloop_budget.go`). The
  legacy `workLimitMeter.refundProvider` runs only on the
  steer-interrupt path, not on a plain provider error, on the
  reasoning that a call that failed for its own reason still consumed
  real work. This widens (never narrows) a finite
  `WorkLimits.MaxOutputTokens`/`MaxPromptTokens` budget after an
  ordinary provider error - a leniency bug, not a safety one.
  `TestProviderErrorKeepsWorkLimitReservation`
  (`internal/agent/loop_steer_worklimit_test.go`) pins this gap and
  is skipped.

## 5. Adopted loop knobs

The adapter projection (`internal/agent/agentloop_adoption.go`) sets
these SDK loop knobs; `TestBuildAgentLoopOptions_AdoptionRows`
(`internal/agent/agentloop_adapter_test.go`) pins each row:

- **Usage (+ SessionID)** — the run carries an SDK session
  accumulator. Only set with a SessionID; the SDK rejects Usage
  without one. The durable UsageWriter path stays the system of
  record.
- **Budget** — a generous runaway bound (64 MiB / 4096 events).
  Deriving it from MaxContextTokens double-bounded history below the
  operator's prompt ceiling: the SDK budget counts history bytes
  while the ceiling counts prompt tokens. The host's context pruning
  and batch shaping stay the binding budgets.
- **Bounds.MaxTotalTokens** — deliberately stays unset: the SDK
  bound counts cumulative billed tokens across the whole run, which
  re-bills history every iteration, so the per-prompt context
  ceiling is the wrong scale and would hard-fail healthy long turns.
- **Bounds.MaxConsecutiveToolFailures** — a hard stop at the same
  count the reminder path's failure-spiral breaker fires at. At
  runtime the SDK surfaces the trip as the graceful
  `StopRepeatedToolFailures` stop; `sdkRepeatedToolFailureError`
  converts it into a failed turn carrying the loop-breaker wording.
- **Tracer** — every SDK-path run gets a span tracer parked on the
  turn state. `recordSDKTurnTelemetry` (`agentloop_adoption.go`) is
  the reader: it appends the run's span count and names to the
  operator audit directory (`EnvProviderAuditDir`) alongside the
  usage snapshot below, so the row is read at least once instead of
  accumulating write-only. No dedicated chatsync/session sink reads
  spans.
- **Usage** — every SessionID-bearing SDK-path run gets a per-session
  usage accumulator parked on the turn state. The host's durable
  UsageWriter path (`l.emitTurnUsage`) stays the system of record;
  `recordSDKTurnTelemetry` reads the accumulator's `Total` once per
  turn into the same audit-directory line as Tracer, so the
  accumulator is not write-only; the accumulator feeds only the
  audit line.
- **Audit** — the SDK loop's Audit hook feeds a `sdkloop-` JSONL
  stream in the operator audit directory, next to (not replacing) the
  completer-seam wire dump; the dump needs the effective post-merge
  request, which the SDK audit record does not carry.
- **HeartbeatInterval + Bus** — a 15s cadence next to the bridged
  bus; both SDK tick kinds bridge onto the legacy EventHeartbeat
  "working" surface.
- **Window/Summarizer/Calibrated** — adopted whenever
  `MaxContextTokens` and `SummaryConfig.Summarizer` are both set: the
  triple sizes from `MaxContextTokens` (`TriggerPercent: 100`,
  `TargetTokens: MaxContextTokens/2`, an exact 80%/50% trigger/target
  of `MaxContextTokens` - a bare 80/50 `TriggerPercent`/`TargetPercent`
  pair would price at an effective 64%/40% instead, since the SDK's
  percents price against `Budget`, not `MaxTokens`), and the calibrated
  estimator is `agentLoopCompleter.EstimateTokens`, running the host's
  own `EstimatePromptCost` semantics. The summarizer is
  `sdkSummarizerAdapter` (`internal/agent/sdk_summarizer_adapter.go`),
  which calls the host's own governed `contextmgr.Summarizer`
  (redaction, policy binding, evidence tracking) - not
  `agentLoopCompleter`, and not the SDK's generic
  `plan.NewSummarizer`-over-completer fallback either.
  **Opt-in with a PreparationManager wired**: every production call
  site wires one
  (`contextmgr.StructuralPreparationManager{}`, set in
  `internal/composition/session.go` and
  `internal/clichat/context_setup_session.go`), and
  `sdkCompactionAdopted` requires `Options.PreferSDKCompaction` in
  that case (default false, so no production call site adopts this
  row until it sets that field). With no PreparationManager wired,
  the row still adopts automatically; that case is reachable only
  under test. With a PreparationManager wired
  and `PreferSDKCompaction` unset (every production turn), Trim
  IS the host's per-request preparation pipeline (repair, prune,
  inject), unaffected. An adopted turn with a PreparationManager runs
  `confirmSDKCompaction` (`internal/agent/agentloop_adoption.go`) to
  reconcile the SDK's mid-run compaction with the durable
  checkpoint.
- **Capability mirror** — `AnthropicCompleter`
  (`internal/provider/anthropic_sdk_capabilities.go`) implements
  the SDK's `ContextAccountant`, `ReasoningPolicy`, and
  `TokenEstimator` capabilities directly, so one concrete provider
  carries them without the `agentLoopCompleter` wrapper. The other
  builtin providers (`openai_compat.go` and its per-vendor wrappers)
  do not implement these capabilities; the `agentLoopCompleter`
  wrapper supplies them (`internal/agent/agentloop_completer.go`).
  Separately, privilege enforcement lives in `internal/tools`' own
  `PrivilegedTool` filter (`internal/tools/tools.go`).

## See also

- `internal/agent/agentloop_adapter.go` — the mapping code itself.
- `internal/agent/loop_dispatch.go` — writes the SDK history back and
  maps steered stops onto `errSteerInterrupt`.
- `internal/agent/refonly_shim.go` — the ref-only shim and its known
  divergences from the legacy `refOnlyTier`.
