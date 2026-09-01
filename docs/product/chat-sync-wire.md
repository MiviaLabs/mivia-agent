# Remote Chat Session Synchronization

This document specifies the remote chat session synchronization wire protocol,
privacy gates, truncation budgets, outbox durability, and configuration options.

## Overview

Remote chat synchronization lets an operator monitor and interact with local
chat sessions through a web viewer. The system synchronizes events in real time
to the backend API using an append-only event stream.

## Activation

**Sync runs when you are logged in.** There is nothing to enable, no flag and
no prompt: an authenticated CLI syncs its chat sessions, and a logged-out one
does nothing at all, silently. Set `sync.enabled = false` to opt out while
staying logged in.

Sync failing is never allowed to break the local chat. Every refusal on this
path is silent by design, and a terminal stop is reported rather than hidden -
see "Reporting" below.

### Known limitation: sessions with no context store

The durable identity that lets a restart re-attach to the same remote
transcript is keyed on the chat session id. That id is restored across a
save/resume cycle for context-enabled sessions, but a **file-backed session
with no context store does not carry one** - its persisted metadata has no
session-id field. Such a session therefore mints a fresh handle, and a fresh
remote transcript, on every run.

## Fail-Closed Privacy Model

Remote synchronization uses a fail-closed posture for CONTENT. Activation
itself is not fail-closed - being logged in is the decision - but everything
sensitive inside a synced session is withheld unless you ask for it:

1. **Authentication required**: an upload without a resolvable token is
   refused before it is attempted, rather than sent anonymously.
2. **Tool I/O withheld**: By default, the system omits tool inputs and outputs
   from the wire payload and records them in the envelope `redacted` array. When
   enabled via `sync.include_tool_io = true`, payloads still pass through the
   workspace redaction policy.
3. **Thinking withheld**: The system withholds model reasoning text by default
   (`sync.include_thinking = false`).

These controls apply to a subagent's own output exactly as they apply to the
root loop's. A subagent runs in its own session, but its prose passes through
the same redaction, the same truncation budgets, and the same
`include_thinking` / `stream_assistant` settings. There is no separate
subagent switch: what leaves this machine must not have two answers.

Subagent prose was formerly withheld from the wire whatever the settings
said. That made a remote viewer strictly weaker than the local TUI - it could
list a running subagent but never open it - so the prose now ships under the
controls above. Operators who already enabled `stream_assistant` or
`include_thinking` therefore send more text than before WITHOUT changing any
setting. Set those options to `false` to withhold it.

## Truncation Budgets

To keep event payloads bounded and prevent network buffer exhaustion, string
fields use rune-safe byte budgets:

| Field | Budget | Behavior |
|-------|--------|----------|
| User Prompt | 32 KiB | UTF-8 rune-safe truncation |
| Assistant Message | 32 KiB | UTF-8 rune-safe truncation |
| Tool Output | 16 KiB | UTF-8 rune-safe truncation |
| Streaming Delta | 8 KiB | UTF-8 rune-safe truncation |
| Tool Input | 4 KiB | UTF-8 rune-safe truncation |
| Error Message | 2 KiB | UTF-8 rune-safe truncation |
| Metadata Labels | 200 B | UTF-8 rune-safe truncation |

When a field exceeds its budget, the system truncates the text cleanly at the
last valid UTF-8 rune boundary and records the retained and total byte counts
in the envelope `trunc.fields` map.

## Event Types and Ordering

The sync stream uses 18 structured `mivia.chat.v1.*` event types. The list
below is a copy for reading; the authoritative set is `KnownWireTypes` in
`internal/chatsync/wire.go`, mirrored in `api/contracts/chat-sessions.v1.json`:

- `mivia.chat.v1.turn.started`
- `mivia.chat.v1.turn.ended`
- `mivia.chat.v1.turn.failed`
- `mivia.chat.v1.assistant.delta`
- `mivia.chat.v1.assistant.message`
- `mivia.chat.v1.thinking.delta`
- `mivia.chat.v1.tool.started`
- `mivia.chat.v1.tool.ended`
- `mivia.chat.v1.subagent.ended`
- `mivia.chat.v1.subagent.progress`
- `mivia.chat.v1.subagent.tool.started`
- `mivia.chat.v1.subagent.tool.ended`
- `mivia.chat.v1.subagent.assistant.delta`
- `mivia.chat.v1.subagent.assistant.message`
- `mivia.chat.v1.subagent.thinking.delta`
- `mivia.chat.v1.context.compacted`
- `mivia.chat.v1.sync.dropped`
- `mivia.chat.v1.sync.forked`

Every subagent type shares one prefix - the version prefix followed by
`subagent.` - and a subagent's output uses its OWN types rather than the root
types with `agent` set in the envelope. A viewer can therefore keep a
subagent out of the main transcript on that prefix alone, including types
added after the viewer shipped. `envelope.agent.task` names which subagent
run an event belongs to; two runs of the same agent have different task ids.

Every session maintains a strictly monotonic sequence (`1..N`). If the system
drops events because of local queue saturation, a `sync.dropped` event consumes
a sequence number immediately before subsequent events to preserve causality.

## Outbox Durability and Conflict Handling

The system writes and persists events locally to `chat-sync/sessions/<id>/events.jsonl`
before network transmission. The local cursor `cursor.json` updates atomically
only after the remote server acknowledges receipt (HTTP 200/201).

A 409 on append means the remote session ended. Sync stops - pusher, poller
and heartbeat - and the local chat continues untouched. It does NOT fork: a
409 is not a writer conflict, and treating it as one used to mint a new remote
session and reset the sequence.

Forking happens at ATTACH time only. On attach, events the server holds past
our cursor are read back and their `writer_id` compared against ours. A
foreign writer means another machine owns the session, so the old one is ended
and a new one created, recording a `mivia.chat.v1.sync.forked` event. The API
has no writer concept of its own; it accepts a second writer's append happily,
which is why this check is done on read-back by the client.

A 400 is classified: a sequence-gap 400 re-reads the session and rebases on
`serverLastSeq+1`, because treating a recoverable gap as fatal guarantees the
failure it is trying to avoid. Any other 400 - and 413 or 422 - is poison a
retry cannot fix, so sync stops and says why. 408 and 429 stay retryable with
jittered backoff.

## Reporting

A terminal stop is announced, never silent: a stop nobody is told about is
indistinguishable from a healthy idle session. The plain CLI writes to stderr
(not stdout, which `-p` and `--json` callers parse); the TUI raises an
advisory notice. Both also report once when sync starts.

## Configuration

Configure remote chat synchronization in `.mivia/mivia.toml`:

```toml
[sync]
enabled = false             # Opt OUT. Omit the key and sync runs when logged in.
include_tool_io = false     # Withhold tool input/output
include_thinking = false    # Withhold thinking blocks
stream_assistant = false    # Per-delta streaming; off = one settled message per turn
api_url = "https://api.mivia.app"
poll_wait_seconds = 25      # Remote input long-poll timeout
heartbeat_seconds = 30      # Periodic heartbeat interval
max_unflushed = 5000        # Outbox buffer limit
```
