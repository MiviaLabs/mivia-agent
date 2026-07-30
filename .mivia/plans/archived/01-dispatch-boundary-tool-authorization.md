# 01 — Dispatch-boundary tool authorization

**Status:** ✅ Completed (2026-07-29).

> **M3's mutation proof was missing and has since been written.** A staleness
> check on 2026-07-30 found `TestExecuteToolTaskRejectsToolMissingFromRegistry`
> (§7, M3) did not exist even though this plan was marked complete — the guard
> shipped, its proof did not. It now exists in `internal/agent/registry_guard_test.go`
> and was verified to fail when the `reg.Get` check is removed. The other five
> mutation-proof tests were confirmed present.
**Date:** 2026-07-29
**Commit:** `f0bf99b security(agent): enforce nested tool authorization`
**Depends on:** nothing. **Blocks:** `05`, `06`, `07`, `08`.
**Blast radius:** HIGH (security + concurrency).

---

## 1. The vulnerability

`MultiStepHandler.setupAgentLoop` (`internal/subagents/multi_step.go:200-203`) builds `agent.Loop{Tools: h.restrictedRegistry()}` but passes `Dispatcher: h.Dispatcher` (`multi_step.go:84`) — the **parent's** dispatcher.

In `internal/agent/loop_tools.go`, the `reg` parameter is used only for `reg.Capability()` (`:290`) and `OpenAITools()` specs. Execution is:

```go
// loop_tools.go:374
r := opts.Dispatcher.Invoke(task.callCtx, runtime.Request{
    ID: task.call.ID, ..., Kind: runtime.Tool, Name: task.call.Function.Name, ...})
```

`toolHandler` closes over the **full** `*tools.Registry` (`internal/runtime/tools.go:11-16`) and `Dispatcher.Register` unconditionally sets `d.policy.Allow[k][name] = true` (`internal/runtime/dispatcher.go:162-168`), so the `allowed` check at `:252-256` always passes.

**A subagent that emits `spawn_agent`, `delegate`, or `inspect_agents` executes it.** There is a correct fallback at `loop_tools.go:183` (`runtime.NewToolDispatcher(reg, ...)`) but it fires only when `opts.Dispatcher == nil`, which subagents never are.

Existing tests (`multi_step_test.go:130-200`) assert registry *membership* only — never execution. The bypass is untested.

### Secondary bug fixed by the same change

`req.ID` is the **model-supplied** `task.call.ID` (`loop_tools.go:375`), and `d.completed`/`d.fingerprints` are dispatcher-global (`dispatcher.go:233-237`). Two concurrent subagents on the shared parent dispatcher both emitting `call_1`: identical input ⇒ subagent B receives **subagent A's tool output**; differing input ⇒ hard error `invocation id reused with different input`. Per-subagent dispatchers isolate the ID space.

---

## 2. Decision: option (B) — per-subagent dispatcher

Build the child's dispatcher from the child's restricted registry at **invoke** time. The pairing of registry and dispatcher *is* the authorization boundary.

### Options rejected

| Option | Why not |
|---|---|
| **(A)** `Request.AllowedTools` / caller identity | Every caller must remember to populate it; forgetting is silently permissive (fail-open) — the exact bug class being fixed. Breaks resume: `recovery.go:99-103` reconstructs `subagents.Task{ID, Name, DependsOn}` only and `ledger.TaskSnapshot` (`coordinator/spawn.go:127`) has no scope column, so it needs a SQLite migration or resumed tasks silently get full scope. |
| **(C)** per-caller `Policy.Allow` | Needs a caller identity `Request` lacks, keeps the fail-open `Register` auto-allow, same persistence problem as (A). Largest diff, latest payoff. |
| **(D)** enforce in `agent.Loop` only | `Pool.executeOne` (`subagents.go:205`) and `skills.go:79` call `Dispatcher.Invoke` directly and bypass it. Good as a second layer, insufficient as the layer. Adopted as a backstop (§3d). |

### Hidden-blocker hunt (all cleared)

| Candidate | Verdict |
|---|---|
| Parent `Policy` lost (`MaxDepth`, byte caps, `Sink`) | `cli/dispatcher.go:74-77` sets only `MaxDepth`/`MaxBudget`; byte caps fall to identical `New()` defaults (`dispatcher.go:92-97`); `Policy.Sink` is never set in non-test code. Fixed regardless by a `Policy()` accessor. |
| Budget counter resets | Real but inert: `config.DefaultBudget = 0` ⇒ `MaxBudget=0` ⇒ enforcement off. Keying is *already* incoherent (`Pool.executeOne` charges `ParentID`, nested calls charge `TurnID`). Accepted, explicitly scoped out. |
| Dispatcher holds handlers absent from the registry | Not for `Kind=Tool`. There are **four** registration paths — `NewToolDispatcher`, `registerSessionTool` (`cli/dispatcher.go:169-178`, writes to both), `registerOneShotHandlers`/`registerMultiStepHandler` (`:103-144`), `registerSkillHandlers` (`:146-157`) — but only the first two register `Kind=Tool`, and the agent loop issues only `Kind=Tool` (`loop_tools.go:380`). No MCP registration exists. **Stated this way deliberately: a future MCP or skill-as-tool path would falsify a "only two paths" premise silently.** |
| Chicken-and-egg with `registerDelegationTools`/`registerOrchestrationTools` | **Eliminated.** Those run at session construction and need `d` before `reg` is complete. (B) builds the child dispatcher at *invoke* time, when `h.FullRegistry` is fully populated. |
| `coordinators` sync.Map keyed by dispatcher; `orchestrationHandleAccessible` | Reached only from orchestration tools, which subagents never get. |

---

## 3. Changes

### 3a. `internal/runtime/dispatcher.go` (434 → ~443)

Add after `Allow` (`:148`):

```go
// Policy returns a copy of the effective policy so a caller can derive a
// scoped child dispatcher that inherits limits and telemetry without
// inheriting the parent's handler set. Allow is deliberately dropped: the
// child rebuilds its own allow map from its own registry.
func (d *Dispatcher) Policy() Policy {
	d.mu.Lock()
	defer d.mu.Unlock()
	p := d.policy
	p.Allow = nil
	return p
}
```

Additive; no behavior change to existing paths.

### 3b. `internal/subagents/multi_step.go` (223 → ~265)

Replace `setupAgentLoop` (`:187-204`) with a pairing constructor:

```go
// scopedLoop pairs the restricted registry with a dispatcher built from that
// same registry. The pairing is the authorization boundary: a nested agent
// cannot execute a tool that is not in the registry it was shown.
type scopedLoop struct {
	loop       *agent.Loop
	dispatcher *runtime.Dispatcher
}

func (h *MultiStepHandler) newScopedLoop() (*scopedLoop, error) {
	reg := h.restrictedRegistry()
	d, err := runtime.NewToolDispatcher(reg, h.parentPolicy())
	if err != nil {
		return nil, fmt.Errorf("scoped tool dispatcher: %w", err)
	}
	return &scopedLoop{
		loop:       &agent.Loop{Completer: h.Completer, Tools: reg},
		dispatcher: d,
	}, nil
}

// parentPolicy inherits the session's limits without its handlers.
func (h *MultiStepHandler) parentPolicy() runtime.Policy {
	if h.Dispatcher == nil {
		return runtime.Policy{}
	}
	return h.Dispatcher.Policy()
}
```

In `run` (`:64`): build the scoped loop, `defer scoped.dispatcher.Close()`, set `Dispatcher: scoped.dispatcher` at `:84`.

Rewrite the `Dispatcher` field doc (`:28-30`) — it currently **documents the vulnerability** ("preserves the parent's policy … for nested tool execution"). New text: *"Dispatcher is the parent session dispatcher. It is used **only** as a policy source; nested tool execution runs on a scoped dispatcher built from the restricted registry."*

**Function-length watch:** `run` is 76 lines (`:64-139`); +5 crosses the soft 80. Extract the result-marshalling tail (`:114-138`) into `buildResult(...)`. `run` lands ~55.

`h.Dispatcher` must stay nil-tolerant — `internal/cli/delegation_test.go:468` constructs a handler without one.

### 3c. Drift guard — `PrivilegedTool` marker

`restrictedRegistry` (`:207-220`) is a hardcoded name denylist; any future orchestration tool silently reopens the hole (rule 20 Critical Drift Guard).

`internal/tools/tools.go` (377 → ~383):

```go
// PrivilegedTool marks a tool that must never be exposed to a nested agent
// (delegation and orchestration control). The marker travels with the tool;
// name lists drift.
type PrivilegedTool interface{ Privileged() }
```

One-line `func (t *xTool) Privileged() {}` on `delegateTool`, `dispatchTasksTool`, `spawnAgentTool`, `inspectAgentTool`, `joinRunTool`, `cancelRunTool` (`cli/orchestrate_lifecycle.go:34,148`).

`restrictedRegistry` skips `blocked[name] || implements PrivilegedTool`. **Keep the name map** so `multi_step_test.go:174-189` (which tests names absent from the default registry) still passes.

### 3d. `internal/agent/loop_tools.go` (397 → ~407) — backstop

At the top of `executeToolTask` (`:348`, 50 → 59 lines), **before** `scheduler.acquire` so no release is needed:

```go
	// Defense in depth: the dispatcher is the authorization boundary, but a
	// caller that hands us a dispatcher wider than l.Tools must not silently
	// gain reach. reg is the tool set this agent was shown.
	if _, ok := reg.Get(task.call.Function.Name); !ok {
		err := fmt.Errorf("tool %q is not available to this agent", task.call.Function.Name)
		results[idx] = toolExecResult{index: idx, toolCall: task.call,
			result: "error: " + err.Error(), err: err}
		emitToolEnd(opts, results[idx])
		if finished != nil {
			finished.Add(1)
		}
		return
	}
```

This finally gives the `reg` parameter teeth. Verified safe: every caller, production and test, builds its dispatcher from the same registry as `Loop.Tools` (`agent/hang_regression_integration_test.go:239-245, 282-288, 344-350`).

---

## 4. Unchanged by design

- **`registerDelegationTools` / `registerOrchestrationTools`** (`cli/dispatcher.go:159-178`, `orchestrate.go:380-393`) — untouched. The root session loop still gets `Dispatcher: s.Dispatcher` (`chat/session.go:276-278`) with full reach, by design.
- **Coordinator** (`spawn.go`, `dag.go`, `retry.go`) — zero changes. The root orchestrator's authority to spawn is not what is being restricted.
- **`subagents.Pool`** (`subagents.go:205-209`) — zero changes; scope is not a Task property under (B).
- **Resume** (`recovery.go:99-103`) — zero changes, and this is the decisive advantage. Scope is derived at invoke time from `FullRegistry` + `restrictedRegistry()`, so a task reconstructed with only `{ID, Name, DependsOn}` gets an identical scope. **No ledger field, no SQLite migration.** Options (A) and (C) both require that migration or silently fail open on resume.

---

## 5. Depth propagation — deliberately deferred

`tools.Tool.Execute(ctx, args)` has no access to the invoking `runtime.Request`, so `buildTasks` (`dispatch.go:231-236`) and `spawnAgentTool.Execute` (`orchestrate.go:172-180`) leave `Task.Depth` unset. Guards at `dispatcher.go:225` and `subagents.go:94` never trip across hops.

**Correct fix (own commit, after this):** `Dispatcher.execute` (`:290-294`) wraps `callCtx` with `runtime.ContextWithDepth(ctx, req.Depth)`; the three task-building tools read `runtime.DepthFrom(ctx)+1`. ~30 LOC.

**Why separate:** once `01` lands, a subagent cannot reach `dispatch_tasks`/`spawn_agent`/`delegate` at all, so maximum nesting is structurally exactly 2 (root loop → subagent loop). `MaxDepth` becomes redundant rather than broken. Bundling doubles the diff and mixes a correctness fix into a security fix, muddying both the regression test and the revert story.

---

## 6. Blast radius

**Existing tests pass unchanged** (verified by reading):

- `subagents/subagent_integration_test.go:135, 219, 297, 356, 433` — all build `toolDisp` from the same `reg` they pass as `FullRegistry`, and `tools.NewDefaultRegistry` contains no delegation tools, so `restrictedRegistry() == full set` there. Behaviorally identical.
- `subagents/multi_step_test.go:131, 174` — test `restrictedRegistry()` directly; §3c keeps the name map.
- `cli/delegation_test.go:407, 433, 454` — root-loop and `Dispatcher: nil` paths.
- `agent/hang_regression_integration_test.go` — dispatcher and `Loop.Tools` share a registry; the §3d backstop never fires.
- `runtime/dispatcher_test.go` — `Policy()` is additive.

**Nothing relies on the permissive behavior.** Grep of `subagents/*_test.go` for `spawn_agent|dispatch_tasks` returns only the negative assertions in `multi_step_test.go`. No test asserts a subagent successfully executes a privileged tool.

### New tests

| Test | Package | Pins |
|---|---|---|
| `TestMultiStepHandlerScopedDispatcherRejectsPrivilegedTool` | `subagents` | **Reproduction test.** Stub completer emits `spawn_agent` inside the subagent; the privileged tool **is** in `FullRegistry`; assert `not available to this agent` and that the side effect never happened. |
| `TestMultiStepHandlerScopedDispatcherIsNotParent` | `subagents` | `opts.Dispatcher != h.Dispatcher` (via `Has(Tool,"spawn_agent") == false`). |
| `TestMultiStepHandlerScopedDispatcherInheritsPolicy` | `subagents` | Parent `MaxOutputBytes` ⇒ child enforces `output budget exceeded`. |
| `TestMultiStepHandlerConcurrentSubagentsDoNotShareToolCallIDs` | `subagents` | Two concurrent subagents both emitting `call_1` with different inputs each get their own result. |
| `TestDispatcherPolicyDropsAllowMap` | `runtime` | `Policy().Allow == nil`. |
| `TestExecuteToolTaskRejectsToolMissingFromRegistry` | `agent` | §3d backstop fires when dispatcher ⊋ registry. |
| `TestRestrictedRegistryExcludesPrivilegedMarker` | `subagents` | A `PrivilegedTool` under an unlisted name is still stripped. |
| `TestSessionToolsImplementPrivilegedTool` | `cli` | All six session tools carry the marker. |

`TestMultiStepHandler*` matches the `make invariants` `-run` regex (`Makefile:129-133`); **`TestExecuteToolTask*`, `TestDispatcherPolicy*`, `TestRestrictedRegistry*`, `TestSessionTools*` do not.** Add them to the regex — `scripts/validate_invariants.py` only checks that a named test *exists*, so a manifest test that never runs under `make invariants` still passes `validate-invariants` cleanly. Add `Makefile` to the change list.

**Concurrency note for `concurrency-review`:** `tools.Registry` has no mutex (`tools.go:47-64`). This change makes `h.FullRegistry.List()` run concurrently across fan-out — read-only and safe today, but it becomes a data race the moment anything registers dynamically (see `08` §2's conditional registration). Record the constraint.

### Mutation proofs (rule 20 — required)

| # | Mutation | File | Test that MUST fail |
|---|---|---|---|
| M1 | Revert `Dispatcher: scoped.dispatcher` → `h.Dispatcher` | `multi_step.go` ~84 | `...RejectsPrivilegedTool`, `...IsNotParent` |
| M2 | Build child dispatcher from `h.FullRegistry` instead of `reg` | `multi_step.go` `newScopedLoop` | `...RejectsPrivilegedTool` |
| M3 | Delete the `reg.Get(...)` guard | `loop_tools.go` ~349 | `TestExecuteToolTaskRejectsToolMissingFromRegistry` |
| M4 | `Policy()` returns `Allow` intact | `dispatcher.go` ~150 | `TestDispatcherPolicyDropsAllowMap` |
| M5 | `parentPolicy()` returns `Policy{}` unconditionally | `multi_step.go` | `...InheritsPolicy` |
| M6 | Drop the `PrivilegedTool` check, rename `spawn_agent` in the fixture | `multi_step.go` ~215 | `TestRestrictedRegistryExcludesPrivilegedMarker` |
| M7 | Remove `func (t *spawnAgentTool) Privileged() {}` | `cli/orchestrate.go` | `TestSessionToolsImplementPrivilegedTool` |
| M8 | Remove `defer scoped.dispatcher.Close()` | `multi_step.go` `run` | **none — residual risk.** GC-reclaimed, no global retains it. Named per rule 20. |

### Invariant

Add to `.mivia/invariants.md`:

```
| INV-AG-7 | Safety | Nested agents execute only tools present in their restricted registry; scoping is enforced at the dispatcher, not advertised | `TestMultiStepHandlerScopedDispatcherRejectsPrivilegedTool`, `TestMultiStepHandlerScopedDispatcherIsNotParent`, `TestExecuteToolTaskRejectsToolMissingFromRegistry` | |
```

Commit body carries `Regression: INV-AG-7 (TestMultiStepHandlerScopedDispatcherRejectsPrivilegedTool)` and the mutation-proof results.

---

## 7. User-visible behavior

**Effectively none for well-behaved runs; exactly the attack case changes.**

A subagent was never *shown* the delegation tools (specs come from `Loop.Tools.OpenAITools()`), so it emits them only by hallucination or prompt injection from tool output — precisely the live exploit. Those calls change from silently succeeding to `error: tool "spawn_agent" is not available to this agent`: a legible, model-recoverable tool error.

**Scoping nuance, stated plainly:** `restrictedRegistry` strips **only** delegation/orchestration tools. `write_file` and `run_command` remain available to subagents — `multi_step_test.go:147,189` assert this today, and that is unchanged. This plan delivers the **enforcement mechanism**; narrowing the list itself is `05`.

No config, flag, ledger, or persisted-state change. Revertible by one line (M1).

---

## 8. Verification

```bash
python3 scripts/git-hooks/check-commit-subject \
  "security(agent): scope subagent tool execution to its own dispatcher"

go build ./... && go vet ./...
go test ./internal/runtime/... ./internal/subagents/... ./internal/agent/... ./internal/cli/...
go test -race ./internal/runtime/... ./internal/subagents/... ./internal/agent/... ./internal/cli/...
make invariants
make validate-invariants
make structure-check
make mutation-coverage
make verify
```

Skills: `secure-change`, `concurrency-review`, `bug-audit`, `verify-code-change` (blast radius HIGH).

**ADLC RED gate:** `TestMultiStepHandlerScopedDispatcherRejectsPrivilegedTool` must be observed **failing against unmodified code** before any production edit. That failing run is the proof the vulnerability is live.

**Rollback criterion:** if per-subagent dispatcher construction shows measurable per-invoke cost, namespace `req.ID` per invocation (`req.ID = invocationPrefix + task.call.ID`) on a shared dispatcher. **Never cache a dispatcher across concurrent subagent invocations** — under `07` §2 there is one `MultiStepHandler` per role, so a `(handler, registry)`-keyed cache is one dispatcher shared by every concurrent invocation of that role, which reinstates the §1 shared-ID-space bug and would fail `TestMultiStepHandlerConcurrentSubagentsDoNotShareToolCallIDs`.

The performance premise is also unfounded: `restrictedRegistry()` already runs per invoke (`multi_step.go:202`); the delta is `New()` plus ~12 map inserts (`runtime/tools.go:17-28`).
