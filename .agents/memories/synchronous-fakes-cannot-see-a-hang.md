---
id: synchronous_fakes_cannot_see_a_hang
title: A synchronous fake gate or fixture cannot fail the way the real one hangs
content: Fixtures that answer instantly are green through every deadlock; a blocking dependency must be tested with something that answers only when the test releases it, and a width- or size-dependent bug needs a fixture whose size actually varies.
importance: high
tags: [testing, mutation, approvals, hangs, fixtures]
updated: 2026-09-04
---

# A fixture that always answers cannot fail the way the real thing hangs

## The pattern

A test double that returns instantly is green through every stall the real
dependency can have. `.agents/skills/gate-authoring/SKILL.md` already says
this for sockets and subprocesses. It applies just as hard to **any blocking
in-process dependency** - an approval gate, a channel handshake, a waiter map.

On 2026-09-02 the deferred-tool path was found to HANG under an interactive
approval policy: the gate was consulted, `uiadapter.Approver.gate` registered
a waiter and blocked, and no prompt was ever drawn because that path passed no
`EmitPending` (the shipped TUI arms its prompt only from the `tool.pending`
event, and nothing drains `Approver.Pending()` in live mode).

Five tests covered this path. All five passed. Every one of them used a
synchronous fake gate that returned a decision immediately, so none of them
could observe that the real gate waits for a prompt nobody raised - and the
function's own doc comment claimed it denied "rather than hang".

**The fix in a test:** the double must answer only when the test releases it,
and the test must have a timeout that is a FAILURE, not a hang:

```go
ApprovalGate: func(ctx context.Context, ...) sdkadapter.ApprovalResult {
        select {
        case <-approve:   // released by whatever should raise the prompt
                return sdkadapter.ApprovalResult{Approved: true}
        case <-ctx.Done():
                return sdkadapter.ApprovalResult{Err: "canceled"}
        }
}
```

## The same shape in size-dependent fixtures

A fixture whose measurements do not vary cannot expose a bug that depends on
them varying. The approval component's scroll offset panicked when a resize
changed the diff's row count. The first test for it passed against the
completely unfixed code, and both mutants stayed green: `previewDiff` builds
ADD-ONLY lines, which split rendering has nothing to pair, so its row count is
identical at every width. Only a diff of REPLACED lines (`pairedDiff`) halves
under split and can expose the offset.

## What to do

- Before trusting a green test on a blocking or size-dependent path, **run
  the mutation**. Break the thing, watch the test fail. A test that passes
  against the unfixed code is not evidence, and it is the only way either of
  the above was caught.
- When a fixture's whole job is to vary, assert that it varies. Measuring
  `diffTotal()` at two widths took one throwaway test and ended an hour of
  wrong assumptions.

Related: [[dispatch-protocol-hang-prevention]], [[sibling-implementations-drift]].
