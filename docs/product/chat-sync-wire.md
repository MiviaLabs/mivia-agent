# Remote Chat Session Synchronization

This document describes the remote chat session synchronization wire protocol,
privacy gates, truncation budgets, outbox durability, and configuration options.

## Overview

Remote chat synchronization lets a user view and interact with local terminal
chat sessions through a web viewer. The CLI synchronizes events in real time to
the backend API using an append-only event stream.

## Fail-Closed Privacy Model

Remote synchronization uses a strict fail-closed privacy posture:

1. **Disabled by default**: Synchronization is inactive unless explicitly enabled
   via configuration (`sync.enabled = true`) or the `--sync` CLI flag.
2. **Tool I/O withheld**: By default, tool inputs and outputs are omitted from
   the wire payload and recorded in the envelope's `redacted` array. When enabled
   via `sync.include_tool_io = true`, payloads still pass through the workspace
   redaction policy.
3. **Thinking withheld**: Model thinking/reasoning blocks are withheld by
   default (`sync.include_thinking = false`).

## Truncation Budgets

To keep event payloads bounded and prevent network buffer exhaustion, string
fields are truncated using rune-safe byte budgets:

| Field | Budget | Behavior |
|-------|--------|----------|
| User Prompt | 32 KiB | UTF-8 rune-safe truncation |
| Assistant Message | 32 KiB | UTF-8 rune-safe truncation |
| Tool Output | 16 KiB | UTF-8 rune-safe truncation |
| Streaming Delta | 8 KiB | UTF-8 rune-safe truncation |
| Tool Input | 4 KiB | UTF-8 rune-safe truncation |
| Error Message | 2 KiB | UTF-8 rune-safe truncation |
| Metadata Labels | 200 B | UTF-8 rune-safe truncation |

When a field exceeds its budget, it is truncated cleanly at the last valid UTF-8
rune boundary and recorded in the envelope's `trunc` array.

## Event Types and Ordering

The sync stream uses 15 structured `mivia.chat.v1.*` event types:

- `mivia.chat.v1.session.created`
- `mivia.chat.v1.turn.started`
- `mivia.chat.v1.turn.ended`
- `mivia.chat.v1.assistant.delta`
- `mivia.chat.v1.assistant.message`
- `mivia.chat.v1.tool.call`
- `mivia.chat.v1.tool.result`
- `mivia.chat.v1.error`
- `mivia.chat.v1.subagent.started`
- `mivia.chat.v1.subagent.heartbeat`
- `mivia.chat.v1.subagent.ended`
- `mivia.chat.v1.compaction`
- `mivia.chat.v1.context.summary`
- `mivia.chat.v1.sync.dropped`
- `mivia.chat.v1.sync.forked`

Every session maintains a strictly monotonic sequence (`1..N`). If events are
dropped due to local queue saturation, a `sync.dropped` event consumes a sequence
number immediately before subsequent events to preserve causality.

## Outbox Durability and Conflict Handling

Events are written and persisted locally to `chat-sync/sessions/<id>/events.jsonl`
before transmission. The local cursor `cursor.json` is updated atomically only
after the remote server acknowledges receipt (HTTP 200/201).

If a remote writer conflict occurs (HTTP 409 Conflict), the session forks cleanly
to a new session ID and records a `mivia.chat.v1.sync.forked` event.

## Configuration

Configure remote chat synchronization in `.mivia/mivia.toml`:

```toml
[sync]
enabled = false             # Fail-closed default
include_tool_io = false     # Withhold tool input/output
include_thinking = false    # Withhold thinking blocks
api_url = "https://api.mivia.ai"
poll_wait_seconds = 25      # Remote input long-poll timeout
heartbeat_seconds = 30      # Periodic heartbeat interval
max_unflushed = 5000        # Outbox buffer limit
```
