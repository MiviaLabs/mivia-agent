# Tool Calling, Subagents, and Skills: Improvement Plan

**Status:** Complete for the defined tool, delegation, skill, shutdown, and security scope
**Current phase:** Final post-fix verification passed; platform/live-provider checks remain release follow-ups
**Last verified:** 2026-07-27
**Next action:** Native Windows and live-provider verification when those environments are available

## Outcome

The reported hang is reachable without a stuck tool process: the normal CLI advertises `delegate` and `dispatch_tasks` to the model after the runtime dispatcher has already snapshotted the registry. A model that retries the resulting `unknown tool` error can livelock indefinitely. Unlimited parent execution is intentional product behavior and is not treated as a defect.

The actual live provider trace was **NOT_RUN**. The conclusions below are source-confirmed unless explicitly marked otherwise. The working tree already contained user-owned timeout/allowlist edits and now also contains the dispatcher, delegation, shutdown, and security patches; those changes are tracked separately below.

## Evidence and current wiring

### Execution path

```text
cmd/mivia -> cli.runChat -> chat.Session
  -> agent.Loop.Run
  -> provider.Completer.ChatTurn
  -> runToolBatch / executeToolsParallel
  -> runtime.Dispatcher.Invoke
  -> tools.Registry.Execute
  -> append tool result
  -> next model step
```

### Subagent path

```text
model tool call
  -> delegate or dispatch_tasks
  -> subagents.Pool.Run
  -> runtime.Dispatcher.Invoke(kind=Subagent)
  -> OneShotHandler OR MultiStepHandler
```

`MultiStepHandler` creates a nested `agent.Loop` with a restricted registry that removes `delegate` and `dispatch_tasks`. It does not inherit the parent dispatcher, policy, budget, sink, or invocation identity.

### Skills path

`skills.Registry` supports programmatic definitions and can register them as `Skill` or `Subagent` handlers. `skills.LoadMarkdown` now reads bounded instruction-only `.ai/skills/*/SKILL.md` files during normal chat startup, and `dispatch_tasks` selects registered handlers explicitly. Markdown is supplied as explicitly framed untrusted task guidance; embedded code is never executed.

### Resolution status

The baseline findings below are retained as audit evidence. Findings fixed in
the current worktree are dispatcher registration order, child identity and
failure propagation, governed nested execution, Markdown skill loading and
dispatch, TUI cancel/join and queue bounds, Windows shell/process fail-open
behavior, argv header leakage, provider request-key collisions, and retained
dispatcher state cleanup. Remaining gaps are platform/live runtime checks.

## Confirmed findings

### Accepted design: unlimited parent loop

Evidence:

- `internal/chat/session.go:65-80` initializes `MaxSteps: 0`.
- `internal/agent/loop.go:132-158` only stops when `MaxSteps > 0`.
- `internal/agent/loop.go:220-243` returns `done=false` for every response containing tool calls.
- `docs/product/agent.md:35-37` documents a finite default of 30, contradicting source.

Impact: repeated tool calls intentionally continue until the model returns, the user cancels, the provider fails, or an explicit `/steps` limit is configured.

Required change: none to the default. Preserve `MaxSteps: 0` and `/steps 0` as the documented unlimited mode. Improve cancellation, progress events, and explicit user-configured limits; do not add an implicit total-turn deadline or cost cap that changes this product contract.

### P0: delegation tools were exposed but absent from the dispatcher — fixed

Evidence:

- `internal/cli/dispatcher.go:26-31` calls `runtime.NewToolDispatcher(reg, policy)`.
- `internal/cli/dispatcher.go:64-66` registers `delegate` and `dispatch_tasks` into `reg` afterward.
- `internal/runtime/tools.go:15-22` registers only the tools present during construction.
- `internal/runtime/dispatcher.go:170-176` returns `unknown tool` when no handler exists.

Impact: the model sees delegation tools in `OpenAITools()` but their calls fail at runtime. With unlimited mode, a provider retrying the visible call can livelock the session until the user cancels or sets `/steps`.

Required change: establish one authoritative registration path so every model-visible tool has a dispatcher handler and permission entry before the first request. Add an invariant test that compares `Registry.OpenAITools()` against dispatcher availability, plus a real `NewSessionDispatcher -> agent.Loop.Run` delegation test.

### P1: child invocation identity was globally collision-prone — fixed

Evidence:

- `internal/cli/delegate.go:80-85` uses task ID `d1` for every single delegation.
- `internal/subagents/subagents.go:174-178` defaults the dispatcher request ID to the task ID.
- `internal/runtime/dispatcher.go:62-70` keys active/completed/waiter/fingerprint state only by ID across kinds and parents.

Impact: repeated or concurrent delegations can reuse a completed result, reject a different input as ID reuse, or wait on another invocation. The current tests do not cover repeated calls through one shared session dispatcher.

Required change: define a run/turn/parent identity contract. Use collision-free child IDs derived from the parent invocation and task ID, carry `ParentID`, `TurnID`, `Depth`, and `Budget`, and either key dispatcher state by a typed invocation identity or guarantee globally unique IDs.

### P1: multi-step child failures were reported as successful dispatcher calls — fixed

Evidence:

- `internal/subagents/multi_step.go:98-119` serializes loop errors as `status=error` but returns a nil Go error.
- `internal/subagents/subagents.go:178-183` maps nil `Result.Err` to `completed`.
- `internal/cli/delegate.go:89-96` returns the JSON output as success when the dispatcher sees no Go error.

Impact: cancellation, timeout, and `max_steps` failures can be delivered to the parent as a successful delegation result, masking failed work and preventing correct dependency blocking.

Required change: return the operational error through the dispatcher/pool boundary while keeping a bounded structured diagnostic in the output. Define whether partial output is retained separately from success status.

### P1: nested multi-step execution bypassed parent policy — fixed

Evidence:

- `internal/subagents/multi_step.go:98-109` constructs nested loop options without `Dispatcher`.
- `internal/agent/loop.go:363-364` creates a fresh default dispatcher when no dispatcher is supplied.
- `internal/subagents/subagents.go:178` passes task depth unchanged.

Impact: nested tools lose parent policy, budget accounting, sink/trace correlation, deduplication, and effective depth propagation. This makes the configured governance boundary inconsistent between parent and child execution.

Required change: pass a child execution context/dispatcher that preserves policy and telemetry while using a restricted capability set. Increment and validate depth at the child boundary; propagate remaining budget and cancellation.

### P1: configured budget did not constrain normal tool/subagent work — partially fixed

Evidence:

- `internal/cli/dispatcher.go:27-31` configures `Policy.MaxBudget`.
- `internal/runtime/dispatcher.go:209-212` accounts only `Request.Budget`.
- `internal/agent/loop.go:466` emits tool requests with no budget.
- `internal/cli/delegate.go:80-85` and `internal/cli/dispatch.go:94-100` create tasks with no budget.

Impact: nonzero `default_budget` does not limit ordinary tool or delegated work.

Required change: define budget units, allocate a per-run budget, propagate remaining budget to tools and children, and reject over-budget work before execution.

### P1: dispatcher lifetime state was unbounded — lifecycle cleanup added

Evidence: `internal/runtime/dispatcher.go:62-70,289-296` retains completed results, fingerprints, spent-budget entries, and resource locks with no lifecycle/eviction path. A session creates one dispatcher in `internal/cli/chat_repl.go:58-61`.

Impact: repeated calls and large outputs accumulate memory for the session lifetime.

Required change: retain only the idempotency metadata needed for a bounded window, add an explicit lifecycle/close path, and test retention without weakening duplicate protection.

### P1: skills were not wired to production chat — fixed

Evidence:

- `internal/cli/chat_repl.go` loads `.ai/skills` and passes the registry into session construction.
- `internal/skills/loader.go` bounds and parses instruction-only Markdown.
- `internal/cli/dispatch.go` selects registered handlers and propagates skill permissions.
- `internal/cli/dispatcher.go` registers skills on both `Skill` and `Subagent` surfaces.

Impact: the documented/declared skill surface is not reachable from normal chat, and `Definition.Tools`/version/permission selection is not part of dispatch-task execution.

Required change: keep the loader bounded and instruction-only, validate names and duplicate registrations, and retain explicit handler selection with permission checks. Do not execute arbitrary code from Markdown.

### P1: shutdown could leave active work running — fixed

Evidence:

- `internal/cli/tui_message.go:128-130` quits on Ctrl+D without calling the active cancel function.
- `internal/cli/tui.go:386-430` starts turns from a background context.
- `internal/cli/tui.go:464-466` closes the bridge after Bubble Tea exits but does not cancel and join the worker.

Impact: provider requests, tools, and subprocesses can outlive the TUI and autosave can race incomplete work.

Required change: use one owned turn context, cancel it on every exit path, and join/observe the worker before final bridge close and autosave.

### P1: Windows command and process isolation failed open — fixed in source

Evidence:

- `internal/tools/run.go:55-60` routes `echo`, `true`, and `false` through `ComSpec /c` even though the tool promises argv/no shell.
- `internal/tools/process_windows.go:34-47` swallows `OpenProcess` and `AssignProcessToJobObject` failures.
- `internal/tools/process_windows.go:49-56` then falls back to killing only the direct process.

Impact: model-controlled arguments can cross a shell boundary, and descendant processes can survive timeout/cancellation. Windows runtime proof is **NOT_RUN**; the source-level fail-open behavior is confirmed.

Required change: implement safe built-ins without a shell or reject them; fail closed if job attachment cannot be guaranteed; add Windows-specific injection and descendant-termination tests.

### P1: provider retries could replay accepted POST requests — client mitigation added

Evidence: `internal/provider/openai_compat.go` now sends a stable per-request `Idempotency-Key`; `internal/provider/retry.go:65-112` retries transport and retryable HTTP failures. Provider enforcement of the key remains **NOT_RUN**.

Impact: a response lost after provider acceptance can cause duplicate billable generations and divergent tool-call decisions.

Required change: confirm provider idempotency support end-to-end; until then, retry safety is only a client-side mitigation and remains release risk.

### P1: command output echoed raw argv — fixed

Evidence: `internal/tools/run.go:97-115` scrubs stdout/stderr but constructs the returned command header from raw `argv`.

Impact: secret-shaped or personal data in arguments can reappear in model context, UI previews, and traces.

Required change: omit raw arguments or apply structured redaction before returning the header; add secret/PII-shaped argv tests.

### P2: TUI queued input was unbounded and recursively drained — fixed

Evidence: `internal/cli/tui.go:70` stores an unbounded queue; `internal/cli/tui_message.go:218-223` appends without a cap; `internal/cli/tui.go:372-379` recursively drains slash commands.

Impact: queued input can consume unbounded memory and slash-command bursts can grow the stack.

Required change: cap queue size with an explicit user-visible rejection policy and drain iteratively.

## Findings rejected or not yet proven

- A stuck Unix `run_command` process is not confirmed; process-group timeout tests pass.
- A `streamBridge` deadlock is not confirmed; race and coalescing tests pass.
- A live provider/API stall is plausible but unverified because no provider run was available.
- Unlimited parent execution is intentional and not a bug; do not replace it with a finite default.
- A cross-kind wait cycle from ID collision is a design risk; the current normal delegation path is already blocked by missing registration, so require the identity regression test before labeling it a live deadlock.
- The sixth delegated lane exceeded the initial wait window; its late result was collected separately and did not add a unique root cause. No lane was allowed to modify files.

## Implementation phases

### Phase 0 — Contract and deterministic reproduction

Files to read first:

- `internal/agent/loop.go`, `internal/agent/loop_test.go`
- `internal/chat/session.go`, `internal/chat/session_test.go`
- `internal/cli/dispatcher.go`, `internal/cli/delegation_test.go`
- `internal/runtime/dispatcher.go`, `internal/runtime/dispatcher_test.go`

Expected changes:

- Add an always-tool-calling provider fixture and a cancellation/explicit-limit test. Do not assert a finite default.
- Add a real top-level delegation test that goes through `agent.Loop`, not direct `delegateTool.Execute`.
- Add a dispatcher invariant test for every model-visible tool.
- Write down the unlimited-default, explicit-step-limit, timeout, and budget semantics in the canonical owned product/architecture docs.

Stop condition: do not change runtime behavior until the tests reproduce both the unbounded loop and missing delegation handler.

### Phase 1 — Preserve unlimited execution and make delegation callable

Likely files:

- `internal/chat/session.go`
- `internal/agent/loop.go`
- `internal/cli/dispatcher.go`
- `internal/runtime/tools.go` or `internal/runtime/dispatcher.go`
- `internal/cli/delegation_test.go`, `internal/agent/loop_test.go`
- `docs/product/agent.md`

Steps:

1. Preserve `MaxSteps: 0` and `/steps 0` as the intentional unlimited contract.
2. Register delegation tools and dispatcher handlers through one ordered/atomic path; do not expose a tool before it is invokable.
3. Ensure cancellation and an explicit `/steps N` limit return clear errors and stop the loop promptly.
4. Cover success, unknown-tool, repeated-tool, cancellation, explicit-limit, and model-retry behavior.

Acceptance: unlimited mode remains unlimited until cancellation or provider completion; explicit limits stop promptly; one-shot delegation completes through the normal session path; no visible tool returns `unknown tool` due to construction order.

### Phase 2 — Preserve child identity, policy, cancellation, and failure status

Likely files:

- `internal/runtime/dispatcher.go`, `internal/runtime/tools.go`
- `internal/subagents/subagents.go`, `internal/subagents/oneshot.go`, `internal/subagents/multi_step.go`
- `internal/cli/delegate.go`, `internal/cli/dispatch.go`, `internal/cli/dispatcher.go`
- `internal/agent/loop.go`
- corresponding runtime/subagent/CLI tests

Steps:

1. Define typed invocation identity and stable parent/turn/task correlation.
2. Propagate depth, remaining budget, cancellation, timeout, and event sink into child execution.
3. Keep the restricted capability registry without silently creating an ungoverned dispatcher.
4. Return non-nil operational errors for child cancellation, timeout, and max-step failures; preserve bounded diagnostic JSON separately.
5. Add repeated/concurrent delegation tests and dependency failure tests.

Acceptance: independent children never share waiters/results; failed children block dependents or surface errors according to `PartialResults`; parent cancellation terminates child work; trace events correlate parent and child.

### Phase 3 — Skill contract and production wiring

Likely files:

- `internal/skills/skills.go` and new loader/config code only if the approved source contract requires it
- `internal/cli/chat_repl.go`, `internal/cli/dispatcher.go`, `internal/cli/dispatch.go`
- `internal/skills/skills_test.go`, `internal/cli/delegation_test.go`
- canonical docs under `docs/product/` or `docs/architecture/`

Steps:

1. Decide whether skills are built-in Go definitions, workspace files, or both. Do not claim `.ai/skills` loading until a parser/loader exists.
2. Pass the loaded registry explicitly into session construction.
3. Expose only selected, validated skills; do not map arbitrary task names to `oneshot`.
4. Enforce required tools, version, permission, input/output schemas, timeout, budget, and cancellation on the actual invocation path.
5. Add production-shaped integration tests proving a skill is reachable and an unavailable/unauthorized skill is rejected.

Acceptance: the documented skill surface matches the callable surface; one skill success and each relevant negative path are covered.

### Phase 4 — Separately approved resource, shutdown, and security hardening

This phase is intentionally separate from delegation correctness. Start it only after owner approval of the security and operational scope and its platform-specific verification.

Likely files:

- `internal/runtime/dispatcher.go`
- `internal/cli/tui.go`, `internal/cli/tui_message.go`
- `internal/tools/run.go`, `internal/tools/process_windows.go`, platform tests
- `internal/provider/retry.go`, `internal/provider/openai_compat.go`

Steps:

1. Bound dispatcher retention and add lifecycle cleanup without breaking required idempotency.
2. Cancel and join active TUI work on Ctrl+D, normal quit, and error exit.
3. Remove shell parsing from Windows built-ins and fail closed on job setup failure.
4. Redact command arguments before tool result construction.
5. Decide and test safe POST retry/idempotency semantics.
6. Cap and iteratively drain queued TUI input.

Acceptance: no unbounded execution or retention remains in the reviewed path; platform-specific security tests pass; shutdown leaves no active child/subprocess work.

## Verification ladder

Run in this order after each phase:

1. Targeted package tests for the changed seam.
2. Regression tests for success, error, cancellation, timeout, repeated calls, and dependency failure.
3. `go test ./... -count=1`
4. `go test -race ./... -count=1`
5. `go vet ./...`
6. `make verify` and `make build` when control-surface, security, or build/runtime files change.
7. Native Windows tests for Windows process/shell behavior; live provider and built-binary interactive checks remain required for release confidence.

Do not report a live hang as reproduced until a provider or deterministic HTTP fixture shows the repeated tool-call sequence and the emitted lifecycle events identify the blocking step.

## Scope and non-goals

In scope: parent tool-loop termination, dispatcher registration, child lifecycle/policy propagation, skills reachability, cancellation/join, bounded state, TUI queue/shutdown, provider retry safety, and Windows command/process isolation.

Non-goals: changing provider/model selection, adding a new provider, redesigning the TUI, changing repository-wide policy, or implementing arbitrary workspace skill file execution without an explicit security review.

## Current verification and residual risk

Implemented in the current worktree:

- Delegation tools are registered in both the model registry and dispatcher before the session starts, with checked registration errors.
- Real `agent.Loop` delegation, repeated invocation identity, permissioned skill dispatch, and multi-step operational errors have regression coverage.
- Nested multi-step loops reuse the governed dispatcher and propagate parent/turn/depth/budget fields; top-level chat turns now have stable session/turn metadata.
- TUI cancellation now cancels and joins the active worker on exit; queued slash commands drain iteratively and the pending queue is capped at 64 entries.
- Windows process-job attachment now fails closed; Windows shell-only built-ins are rejected rather than routed through `ComSpec`.
- `run_command` no longer echoes raw argv arguments, and OpenAI-compatible requests carry a stable idempotency key for retry-capable providers.
- Subagent batches reject duplicate invocation keys, preventing dispatcher result aliasing.
- Dispatcher exposes `Close`, and the normal chat path invokes it after active TUI/REPL work ends to release retained session state.

Verified independently from the native WSL checkout:

- `go test ./... -count=1` — PASS
- `go test -race ./... -count=1` — PASS
- `go vet ./...` — PASS
- `make verify` — PASS
- `make build` — PASS
- focused source inspection and six delegated read-only lanes; five completed within the bounded wait and the sixth completed on a short follow-up

Not verified:

- live provider behavior or billing/idempotency semantics
- Windows runtime, shell injection, and process-job behavior
- built-binary interactive TUI shutdown/queue behavior
- native Windows runtime, live provider enforcement, and interactive TUI behavior remain unverified

The worktree had pre-existing user changes in `internal/agent/loop.go`, `internal/chat/session.go`, `internal/tools/tools.go`, and an untracked `docs/plans/` path. This plan did not modify or overwrite them.

Required human review: approve the unlimited-mode cancellation/observability contract, the skill source contract, provider retry semantics, and Windows command execution policy before implementation.
