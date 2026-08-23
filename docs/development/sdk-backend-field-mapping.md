# SDK backend field mapping (B.2 #8 inner-loop swap)

The CLI `agent.Options` field set splits into three groups when the
caller selects `Backend: "sdk"`. The groups are deliberately explicit
so a `Backend: "sdk"` caller knows exactly what behavior changes.

## 1. Carried today

The SDK path consumes these directly:

| CLI field | SDK carrier | Notes |
|---|---|---|
| `Backend == "sdk"` | selects the dispatch | one-line in `loop_dispatch.go` |
| `Model` | `Options.Model` | pass-through |
| `Temperature` | `Request.Temperature` | translator in `agentloop_completer.go` |
| `MaxTokens` | `Request.MaxTokens` | translator |
| `MaxSteps` | `Options.MaxIterations` | clamped by `MaxTurns` when set |
| `SessionID` | `Options.SessionID` | required when Usage is set |
| `AdvertisedToolSpecs` | converted registry | via `sdkadapter.ConvertToolRegistry` |
| `Reasoning` | `Options.ReasoningEffort` | 7→4 mapping in `sdkadapter` |
| `MaxToolCallsPerBatch` | `Options.MaxCallsPerTurn` | positive only |
| `BatchResultBudgetBytes > 0` | `Options.TurnResultBudget` | literal bytes |
| `OnEvent` / `EventBus` | `Options.Bus` | via `bridgeAgentLoopEvents` |
| `UsageWriter` | `Options.Audit` | via `bridgeUsageAudit` |
| `FinalWriter` / `RequireFinalText` | post-run finalize | via `finalizeSDKTurn` |
| `MaxTurns` | clamps `MaxIterations` | pre-default so 0 means "any limit wins" |
| `DeadlineAt` | narrows ctx | pre-Run |
| `InterruptCh` | steer bridge | one-shot goroutine |
| `MailboxPending` | steer bridge | watchdog poller |
| `MailboxPendingInterrupt` | steer bridge | strict signal-branch poller |

## 2. Accepted semantic gaps

Set value passes through, but the SDK interprets differently or not
at all. A `Backend: "sdk"` caller accepts the difference; the gaps are
documented here so the call is informed.

### `MaxConcurrentTools > 1`

The SDK runs tool calls sequentially within a turn, ordered by
`ToolCall.Index` (`mivia-ai-sdk/agentloop/toolcall.go:24,63`). A
`MaxConcurrentTools > 1` value is silently dropped: the SDK runs one
call at a time. A CLI caller relying on parallel batches to fit a
latency budget must accept sequential execution on the SDK path, or
stay on `Backend: "legacy"`.

### `BatchResultBudgetBytes < 0`

The CLI's negative form derives a budget from `MaxContextTokens` in
the `internal/agent/shape_batch` path. The SDK's `TurnResultBudget`
is a literal byte budget only (`mivia-ai-sdk/agentloop/options.go:264`
and `agentloop/toolcall.go:46`). A `BatchResultBudgetBytes < 0` value
is silently dropped on the SDK path: no batch shaping runs from this
knob. The positive form still maps.

## 3. Fail-closed today

Every CLI Options field whose semantics cannot be carried on the SDK
path returns an error naming the field from `buildAgentLoopOptions`,
so an opt-in caller learns the boundary at the call instead of
silently losing behavior. The fail-closed set shrinks commit by
commit.

### `WorkLimits` token reservations and `PreserveWorkLimits`

The CLI's reservation model is fundamentally external to the loop:
`internal/runtime/work_limits.go` refuses to START a turn when
reservations would be exceeded, and refunds on completion. The SDK's
`MaxTotalTokens` is a stop-in-the-loop cumulative cap
(`mivia-ai-sdk/agentloop/options.go:188`), not a reservation. Mapping
the CLI reservation fields onto it would change the contract: a CLI
`MaxPromptTokens: 8000` means "refuse to start a new turn that would
push the prompt over 8000 tokens"; the SDK would interpret that as
"stop the run after cumulative tokens have reached 8000."

The four fail-closed fields are `WorkLimits.MaxPromptTokens`,
`MaxPromptTokens`, `WorkLimits.MaxOutputTokens`,
`WorkLimits.MaxOutputPerCall`, `WorkLimits.MaxToolCalls`, and
`PreserveWorkLimits` (which is the cross-call reservation stickiness).

The CLI keeps tracking reservations outside the SDK call: callers that
need them enforce them by gating the call to `RunAgentLoopOnce`.

### `Surface` rotation

The SDK reads `Options.Tools *tools.Registry` once at
`mivia-ai-sdk/agentloop.New` and runs one loop per Run call. Per-step
surface rotation — the CLI's `Surface func() Surface` returning a
fresh registry each step — would require rebuilding the loop per
step, which the SDK does not support. A `Backend: "sdk"` caller that
needs surface rotation must stay on `Backend: "legacy"`.

### `StagedToolMessage`, `UnadmittedToolHandler`

The SDK's `PointPreTool` hook
(`mivia-ai-sdk/hooks.Handler`) returns `(bool, error)` — a veto
(`false, nil`) stops the run with `StopHookVeto`; an error fails the
run. It has no substitution shape: a handler cannot return a
synthetic `RoleTool` message to replace the failed call. The CLI's
`executeToolTask` (in `internal/agent/loop_tool_exec.go:13-27`) uses
the staged/unadmitted predicates to produce a denial `RoleTool`
message and let the model retry on the next iteration — a continue,
not a stop.

Two workarounds were considered and rejected:

1. **Per-step SDK registry rebuild** — at each iteration, enumerate
   `AdvertisedToolSpecs`, call `StagedToolMessage(name)` for names
   not in the live registry, register stub tools that produce the
   staged message as their `Out.Value`. Rejected because
   `UnadmittedToolHandler` has a documented side effect ("auto-stage
   it for publication at the next step boundary", see
   `options.go:111`), so probing it at conversion time would trigger
   that side effect before the model even asks for the tool.

2. **Pre-step hooks that veto** — wrong shape, since the staged case
   must continue, not stop.

Wiring requires either a SDK extension (`PointPreTool` returns
`(allow bool, substitute *provider.Message, err error)`) or a host
loop that catches `StopHookVeto` and retries. Both are out of scope
for the flag flip; the fields stay fail-closed for now.

## 4. Under active wiring (fail-closed today, wire planned)

These fields are documented here because they will eventually be wired
into the SDK path. Until each lands, setting the field returns an
unsupported-SDK-option error.

- `RefOnlyTools`, `RemainderSpool`: wire via the SDK's
  `contextplan.Planner` with a planner policy that promotes oversized
  results to refs.
- `MaxContextTokens`: wire as "pass-compacted-history" — the CLI's
  `PreparationManager` keeps hosting compaction, and the SDK receives
  the compacted history as its starting `Request.Messages`.
- `SummaryConfig.Summarizer`: wire as a post-compaction inject — the
  CLI's `internal/agent/summary_inject` keeps hosting the LLM summary
  call, and the SDK receives the injected message as part of the
  history.

## See also

- `internal/agent/agentloop_adapter.go` — the mapping code itself,
  with the same three-group taxonomy in its package doc.
- `internal/agent/loop_dispatch.go` — the dispatcher that selects
  between legacy and SDK backends.
