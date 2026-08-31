# Remote Chat Session Synchronization

This document specifies the remote chat session synchronization wire protocol,
privacy gates, truncation budgets, outbox durability, and configuration options.

## Overview

Remote chat synchronization lets an operator monitor and interact with local
chat sessions through a web viewer. The system synchronizes events in real time
to the backend API using an append-only event stream.

## Status: incomplete. Do not enable.

This feature is under construction and is **not ready to turn on**. Known
unfixed defects: the remote session id is not persisted, so each restart
starts a new server-side transcript; `serverLastSeq` is not yet authoritative;
and the writer id that separates two machines writing one session is not wired,
so two machines merge into one transcript.

Leave `sync.enabled = false`. This note is removed when those land and the
first-run consent flow ships.

## Fail-Closed Privacy Model

Remote synchronization uses a strict fail-closed privacy posture:

1. **Disabled by default**: Synchronization is inactive unless you explicitly
   set `sync.enabled = true` in configuration. There is no CLI flag: the
   settled design has `--sync`/`--no-sync`, but they are not implemented yet,
   and this document described them before they existed.
2. **Tool I/O withheld**: By default, the system omits tool inputs and outputs
   from the wire payload and records them in the envelope `redacted` array. When
   enabled via `sync.include_tool_io = true`, payloads still pass through the
   workspace redaction policy.
3. **Thinking withheld**: The system withholds model reasoning text by default
   (`sync.include_thinking = false`).

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

The sync stream uses 16 structured `mivia.chat.v1.*` event types:

- `mivia.chat.v1.turn.started`
- `mivia.chat.v1.turn.ended`
- `mivia.chat.v1.turn.failed`
- `mivia.chat.v1.assistant.delta`
- `mivia.chat.v1.assistant.message`
- `mivia.chat.v1.thinking.delta`
- `mivia.chat.v1.tool.started`
- `mivia.chat.v1.tool.ended`
- `mivia.chat.v1.subagent.started`
- `mivia.chat.v1.subagent.heartbeat`
- `mivia.chat.v1.subagent.ended`
- `mivia.chat.v1.subagent.tool.started`
- `mivia.chat.v1.subagent.tool.ended`
- `mivia.chat.v1.context.compacted`
- `mivia.chat.v1.sync.dropped`
- `mivia.chat.v1.sync.forked`

Every session maintains a strictly monotonic sequence (`1..N`). If the system
drops events because of local queue saturation, a `sync.dropped` event consumes
a sequence number immediately before subsequent events to preserve causality.

## Outbox Durability and Conflict Handling

The system writes and persists events locally to `chat-sync/sessions/<id>/events.jsonl`
before network transmission. The local cursor `cursor.json` updates atomically
only after the remote server acknowledges receipt (HTTP 200/201).

If a remote writer conflict occurs (HTTP 409 Conflict), the session forks
cleanly to a new session ID and records a `mivia.chat.v1.sync.forked` event.

## Configuration

Configure remote chat synchronization in `.mivia/mivia.toml`:

```toml
[sync]
enabled = false             # Fail-closed default. See "Status" above.
include_tool_io = false     # Withhold tool input/output
include_thinking = false    # Withhold thinking blocks
stream_assistant = false    # Per-delta streaming; off = one settled message per turn
api_url = "https://api.mivia.ai"
poll_wait_seconds = 25      # Remote input long-poll timeout
heartbeat_seconds = 30      # Periodic heartbeat interval
max_unflushed = 5000        # Outbox buffer limit
```
