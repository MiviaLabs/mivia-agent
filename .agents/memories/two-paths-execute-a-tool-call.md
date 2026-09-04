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

- ~~A third path via a nil dispatcher.~~ **Closed.** `applyDispatcherShim`
  used to return early when `opts.Dispatcher == nil`, leaving the whole
  registry unshimmed, and had two smaller degrades beside it (a
  non-SchemaTool was skipped; a failed re-Add restored the UNWRAPPED tool).
  It now refuses instead, and
  `TestNoToolReachesTheModelUngoverned` (`internal/agent`) asserts the
  outcome - every tool in a built registry is a `*dispatcherShim`, or the
  build failed. Six tests had been relying on the degrade while testing
  something else; they now wire a real dispatcher.
- **The LIVE tool_start row.** The shim emits `EventToolStart` before it
  dispatches, and `sdk_tool_events.go` ALSO synthesises one from the recorded
  outcome afterwards. So an operator sees a row either way and a conformance
  contract on "did a tool_start arrive" cannot fail - I wrote one, watched the
  mutation survive, and removed it. Telling the live emission from the
  synthesised one needs a timing assertion this harness cannot make cleanly.

  Everything else in this group is closed. `RefOnlyTools` and turn shaping are
  wrapped for the deferred path too (`wrapRefOnly`, `wrapTurnShaping`), and the
  ephemeral spool-nil rule, the result cap and the outcome record all came for
  free when execution moved into the shim.

## The table is the contract list - keep it that way

Thirteen contracts over both routes, and every one was added because a
mutation proved the previous set could not see it. If you add a rule to
`dispatcherShim.Run`, add a row. Two lessons the additions paid for:

- **A contract that reads the wrong artefact cannot fail.** The hook-BLOCK
  case reads the dispatcher's blocked envelope, not `HookContext`, so
  deleting the hook advisory entirely stayed green until a separate
  advisory case was added. Two guards need two cases.
- **So does a contract whose fixture disables what it tests.** The ephemeral
  rule is enforced twice (the ref-only wrapper skips ephemeral tools, and
  the shim nils the spool before capping); the ref-only case cannot reach
  the second, because it never gets as far as the spool.

**When you add a contract to the shim, it reaches both routes for free - but
add a row to the table anyway.** The table is what will catch the next attempt
to serve a tool call from somewhere new.

Related: [[sibling-implementations-drift]], [[viewer-surfaces-must-agree]],
[[synchronous-fakes-cannot-see-a-hang]].
