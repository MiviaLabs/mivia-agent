---
id: two_paths_execute_a_tool_call
title: Two code paths execute a model's tool call, and they share no interface
content: dispatcherShim.Run and runDeferredToolNow are the same capability written twice; every contract must hold on both, and the conformance table in internal/clichat is what checks that.
importance: high
tags: [tools, execution, conformance, drift, approvals, deferred]
---

# Two paths execute a tool call. Change one, check the other.

## The two

- **Admitted** - `dispatcherShim.Run` (`internal/agent/sdk_dispatcher_shim.go`).
  Serves a tool the model may already call.
- **Deferred** - `serveUnadmittedTool` → `runDeferredToolNow`
  (`internal/chat/session_turn_surface.go`). Serves a tool that is advertised
  but not yet admitted, so it invokes the dispatcher DIRECTLY, underneath the
  SDK registry where the wrappers live.

They share no Go interface: one is an `sdktools.Tool` method, the other an
eight-parameter `Session` method. Nothing makes the compiler, or any
per-interface gate, treat them as siblings.

## What that cost

On 2026-09-02 the deferred path honoured **four of nine** contracts. The other
five each shipped as a separate bug with an unrelated-looking symptom:

- no approval at all → a write tool ran under a `deny` policy;
- then approval, but no prompt → the turn HUNG with nothing on screen;
- no per-call timeout → an unbounded deferred `run_command`;
- no `SkipDedup` → a read-class tool answered from a stale record;
- failure reported as "not yet loaded, retry" → duplicate side effects;
- outcome recorded as success → a refusal rendered as a green tool call;
- no denylist → an operator-denied tool executed.

Nine fixes, one defect. See DC-35.

## What is in place now

`internal/clichat/tool_execution_conformance_test.go` drives BOTH paths
through the real attach path and a real session turn - the tier decides which
path a call takes - and asserts the same contracts on each. Divergences are
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

**When you add a contract to the admitted path, add a row to the table in the
same change.** That is the whole point: the table is the only place the two
paths are written down as the same capability.

Related: [[sibling-implementations-drift]], [[viewer-surfaces-must-agree]],
[[synchronous-fakes-cannot-see-a-hang]].
