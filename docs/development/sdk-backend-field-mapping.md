# SDK backend field mapping (B.2 #8 inner-loop swap)

The CLI `agent.Options` field set splits into three groups on the SDK
path. The groups are deliberately explicit so a caller knows exactly
what behavior changes. Since the SDK convergence flip, `Backend: ""`
(the default) and `Backend: "sdk"` both take the SDK path; the two
production callers that still depend on legacy-only options
(`internal/chat/session.go` for surface rotation,
`internal/subagents/multi_step.go` for the BeforeStep mailbox drain)
set `Backend: "legacy"` explicitly.

## 1. Carried today

The SDK path consumes these directly:

| CLI field | SDK carrier | Notes |
|---|---|---|
| `Model` | `Options.Model` | pass-through |
| `Temperature` | `Request.Temperature` | translator in `agentloop_completer.go` |
| `MaxTokens` | `Request.MaxTokens` | translator |
| `MaxSteps > 0` | `Options.MaxIterations` | clamped by `MaxTurns` when set; `MaxSteps <= 0` caps at the adapter default 25 (see §2) |
| `SessionID` | `Options.SessionID` | required when Usage is set |
| `AdvertisedToolSpecs` | converted registry | via `sdkadapter.ConvertToolRegistry` |
| `Reasoning` | `Options.ReasoningEffort` | 7→4 mapping in `sdkadapter` |
| `MaxToolCallsPerBatch` | `Options.MaxCallsPerTurn` | positive only |
| `BatchResultBudgetBytes > 0` | `Options.TurnResultBudget` | literal bytes; omits rather than degrades (see §2) |
| `MaxContextTokens` | host-side compaction | `prepareSDKHistory` calls `PreparationManager.Prepare`; SDK's `Window` stays nil |
| `SummaryConfig.Summarizer` | host-side inject | `prepareSDKHistory` runs `Loop.injectSummary` once pre-run; SDK sees the summary frame |
| `StagedToolMessage` / `UnadmittedToolHandler` | per-call wrapper | `sdkadapter.ConvertToolRegistryWithAdmission` on registered tools; denial renders as `RoleTool` |
| `RefOnlyTools` / `RemainderSpool` | per-call wrapper | `applyRefOnlyShim` calls the CLI `*remainder.Spool` directly |
| `OnEvent` / `EventBus` | `Options.Bus` | via `bridgeAgentLoopEvents` (3 kinds mapped) |
| `UsageWriter` | `Options.Audit` | via `bridgeUsageAudit` |
| `FinalWriter` / `RequireFinalText` | post-run finalize | via `finalizeSDKTurn` |
| `MaxTurns` | clamps `MaxIterations` | pre-default so 0 means "any limit wins" |
| `DeadlineAt` | narrows ctx | pre-Run |
| `InterruptCh` | steer bridge | one-shot goroutine |
| `MailboxPending` | steer bridge | watchdog poller |
| `MailboxPendingInterrupt` | steer bridge | strict signal-branch poller |
| turn history | `Result.History` | `runOnceSDK` writes the SDK history back onto `Loop.Messages`, including the turn's assistant and tool messages, and falls back to the last assistant text when the final step produced none |

Not carried on the SDK path: `Loop.LastFinishReason` stays empty —
the SDK's `Message` shape has no finish-reason field, so the legacy
per-step reason reporting has no SDK analogue yet.

## 2. Accepted semantic gaps

The set value passes through (or the knob is simply absent), but the
SDK interprets it differently or not at all. A `Backend: "sdk"`
caller accepts the difference.

- **`MaxConcurrentTools > 1`** — the SDK runs tool calls sequentially
  within a turn, ordered by `ToolCall.Index`
  (`mivia-ai-sdk/agentloop/toolcall.go`). Parallel batches do not
  exist on the SDK path.
- **`MaxSteps <= 0`** — the legacy loop treats 0 as unbounded; the
  SDK's `Validate` requires a positive `MaxIterations`, so the adapter
  substitutes `defaultSDKMaxIterations = 25`. An unbounded run is
  impossible on the SDK path.
- **`BatchResultBudgetBytes`** — the positive form maps to
  `TurnResultBudget`, but where the legacy batch shaper degrades an
  over-budget result to an honest truncation notice (never dropping a
  call), the SDK budget OMITS the result with a bare "omitted" notice.
  The negative derived-budget mode has no SDK analogue at all.
- **Same-batch dedup** — the legacy dispatcher collapses identical
  read-class calls within one batch; the SDK executes every call.
- **Conclude-steer nudges** — the legacy loop injects a conclude
  message when budgets or the deadline are nearly exhausted; the SDK
  path has no equivalent injection.
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

- **`Surface` rotation** — the SDK reads `Options.Tools` once at
  `agentloop.New` and runs one loop per Run call; per-step surface
  rotation would require rebuilding the loop per step, which the SDK
  does not support. This is why `internal/chat/session.go` (per-step
  admission publication via `wireStepBoundaryAdmission`) pins
  `Backend: "legacy"`.
- **`BeforeStep`** — the SDK has no pre-step host hook. This is why
  `internal/subagents/multi_step.go` (parent mailbox drain) pins
  `Backend: "legacy"`.
- **`WorkLimits` token reservations** — `MaxPromptTokens`,
  `MaxOutputTokens`, `MaxOutputPerCall`, `MaxToolCalls`, and
  `PreserveWorkLimits`. The CLI's reservation model (refuse to start a
  turn that would exceed; refund on completion, tracked in
  `internal/runtime/work_limits.go`) is fundamentally different from
  the SDK's in-loop cumulative `MaxTotalTokens` cap; mapping one onto
  the other would change the contract. Callers keep tracking
  reservations outside the loop and gating the call.
  `MaxTurns` and `DeadlineAt` are carried (§1).

## See also

- `internal/agent/agentloop_adapter.go` — the mapping code itself.
- `internal/agent/loop_dispatch.go` — the dispatcher that selects
  between legacy and SDK backends and writes the SDK history back.
- `internal/agent/refonly_shim.go` — the ref-only shim and its known
  divergences from the legacy `refOnlyTier`.
