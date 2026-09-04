---
id: viewer_surfaces_must_agree
title: Every event kind must reach every viewer, or say why not
content: An event kind is produced once and rendered on four surfaces; all four must agree.
importance: high
tags: [events, ui, invariants]
updated: 2026-09-04
---

# Every event kind must reach every viewer, or say why not

An event kind is produced once and rendered on four surfaces: the TUI
(`internal/uiadapter` translation), the subagent dialog
(`SubagentTranscriptConversation.applyEvent`), the local `--json` NDJSON
stream (`jsonTurnEventCallback`), and the cross-process relay
(`renderExternalEvent`). The chat-sync wire is a fifth consumer with its own
recorded contract.

**The trap.** Each surface has its own tests, and each of those tests drives
the kinds its author remembered. Nothing asked whether the surfaces agree
about which kinds exist. That gap shipped four times in one body of work:

- `assistant_reset` reached the chat-sync projector and no other viewer. The
  TUI logged it as unknown, the plain renderer had no arm, and both NDJSON
  surfaces sent the retry's answer with nothing saying it replaced anything.
- `subagent_begin` was relayed across processes and absent from this process's
  own `--json`.
- A kind was added to `hub.RelayedKinds` with no arm in the renderer, so it
  was dropped in silence after crossing the process boundary.
- Later the mirror: an arm whose allowlist entry nothing tested, so deleting
  the entry failed nothing.

The guard that should have caught the first one was a hand-written list of
every `EventKind` sitting beside the exhaustiveness test that read it. The
kind was never written into the list, so the test skipped it and stayed green.

**What is in place now.**

- `agent.AllEventKinds()` (`internal/agent/event_registry.go`) is the one
  source of the set, and `TestAllEventKindsMatchesTheDeclaredConstants` checks
  it against the PARSED source of the constant block. Adding a constant fails
  a test rather than being ignored by one.
- `TestEveryEventKindReachesEveryViewerOrSaysWhyNot`
  (`internal/clichat/viewer_surface_conformance_test.go`) drives every kind
  through every surface: the TUI translation, the local `--json` writer, the
  cross-process relay, and the chat-sync wire the web app reads. A surface that renders nothing for a kind must
  declare it in `.mivia/policy/viewer-surfaces.json` **with a reason**. A
  declared kind that DOES render also fails, so a stale entry cannot hide the
  next real gap.
- `TestTheRelayAllowlistAndItsRendererAgree` holds the two halves of
  cross-process delivery to each other: allowlist without an arm, or an arm
  without an allowlist entry, both fail.
- The same policy file records the remaining KNOWN GAPS rather than hiding
  them. `hook` was one and is now rendered on `--json` and on the chat-sync
  wire. `tool_pending` is still one on every surface but the TUI: no program
  parsing `--json`, and no remote viewer, can tell that a turn is blocked
  waiting for the operator. Closing that needs an answer path, not just a
  row.

**When you add an event kind:** expect to touch the policy file. That is the
gate working, not noise - it is asking you to decide for each surface instead
of deciding for one and defaulting the rest to silence.

**What it still cannot catch.** The subagent dialog is not in the table (its
"did anything render" signal is a history diff rather than a byte count), and
`internal/ui/stream` is not reachable from `cmd/mivia` at all, so its arms are
unexercised by any user path.

**The table shipped one surface short.** The chat-sync wire - the one a REMOTE
reader uses - was missing for two commits while the gate's own message claimed
the class was closed. A review found it. When you add a viewer, add it to the
table in the same change: a gate that covers three of four surfaces reads as
covering all of them.
