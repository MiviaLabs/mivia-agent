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

The initial design assumption was that the SDK needs a new primitive
("deny-and-substitute-message") to support this flow. After research
and review, the assumption was wrong: every adjacent generic agent
SDK exposes pre-tool deny as a stop, not a continue-with-message
(Anthropic Claude Code's `PreToolUse` exposes `permissionDecision`
and `updatedInput` for *input* rewriting; OpenAI Agents SDK exposes
input/output guardrails; MCP is server-side only). The SDK's
`hooks.Handler` shape is the right generic primitive; the
substitute-message path is mivia-agent-shaped, not generic.

Two CLI-side workarounds fit the SDK's existing shape and stay
fail-closed until the CLI does the refactor:

1. **SDK `Scope.Approve` for known-but-declined tools** — register
   `tools.Scope.Approve` against the live registry for staged or
   unadmitted names. The SDK denies via `ErrToolDeclined`, the loop
   ends on this iteration, and the CLI's retry logic produces a
   `RoleTool` denial message naming the deferred tool on the next
   iteration. The auto-stage side effect runs inside the CLI's
   `Approve` callback at the moment of denial — not at conversion
   time.

2. **Pre-tool hooks that synthesize denial** — register a
   `PointPreTool` handler that, for a known staged name, returns
   `(allow=false, err=nil)` and let `StopHookVeto` end the iteration.
   The CLI catches `StopHookVeto` and rewrites the run's final
   message to a denial. This is wrong for interactive runs (the
   user sees a `StopHookVeto` end-of-run) but matches a non-interactive
   batch shape.

Both routes fit the SDK's current primitives without an SDK
extension. The CLI picks one in the (A) refactor slice and drops
`StagedToolMessage`/`UnadmittedToolHandler` from `Options`.

### `RefOnlyTools`, `RemainderSpool`

The SDK already exposes `spool.SpoolTool` (`spool/tool.go:202-234`)
for per-call, per-tool oversize-to-spool and `BatchTruncationNotice`
(`agentloop/wire.go:32-37`) as the over-budget fallback. The CLI's
"promote oversize to ref instead of notice, gated by tool name and
batch byte pressure" is a *policy* on top of these primitives, not a
new primitive.

The initial assumption was that the SDK needs an extension
(`TurnResultBudget.RefOnlyToolNames` + `TurnResultBudget.Spool`).
After review, a non-mivia consumer wanting this behavior composes
`spool.SpoolTool` per named tool at registration time; tools not in
`RefOnlyToolNames` continue to receive `BatchTruncationNotice`. No
SDK extension needed.

The CLI's (A) refactor wraps each `RefOnlyTools` tool in
`spool.SpoolTool` at conversion time, leaving the rest of the
registry untouched. The CLI drops `RefOnlyTools`/`RemainderSpool`
from `Options` and replaces them with `Options.SpooledToolNames` (a
naming-only change in the SDK path).

### `MaxContextTokens`

The CLI's `MaxContextTokens` is a prompt budget; the SDK's
`Window.MaxTokens` is the model's context window
(`mivia-ai-sdk/contextplan/window.go:23-27`). These are different
concepts: the CLI's value gates the `PreparationManager.Prepare`
prune pass (`internal/agent/context.go:74-91`); the SDK's value gates
`contextplan.Compact` (`mivia-ai-sdk/contextplan/compact.go:28-78`).
Mapping CLI's value to `Window.MaxTokens` would either over-budget
the SDK or silently change the CLI's cache-miss amortization.

**Carried.** The CLI keeps `PreparationManager` on the SDK path;
`RunAgentLoopOnce` calls `Prepare` on the loop's history and hands
the prepared messages to `RunSteerable`. The SDK's `Window` stays
nil; the SDK's per-call `Budget`
(`agentloop/options.go:208-212`) bounds one Completer call by byte
count after the fact, mirroring the CLI's
`promptBudgetErrorWithTools` (`internal/agent/context.go:225-237`).

When `PreparationManager` is nil but `MaxContextTokens` is positive,
the SDK path silently bypasses the host-side compaction. That is a
CLI-side misconfiguration (no compaction despite a positive budget)
rather than a path-level error.

### `SummaryConfig.Summarizer`

The SDK's `contextsummary.Summary` has 5 fields
(`mivia-ai-sdk/contextsummary/summary.go:28-34`); the CLI's
`Summary` has 7 (`internal/contextmgr/contracts.go:188-198`). The
CLI's two extra fields (`Evidence`, `ChangedSurfaces`) carry
host-side omitted-evidence and write-class tracking that downstream
CLI consumers depend on
(`internal/agent/summary_inject_test.go:456-460`).

**Carried.** The SDK's `Window` is nil on this path, so the SDK
never invokes its own LLM summary call. The CLI's
`SummaryConfig.Summarizer` runs host-side through
`Loop.injectSummary` after `PreparationManager.Prepare` reports a
compacted outcome; the rendered summary message rides on the
prepared history as the last user-role frame before the SDK loop
runs. The CLI's 7-field `Summary` schema stays authoritative on the
wire — `Evidence` and `ChangedSurfaces` keep their host-side
semantics, and the SDK's 5-field schema is never reached.

## 4. Under CLI refactor (fail-closed today)

The three fields below all stay fail-closed on the SDK path. They
will be unwired in mivia-agent by the (A) refactor slices — each one
removes a CLI vocabulary quirk (or wraps an SDK primitive) rather
than extending the SDK. The SDK never grows a new primitive for the
flag flip.

- `StagedToolMessage`, `UnadmittedToolHandler`: refactor to use
  `tools.Scope.Approve` + retry-on-decline.
- `RefOnlyTools`, `RemainderSpool`: refactor to wrap `RefOnlyTools`
  tools in `spool.SpoolTool` at conversion time.

`MaxContextTokens` and `SummaryConfig.Summarizer` are now carried:
`RunAgentLoopOnce` calls `opts.PreparationManager.Prepare` on the
loop's history, then runs the CLI's host-side summary inject when a
summarizer is wired, and hands the resulting history to
`RunSteerable`. The SDK's `Window` stays nil so the SDK never runs
its own per-iteration planning pass on top of the CLI's
host-side compaction.

## See also

- `internal/agent/agentloop_adapter.go` — the mapping code itself,
  with the same three-group taxonomy in its package doc.
- `internal/agent/loop_dispatch.go` — the dispatcher that selects
  between legacy and SDK backends.
