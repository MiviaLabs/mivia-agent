# 45 — v2 Lifecycle Events

**Status**: Planned (design work required before any code)
**Items**: R9 (`SessionStart`), R10 (`SubagentStart`/`SubagentStop`), R11 (skill-level hooks)
**Supersedes**: phase 4 of plan 44, moved here because it is a separate piece of
work rather than a tail of that one. Plan 44 phases 0–3 are implemented and
archived at `.mivia/plans/archived/44-lifecycle-hooks/`.

## What changed under this plan while it sat

Plan 44 shipped between this being written and being started, and it moved
ground this plan stands on. Re-check each of these before implementing anything
below; the item text further down has NOT been rewritten around them.

1. **There is no hook trust model.** No confirmation step, no trust store, no
   `/hooks trust`, no `--bypass-hook-trust`. A declared hook runs. Anything here
   that assumes a confirmation gate is wrong.

2. **Hooks load from the WORKSPACE too**, additively, user-config first. This is
   the big one for R9: a `SessionStart` hook that can block session
   initialization, loaded from a repository's own `.mivia/mivia.toml`, means a
   cloned repo can stop mivia from starting in its directory - and unlike a
   blocked tool call, the operator has no session in which to read why. Decide
   deliberately whether project-scoped hooks may bind a blocking `SessionStart`
   at all, and if they may, where the refusal is reported.

3. **`Policy.PostInvokeHook` returns `runtime.HookResult`, not a string**, and
   `runtime.HookVerdict` carries `Runs []HookRun`. Any new event must populate
   `HookRun` records or it will fire invisibly - every existing event produces a
   transcript row per execution, including silent runs, and a v2 event that
   skipped that would be the one hook nobody can see.

4. **Model-visible hook output is framed** in `<lifecycle-hook-output>` tags via
   `agent.FrameHookOutput`, with the payload's own attempts at those tags
   neutralized. A new event whose output reaches the model must go through the
   same framing rather than inventing its own.

5. **An allowing `PreToolUse` hook's `additionalContext` now reaches the model**,
   merged with the reactive event's under one bound. A new blocking event should
   follow that shape rather than the old allow-or-deny-only one.

6. **Line references below have drifted.** `deferredEvents` is at
   `internal/hooks/config.go:112` as this plan says, but treat every other
   `file:line` here as approximate and re-locate by symbol.

## Sequencing note

R9 and R10 are independent of each other and both are self-contained. R11
(skill-level hooks) is a design question first and should not be started as an
implementation task - the "decide between frontmatter hooks, skill-event hooks,
or a hybrid" task under it is the actual deliverable, and the rest depends on
its answer.

## Problem

Three categories of lifecycle events that the current system cannot fire on:

1. **Session lifecycle**: No `SessionStart` hook for environment validation, dependency checks, or session-scoped setup.
2. **Subagent lifecycle**: No `SubagentStart`/`SubagentStop` hooks. You cannot gate the *act of spawning* a subagent — only its internal tool calls after it's already running.
3. **Skill lifecycle**: No skill-level hooks. Skills use probabilistic trigger phrases; there is no deterministic mechanism to fire hooks on skill activation.

All three are explicitly deferred in `internal/hooks/config.go:112-136` (the `deferredEvents` map). The event bus (`internal/events/event.go`) already defines `KindSessionStart`, `KindSessionEnd`, `KindSubagentStart`, `KindSubagentEnd` — so publish sites exist for sessions and subagents. Skill events do not yet exist in the bus.

---

## R9 — `SessionStart` Lifecycle Event

### Current State

- `deferredEvents["SessionStart"]` = `"no session-start publish site exists yet"`
- `internal/events/event.go:26` defines `KindSessionStart`
- `internal/events/metrics.go:47-49` lists `KindSessionStart` as a metric event

The publish site comment is stale — `KindSessionStart` exists in the event bus. The actual gap is wiring it into the lifecycle hook system.

### Scope

- `internal/hooks/config.go` — remove `SessionStart` from `deferredEvents`, add to `V1Events()` and `eventDefaults`
- `internal/hooks/exec.go` — `Runner.Run` must handle a tool-less event (no `payload.Tool`, no `req.Name`)
- `internal/runtime/hooks.go` — new gate function or extension of existing pattern
- `internal/cli/hooks_runner.go` — wiring from session start to hook execution
- `internal/hooks/protocol.go` — `Payload` shape for session events

### Design Considerations

1. **SessionStart has no tool**: The current `Payload` is tool-centric (`Tool`, `Input`, `ToolCallID`). A session event needs a different payload shape — session ID, workspace root, agent name, model, config path.
2. **Can it block?**: `SessionStart` should be able to block session initialization (e.g., if a dependency check fails). This makes it a blocking event like `PreToolUse`, but on session lifecycle, not tool lifecycle.
3. **Publish site**: The event bus has `KindSessionStart`, but the lifecycle hook system fires from the dispatcher's `preInvoke`/`postInvoke` — which are tool-centric. A new publish point is needed outside the dispatcher.
4. **Matcher**: What does a `matcher` regex match on a session event? There's no tool name. Options: match on agent name, model name, or drop matcher for session events.

### Tasks

#### 4.1 — Design session event payload

Define the `Payload` fields for session events. Must not break the existing tool-centric path.

#### 4.2 — Wire publish site to hook execution

Add a new function (analogous to `runStopHookEvent`) that fires `SessionStart` hooks at session initialization, before any tool calls.

#### 4.3 — Update parser and defaults

- Remove `SessionStart` from `deferredEvents`.
- Add to `V1Events()`.
- Define `eventDefaults`: timeout=10s, `on_timeout=block` (blocking — a failed session setup should prevent the session).

#### 4.4 — Add tests

- Test that `SessionStart` hooks fire at session initialization.
- Test that a denied `SessionStart` blocks the session.
- Test that `SessionStart` has no tool name or input.

---

## R10 — `SubagentStart`/`SubagentStop` Lifecycle Events

### Current State

- `deferredEvents["SubagentStart"]` = `"not implemented in v1"`
- `deferredEvents["SubagentStop"]` = `"not implemented in v1"`
- `internal/events/event.go:20` defines `KindSubagentStart`
- `internal/cli/subagent_tracker.go:56` handles `KindSubagentStart` for UI tracking

### Why This Matters

Currently, the only way to control subagent behavior is:
1. Gate subagent *tool calls* via `PreToolUse` hooks (hooks propagate via `Policy()` copy).
2. Restrict the subagent's tool registry (`restrictedRegistry` removes privileged tools).

You **cannot**:
- Block a specific subagent from being spawned.
- Log subagent start/stop with hook-defined scripts.
- Run environment checks before a subagent starts.

### Scope

- `internal/hooks/config.go` — promote from deferred
- `internal/subagents/multi_step.go` — add publish points at `Invoke` start and end
- `internal/hooks/exec.go` — handle subagent events (tool name = subagent name? or a new field?)
- `internal/hooks/protocol.go` — `Payload` shape for subagent events

### Design Considerations

1. **Tool name for matcher**: What does `matcher` match? The subagent handler type (`multi_step`)? The task description? The agent name? Recommend: match on the handler type name, since that's the stable, known value.
2. **Can `SubagentStart` block?**: Yes — this is the whole point. A `SubagentStart` hook that denies should prevent the subagent from running. This requires the `MultiStepHandler.Invoke` to check the hook before creating the scoped loop.
3. **Propagation**: A `SubagentStart` hook should fire from the parent dispatcher's hook session, not the subagent's. The subagent doesn't exist yet when the hook fires.
4. **Re-entry**: A `SubagentStart` hook matching `run_command` must not dispatch `run_command` (existing re-entry guard handles this via `withinHook`).

### Tasks

#### 4.5 — Design subagent event payload

Define payload fields: handler type, task prompt (truncated), parent session ID.

#### 4.6 — Add publish point in `MultiStepHandler.Invoke`

Before creating the scoped loop, fire `SubagentStart` hooks from the parent dispatcher's policy.

After the scoped loop completes, fire `SubagentStop` hooks.

#### 4.7 — Update parser and defaults

- Remove from `deferredEvents`.
- Add to `V1Events()`.
- `SubagentStart`: timeout=5s, `on_timeout=block`.
- `SubagentStop`: timeout=5s, `on_timeout=allow`.

#### 4.8 — Add tests

- Test that `SubagentStart` hooks fire before subagent execution.
- Test that a denied `SubagentStart` prevents the subagent from running.
- Test that `SubagentStop` fires after completion.
- Test that a subagent's own tool calls still fire the parent's `PreToolUse` hooks (existing behavior preserved).

---

## R11 — Skill-Level Hooks

### Current State

- `validate.go` rejects all handler types except `HandlerTypeCommand`.
- The `deferredEvents` map has no skill-specific events.
- `SKILL.md` frontmatter has no `hooks:` field.
- Skill activation is probabilistic via `triggers:` phrases.

### Why This Matters

Skills are the model's way of selecting a workflow. A `bug-audit` skill, for example, should ideally auto-activate a `PreToolUse` hook that blocks `write_file` during the audit — ensuring the auditor is truly read-only. Currently, this relies on the model respecting the skill's instructions (probabilistic, not deterministic).

### Scope

- `internal/agents/skill_policy.go` — skill activation system
- `internal/hooks/config.go` — new handler types or skill-triggered hooks
- `internal/hooks/validate.go` — accept new handler types
- `internal/hooks/exec.go` — implement new handler types
- `.mivia/skills/*/SKILL.md` — new `hooks:` frontmatter field

### Design Considerations

1. **Handler types beyond `command`**: The deferred handler types are `prompt`, `agent`, `http`, `mcp_tool`. Each adds a nested call with its own cost, timeout, and injection surface. This is explicitly deferred.
2. **Skill-triggered hooks vs skill frontmatter hooks**: Two approaches:
   - **Skill frontmatter `hooks:`**: A `SKILL.md` declares hooks that fire when the skill activates. This is deterministic (the skill system knows when it activates), but adds coupling between skills and the hook runtime.
   - **Skill-activated hook config**: The hook config uses a `matcher` on the skill name, not the tool name. This requires a new event type (`SkillActivate`) and changes to the matcher semantics.
3. **Composition with existing hooks**: Skill-level hooks should compose with tool-level hooks, not replace them. A `PreToolUse` hook from the user config and a `PreToolUse` hook from the active skill should both fire.

### Tasks

#### 4.9 — Design skill hook activation model

Decide between frontmatter hooks, skill-event hooks, or a hybrid. Document the tradeoffs.

#### 4.10 — Define `SKILL.md` `hooks:` frontmatter schema

Example:
```yaml
---
name: bug-audit
hooks:
  - event: PreToolUse
    matcher: write_file|search_replace
    handler: ./skills/bug-audit/block-writes.sh
    timeout: 5
    on_timeout: block
---
```

#### 4.11 — Wire skill hooks into the hook runner

When a skill activates, register its declared hooks in the session. When the skill deactivates, remove them. Ensure they compose with user-config hooks.

#### 4.12 — Add tests

- Test that skill activation registers hooks.
- Test that skill deactivation removes hooks.
- Test that skill hooks and user hooks both fire on matching tool calls.
- Test that a skill hook denial blocks the tool call.

---

## Exit Criteria

- `SessionStart` fires at session initialization with session-scoped payload
- `SubagentStart`/`SubagentStop` fire at subagent dispatch with handler-type matcher
- Skill-level hook design document complete (implementation may be split into its own phase)
- All new events have tests, docs, and example configs
- `deferredEvents` updated to reflect promoted events
- `make verify` passes
