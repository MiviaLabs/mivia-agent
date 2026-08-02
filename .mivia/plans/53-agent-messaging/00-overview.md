# 53 - Agent-to-agent messaging: program overview

**Status:** DESIGN - ADLC Step 0 (architecture-review) not run on any member.
Nothing here is implementation-ready.
**Date:** 2026-08-02
**Depends on:** nothing hard; coordinates with `45` (v2 lifecycle events) -
see §5. Phase members depend on each other strictly in order.
**Blocks:** nothing.
**Blast radius:** program-level HIGH. Touches the coordinator, the subagent
pool, the event bus, the ledger, and adds model-visible channels in both
directions.

## 1. Thesis

Today a delegated task is one-shot in both directions: the parent's entire
influence is the spawn-time `subagents.Task.Input`, and the child's entire
model-visible output is the final tool-result envelope (`dispatchTaskResult`,
`internal/cli/dispatch.go:248`). Progress events exist but reach only the TUI
(`OnEventForMultiStep`, `internal/cli/dispatcher.go:382`); the parent model
never sees them. There is no way for a child to ask a question, surface a
discovery mid-flight, or for the parent to steer a running child short of
cancellation.

This program adds **structured, sparse, typed messaging** - deliberately not
free-form agent chat. The design follows the external evidence (Cognition,
Anthropic multi-agent research system, A2A v1.0 task lifecycle): rich briefs
and compressed reports stay the primary channel; messaging is an escalation
and coordination channel with a fixed vocabulary, byte budgets, and
parent-routed peer communication only.

Shapes stolen, protocols not adopted: A2A's task state machine (notably
`input-required`), MCP elicitation's typed question/answer, actor-mailbox
delivery at step boundaries.

## 2. Members

| File | Subject | Blast radius |
|------|---------|--------------|
| `01-message-envelope-and-bus.md` | Typed message envelope, event kinds, budgets, config; no behavior change | MEDIUM |
| `02-child-to-parent.md` | `Finding` / `Question` upstream; blackboard on the ledger; parent read tools | HIGH |
| `03-parent-to-child.md` | Per-task mailbox; `Steer` / `Answer` delivery at step boundaries; `send_to_task` tool | HIGH |
| `04-peer-referral.md` | Parent-routed `Ask` between named-role agents (reviewer/auditor panels) | MEDIUM |

Strict sequencing: `01 → 02 → 03 → 04`. `04` is built only if real usage of
`02`+`03` demonstrates the need; it is designed now so `01`'s envelope does
not have to change later.

## 3. Non-goals

- **No free-form agent chat room.** Every message is typed, budgeted, and
  attributable. A shared conversational space is explicitly rejected:
  context pollution, untrained peer-negotiation behavior, and convergence
  problems (see program research, §6).
- **No direct child↔child channel.** Peer communication is always routed
  through the parent's router (`04`), which may decline or transform.
- **No nested delegation changes.** Subagents still never receive
  delegation/orchestration tools (`registerSessionTool`,
  `internal/cli/dispatcher.go:364`); messaging tools follow the same scoping
  discipline.
- **No wire protocol.** In-process Go channels and the ledger. If subagents
  are ever exposed externally, the envelope maps onto A2A messages cheaply;
  that mapping is out of scope.

## 4. Invariants this program must not break

- **Fingerprint stability (INV via `coordinator/spawn.go`):** the coordinator
  fingerprints work-defining `Task` fields for idempotency. Mailbox handles
  and message state must never enter the fingerprint - they travel via
  context or non-fingerprinted fields, and this must be tested.
- **Principal scoping (INV-AG-9):** message send/read tools are gated by
  `orchestrationHandleAccessible` exactly like `join_run`/`cancel_run`.
- **Ledger refs recorded, never re-minted (INV-AG-10):** blackboard entries
  are content-addressed ledger writes; messages reference them by ref.
- **Fixed termination vocabulary:** `terminationReason`
  (`internal/cli/dispatch.go:397`) must not become a content channel;
  a blocked-on-question task terminates with a new fixed reason, with the
  question itself living in the ledger.
- **Byte discipline:** every model-visible message counts against explicit
  budgets in the style of `inline_output_bytes` /
  `batch_result_budget_bytes`; oversize payloads go to the ledger with a
  synopsis, mirroring the existing `output_ref` pattern.
- **One result per task (INV-AG-21):** a task that dies while parked on a
  question still emits its result envelope with an existing
  fixed-vocabulary reason; the new `awaiting_input` status must not
  collide with the terminal `blocked` status that invariant already uses.
- **Concurrency and heartbeat rules:** `.mivia/rules/50-concurrency-subagents.md`
  and `.mivia/rules/70-long-running-heartbeat.md` govern all new goroutines
  and blocking states.

## 5. Relationship to plan 45 (v2 lifecycle events)

Plan 45's `SubagentStart`/`SubagentStop` hooks and this program both extend
the same seams (`OnEventForMultiStep`, coordinator lifecycle). They are
independent but should not race each other in the same files; whichever
lands second re-verifies the forwarding switch in
`internal/cli/dispatcher.go:386-410`. Message events deliberately reuse the
`ledger.LifecycleEvent` extension point identified there rather than
inventing a parallel event stream.

## 6. Evidence base (research summary, 2026-08)

- A2A v1.0 (Linux Foundation): cross-network protocol; its task lifecycle
  (`working / input-required / completed / failed / canceled`) is adopted as
  the child state model. ACP merged into A2A; not relevant standalone.
- Cognition ("Don't Build Multi-Agents" and 2026 follow-up): free-form
  inter-agent chatter fails; single-writer + read-only intelligence-
  contributing agents works; share compressed structured findings.
- Anthropic multi-agent research system: orchestrator-worker with detailed
  briefs; shipped with no mid-task steering, flagged async steering as
  state-consistency risk - which is why `03` delivers only at step
  boundaries and keeps delivery semantics trivial.
- Framework survey: mailbox/actor (AutoGen 0.4 runtime, Claude Code
  SendMessage), blackboard (LangGraph channels), interrupt/input-required
  (LangGraph `interrupt`, A2A). All three appear here in their minimal form.

## 7. Program-level open decisions

Each member lists its own; these cut across:

1. **Does `Question` block the child?** Position: yes in `02` (child parks in
   a new `blocked_on_question` attempt state, wall-clock still running,
   timeout answerable-by-default). Non-blocking parking is explicitly
   deferred.
2. **Parent attention model.** The parent is itself an LLM turn-taker; it
   only observes messages when it next runs. `02` therefore makes upstream
   messages *pull* (surfaced in `join_run`/`inspect_agents`/new read tool and
   appended to the final result envelope), never push-injected into the
   parent's in-flight context.
3. **Retention.** Messages and blackboard entries live in the run ledger and
   share its retention story; nothing new to expire.
