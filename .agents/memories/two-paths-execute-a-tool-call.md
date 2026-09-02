---
id: two_paths_execute_a_tool_call
title: Two code paths execute a model's tool call, and they share no interface
content: execution is now ONE implementation (dispatcherShim.Run); the deferred path decides admission and hands the tool to the loop. The conformance table in internal/clichat holds both routes to the same contracts.
importance: high
tags: [tools, execution, conformance, drift, approvals, deferred]
---

# Two paths execute a tool call. Change one, check the other.

## How it works NOW

There is ONE execution implementation: `dispatcherShim.Run`
(`internal/agent/sdk_dispatcher_shim.go`). Both routes reach it.

- **Admitted** - the model calls a tool already in the SDK registry.
- **Deferred** - the model calls a tool that is advertised but not yet
  admitted. `serveUnadmittedTool` (`internal/chat/session_turn_surface.go`)
  makes the ADMISSION decision - advertised? resolve it, honour the denylist,
  approve it, charge the attempt, stage the publication, install the handler -
  and hands the tool back as `UnadmittedToolResult.Execute`. The loop then
  runs it through `agent.RunUnadmittedTool`, which builds the same shim.

**The host decides, the loop executes.** Approval stays on the host side
because it must happen before the admission attempt is charged; execution
belongs to the shim because that is where the contracts live.

## What it cost before that

Until 2026-09-02 the deferred path invoked the runtime dispatcher itself, so
it was a second implementation and honoured **four of nine** contracts. The
other five each shipped as a separate bug with an unrelated-looking symptom:

- no approval at all → a write tool ran under a `deny` policy;
- then approval, but no prompt → the turn HUNG with nothing on screen;
- no per-call timeout → an unbounded deferred `run_command`;
- no `SkipDedup` → a read-class tool answered from a stale record;
- failure reported as "not yet loaded, retry" → duplicate side effects;
- outcome recorded as success → a refusal rendered as a green tool call;
- no denylist → an operator-denied tool executed.

Nine fixes, one defect. See DC-35. The second implementation is now deleted -
162 lines out of session_turn_surface.go - which is why those contracts can no
longer drift apart rather than merely being watched.

## What is in place now

`internal/clichat/tool_execution_conformance_test.go` drives BOTH ROUTES
through the real attach path and a real session turn - the tool's tier decides
which route a call takes - and asserts the same contracts on each. It is what
made the delete safe: it passed unchanged across the refactor, and a single
mutation in the shim now fails BOTH routes, which is the proof they share one
implementation. Divergences are
declared in `.mivia/policy/tool-execution-conformance.json` **with a reason**,
and a declared divergence that has gone away FAILS, so the list cannot go
stale and hide the next one.

Two divergences are recorded there today: the second identical call to a
deferred tool in one turn is intercepted by admission staging rather than
executed, and the two paths read an UNSET approval policy differently
(`NeedsApprovalLayer` treats empty as "no approval layer", the deferred path
defaults it to write-only and fails closed).

## What it still cannot catch

- **A third path.** `applyDispatcherShim` returns early when
  `opts.Dispatcher == nil`, leaving the SDK registry unshimmed: no dispatcher
  means no hooks, no dedup, no capping, no outcome record. Not reachable in a
  shipped session today - `composition` always builds a dispatcher - but
  nothing enforces that, and the failure mode is losing every contract at once
  in silence.
- **Contracts the table does not name.** `RefOnlyTools` is applied by a
  registry shim, so an operator who names a DEFERRED tool in `ref_only_tools`
  still gets its full body inline. Turn-shaping (`pass1`) and the
  `EventToolStart` "running" row are also admitted-path only.

**When you add a contract to the shim, it reaches both routes for free - but
add a row to the table anyway.** The table is what will catch the next attempt
to serve a tool call from somewhere new.

Related: [[sibling-implementations-drift]], [[viewer-surfaces-must-agree]],
[[synchronous-fakes-cannot-see-a-hang]].
