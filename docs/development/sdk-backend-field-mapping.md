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

**Carried.** The CLI's `internal/sdkadapter.ConvertToolRegistryWithAdmission`
wraps every CLI tool with an admission wrapper that runs the
`StagedToolMessage` and `UnadmittedHandler` predicates per-call. When
a predicate answers true, the wrapper returns the predicate's denial
string wrapped in `tools.Out{Value: msg}`; the SDK renders that as a
`RoleTool` message exactly the way the legacy path does. The model
sees the denial and retries on the next iteration. Per-call
evaluation keeps the `UnadmittedHandler` auto-stage side effect
(`internal/agent/options.go:108-117`) firing only when the model
actually invokes the unadmitted tool, not at conversion time. See
`internal/sdkadapter/tool_registry.go` for the converter and its
table tests in `tool_registry_test.go`.

### `RefOnlyTools`, `RemainderSpool`

The SDK exposes `spool.SpoolTool` (`spool/tool.go:202-234`) for
per-call oversize-to-spool and `BatchTruncationNotice`
(`agentloop/wire.go:32-37`) as the over-budget fallback. The CLI's
"promote oversize to ref instead of notice, gated by tool name and
batch byte pressure" is a *policy* on top of these primitives.

**Carried today (A3): stay fail-closed; document the gap.** The
adapter's `rejectUnsupportedSDKBatches` keeps `RefOnlyTools` and
`RemainderSpool` in the unsupported set. A `Backend: "sdk"` caller
that needs ref-notices must stay on `Backend: "legacy"`.

Why the originally proposed A1 / A2 wiring does not work:

1. **Batch-level vs per-call semantics.** The CLI's
   `refOnlyTier` (`internal/agent/shape_batch.go:341`,
   `shape_batch_refonly.go:25-45`) fires only when the batch is over
   `effectiveBatchBudget` AND the body clears `BatchDegradeFloorBytes`
   AND the tool is in `RefOnlyTools`. `spool.SpoolTool` is
   unconditional per-call: every wrapped Run spools when its content
   exceeds `maxBytes`. Wrapping `RefOnlyTools` tools in `SpoolTool`
   changes user-facing behavior for under-budget batches and small
   results.

2. **Missing principal injection.** `spool.SpoolTool.Run` reads the
   spool principal via `spool.PrincipalFrom(ctx)`
   (`spool/tool.go:38-42`); without `spool.WithPrincipal(ctx, name)`
   attached, every wrapped call returns `ErrNoPrincipal`. No
   production SDK call site attaches `WithPrincipal` (grep across
   the SDK: zero non-test call sites), and the SDK's pre-tool hook
   layer (`agentloop/toolcall.go:165-173`) discards the handler's
   returned context, so a `PointPreTool` handler cannot inject the
   principal before the wrapped tool runs. Wrapping `RefOnlyTools`
   in `SpoolTool` would fail every wrapped call.

An SDK-side fix would unlock this. Either (a) a `PointPreTool`
handler return shape that lets the handler attach a context
(consumed by `RunScoped`), or (b) a `spool.Spool` constructor
variant taking a `principal func(ctx) string` resolver. Either is
a non-trivial SDK decision; either is out of scope for the
flag-flip series. Until one lands, `Backend: "sdk"` callers cannot
get ref-notices from these flags. The host's existing
`read_output` tool (`internal/clichat/read_output.go:42-43`) is
unaffected — it reads through the CLI's `*remainder.Spool` directly
and continues to work for any caller that stays on
`Backend: "legacy"`.

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

No fields remain in this section. `MaxContextTokens`,
`SummaryConfig.Summarizer`, `StagedToolMessage`, and
`UnadmittedToolHandler` are all carried today (see §1 and §3). The
remaining fail-closed fields are documented as legacy-only in §3:
`Surface` rotation, the four `WorkLimits` token reservations, and
`RefOnlyTools`/`RemainderSpool`. None of those need SDK extensions;
they are accepted semantic gaps that require either SDK work the
SDK authors have not yet committed to or design decisions the CLI
cannot make on its own.

`MaxContextTokens` and `SummaryConfig.Summarizer` are carried by
the SDK path: `RunAgentLoopOnce` calls `opts.PreparationManager.Prepare`
on the loop's history, then runs the CLI's host-side summary inject
when a summarizer is wired, and hands the resulting history to
`RunSteerable`. The SDK's `Window` stays nil so the SDK never runs
its own per-iteration planning pass on top of the CLI's host-side
compaction.

## See also

- `internal/agent/agentloop_adapter.go` — the mapping code itself,
  with the same three-group taxonomy in its package doc.
- `internal/agent/loop_dispatch.go` — the dispatcher that selects
  between legacy and SDK backends.
