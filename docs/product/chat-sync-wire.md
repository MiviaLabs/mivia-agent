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
the same redaction and the same truncation budgets. There is no separate
subagent control, because the amount of text that leaves the machine must
have one answer, not two.

**A subagent's settled answer is sent by default.** Read this before you
enable sync on sensitive work:

- `include_thinking = false` withholds a subagent's reasoning text, as it
  withholds the root loop's. Only the byte count is reported.
- A configured redaction policy **turns per-delta streaming off by itself**.
  Redaction is a regex over one string, so it cannot match a secret split
  across two fragments: a pattern that catches a key in a settled message
  catches nothing when the same bytes arrive in three deltas. With a policy
  active the turn therefore sends one settled message, redacted as a whole.
  The reader waits for the end of the turn and receives the same words.
- `stream_assistant` is **on by default** and selects HOW answer text is sent,
  not WHETHER it is sent. On, a turn sends deltas and then a settled message
  with empty text; off, it sends one settled message carrying the same text.
  A reader receives the same words either way, which is why this option is not
  held to the fail-closed rule the two above are. Setting it to `false` does
  NOT withhold an answer - it only makes the viewer wait for the end of the
  turn to see any of it.
- To send no chat content at all, set `enabled = false`.

Subagent prose was formerly withheld whatever the settings said. That made a
remote viewer weaker than the local TUI, which shows a subagent's thread in
full: the viewer could list a running subagent but never open it. The prose
now ships. An operator who has sync on therefore sends a subagent's answers
where earlier versions sent none, WITHOUT having changed any setting.

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

A budget counts the **JSON-escaped** size of the field, not its raw size. That
is the unit the receiving store measures in, and the two are far apart for
some content: a control byte occupies one raw byte and six escaped ones. A
budget measured raw let a 16 KiB tool output be stored as 96 KiB, over the
receiving column's 64 KiB bound - the ordinary case for a tool that reads a
binary file.

When a field exceeds its budget, the system truncates the text cleanly at the
last valid UTF-8 rune boundary that fits, and records the retained and total
byte counts - in the same escaped unit - in the envelope `trunc.fields` map.

Two code points never reach the wire regardless of budget: `U+0000`, which the
receiving store cannot hold at all, is removed. See "Repairs made by the
server" below for what happens when a payload still arrives oversize.

## Event Types and Ordering

The sync stream uses 20 structured `mivia.chat.v1.*` event types. The list
below is a copy for reading; the authoritative set is `KnownWireTypes` in
`internal/chatsync/wire.go`, mirrored in `api/contracts/chat-sessions.v1.json`:

- `mivia.chat.v1.turn.started`
- `mivia.chat.v1.turn.ended`
- `mivia.chat.v1.turn.failed`
- `mivia.chat.v1.assistant.delta`
- `mivia.chat.v1.assistant.message`
- `mivia.chat.v1.assistant.reset`
- `mivia.chat.v1.thinking.delta`
- `mivia.chat.v1.tool.started`
- `mivia.chat.v1.tool.ended`
- `mivia.chat.v1.hook.ran` - one lifecycle hook run on the operator's machine.
  `blocked` is true for the run that REFUSED a tool call. That case is why the
  event exists: a blocked call emits no `tool.ended`, so without this a reader
  watches a `tool.started` that never finishes and is never told a local policy
  stopped it. `program` is the script's name, never its path. `output` rides the
  same include-tool-io gate as tool output and reports `output_bytes` even when
  withheld, so silence is distinguishable from suppression.
- `mivia.chat.v1.subagent.started`
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
`envelope.agent.parent_task` names the run that dispatched it. Depth reports
how deep a run sits; `parent_task` reports under which run, which two runs at
the same depth do not share.

`parent_task` is absent whenever the root loop dispatched the run, which is
the common case. A subagent cannot dispatch a subagent - the mandatory tool
denylist removes `delegate`, `dispatch_tasks` and `spawn_agent` from every
spawned registry - so the one relationship that produces a parent today is an
`ask_agent` referral: a task asks a peer role a question, and the referral
started to answer it reports the asking task as its parent.

A consumer must treat an absent `parent_task` as "top level", never as
"unknown", and must not require the field to draw a run. The parent can also
be genuinely unreachable: an ask whose owner was released at a retry boundary
reports no parent rather than a stale one.

A run reports its own start and end: `subagent.started` carries `task`, a
preview of what the run was asked to do that the producer bounds to 200
bytes, and its timestamp is the run's start time;
`subagent.ended` carries the terminal status. `subagent.progress` carries a
heartbeat, and its `detail` text holds the elapsed time, step count and tool
count. That type once declared `elapsed_seconds`, `steps` and `tool_calls`
fields as well; no version ever populated them, so they were removed.

The envelope's `block` groups the fragments a consumer must stitch together.
A tool event's block is its `tool_call_id`. Prose blocks name a stream and a
step within it:

```
<turn>:assistant:<step>              root narration
<turn>:thinking:<step>               root reasoning
<turn>:<task>:assistant:<step>       one subagent run's narration
<turn>:<task>:thinking:<step>        one subagent run's reasoning
```

This grammar is recorded in `api/contracts/chat-sessions.v1.json` under
`blockGrammar` - the prose regex, anchored on the stream suffix so a
`tool_call_id` can never parse as prose, the four stream forms, and the step
rules below - and the producer's own tests hold the record to the code. A
consumer parses ids with the recorded regex, never a copy of its own.

A turn is a loop - the model talks, calls a tool, reads the result, talks
again - and `<step>` is what separates one utterance from the next. It counts
from 0 and advances when a tool call closes the prose that preceded it, so
**a consumer must render one message per block, in arrival order, rather than
concatenating a turn's prose into one.** Doing the latter loses the order the
narration interleaved with the tool calls. A step that calls several tools at
once advances once, and a tool call that follows no prose advances not at all.

`<step>` is an identifier, NOT a dense counter, and a consumer must not treat it
as one. Ids come from one counter per producer session, shared by every turn
and every subagent run, so a stream's ids skip freely: the assistant and
thinking streams of a turn share a step, a second turn opens wherever the
counter stands, and two runs streaming at once interleave their ids. What the
wire guarantees is narrower and sufficient - a step id is never REUSED, by any
stream of the session, and a consumer is never handed a block with nothing in
it: a delta that failed to persist spends no step, and a reset that failed to
persist leaves the step it would have advanced.

The part before the final `:<step>` is the STREAM id, and it is a stable name
for all of a turn's prose of one kind from one agent.

`assistant.reset` says the turn producing a block is being re-driven from the
beginning: a prompt-too-long compaction, a bounded empty-response retry, or a
subagent's schema-repair retry. **A viewer that accumulates deltas must discard
what it holds for that block.** Its `block` is the STREAM id, with no `<step>`
suffix: by the time a reset fires the discarded attempt may span several steps,
and one step id cannot name them all - so the scope is every step of that
stream. The replay that follows opens a step id the abandoned attempt never
used, so a consumer keyed on the id alone cannot append the replay to the text
it was just told to drop.

Every session maintains a strictly monotonic sequence (`1..N`). If the system
drops events because of local queue saturation, a `sync.dropped` event consumes
a sequence number immediately before subsequent events to preserve causality.

## Outbox Durability and Conflict Handling

The system writes and persists events locally to `chat-sync/sessions/<id>/events.jsonl`
before network transmission. The local cursor `cursor.json` updates atomically
only after the remote server acknowledges receipt (HTTP 200/201).

Beside them, `status.json` records push health so a stall can be diagnosed
after the process is gone. It is written on transition only - `healthy` on
attach, `degraded` after three consecutive failed pushes or a failure sixty
seconds after the last success, `recovered` when a push lands again, `stopped`
on any stop (an orderly close, or the terminal reason of an auth stop, a
poison 400 or a recovery bound, whichever came first) - through the same temp-file-and-rename discipline
as the cursor, and a write failure is ignored: the file is a diagnostic, never
a reason to break sync. Fields: `state`, `reason`, `unflushed`,
`last_success_at`, `consecutive_failures`, `recoveries`, `create_failures`,
`create_throttled_until`, `at`. The last three belong to session recovery and
are zero or null until it engages; a non-null `create_throttled_until` is what
separates a throttled-create stall from a plain push stall; a successful
create clears both in memory, and the file shows that at its next
transition. The same transitions reach the host once each through `OnDegraded` / `OnRecovered`,
which the CLI prints as a notice and the TUI shows in its notice rail.

Every failed push is classified once, in `classifyFlushError`, into three
outcomes. **Retry** keeps the batch at the outbox head with jittered backoff:
network errors, 5xx, 408, 429, and the sequence-gap 400 that rebases.
**Stop** latches sync terminally: a fatal auth failure, and any 400, 413 or
422 that does not name a sequence problem - the server refused the *body*, and
a new session would refuse it identically. **Recover** abandons the remote
*session* and re-attaches the backlog onto a fresh one: a 409 (ended, as a
web viewer does), a 404 (deleted from the web), a transcript the server holds
that this outbox can never line up with. A deleted or ended session never
stops a live CLI; the server may hold it under a new id.

Recovery creates the new session, rebases the outbox onto it renumbered from
1, records a `mivia.chat.v1.sync.forked` whose `new_session_id` and
`forked_from` name both sessions, retargets the heartbeat and poller, rewrites
the identity file, and pushes at once. It is bounded two ways, and only one
bound latches: a second recovery inside 60 seconds is *deferred* to the retry
schedule, and two consecutive recoveries with no successful push in between
stop sync with a reason naming that. A failed `CreateSession` never latches
(auth aside), never counts as a recovery and leaves the outbox untouched;
after three consecutive failures further attempts are throttled to one per
five minutes, the session stays degraded meanwhile, and `status.json` carries
`create_failures` and `create_throttled_until` so the throttle is visible.

The same marker is recorded when a fork happens at ATTACH time: events the
server holds past our cursor are read back and their `writer_id` compared
against ours, and a foreign writer means another machine owns the session, so
the old one is ended and a new one created. The API has no writer concept of
its own; it accepts a second writer's append happily, which is why this check
is done on read-back by the client.

A 400 is classified: a sequence-gap 400 re-reads the session and rebases on
`serverLastSeq+1`, because treating a recoverable gap as fatal guarantees the
failure it is trying to avoid. Any other 400 - and 413 or 422 - is poison a
retry cannot fix, so sync stops and says why; a gap the rebase cannot close
recovers onto a new session instead. 408 and 429 stay retryable with
jittered backoff.

### Repairs made by the server

The API appends a batch as one multi-row statement, so a rejection of any
single event rejects the whole batch - up to 100 events. Combined with the
poison rule above, one unacceptable event used to cost the rest of the
session: the 400 stopped sync permanently.

The API therefore repairs rather than rejects. It removes a `U+0000` it cannot
store, and shrinks a payload over its column bound by cutting the longest
string, recording the cut in that payload's own `trunc.fields`. A repaired
payload carries `"repaired_at_ingest": true`.

That marker matters to this client. On a short `insertedCount` - the ordinary
shape of a retry, because the API skips rows it already holds - the client
reads the range back and checks that the stored body is its own. A repaired
body is not equal to what was sent, so without the marker it reads as a
foreign writer and stops the session.

The tolerance is deliberately narrow: a repair may only SHRINK a string, so a
stored string must be what was sent with its NULs removed, or a prefix of
that. Any other difference is still treated as corruption and still stops
sync.

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
stream_assistant = false    # Opt OUT of per-delta streaming. Omit the key and
                            # streaming runs; off = one settled message per turn
api_url = "https://api.mivia.app"
poll_wait_seconds = 25      # Remote input long-poll timeout
heartbeat_seconds = 30      # Periodic heartbeat interval
max_unflushed = 5000        # Outbox buffer limit
```

`api_url` is resolved in one place, `chatsync.ResolveEndpoint`: `[sync]
api_url` when set, else `MIVIA_API_BASE_URL` from the process environment or
an env file, else `https://api.mivia.app`. The "chat sync is running" notice
names the resolved URL and its source, so an upload to the wrong backend is
visible the moment it starts. `mivia doctor` prints the same resolution as
`sync_api`, whether a login is present as `sync_login`, and the result of one
bounded, unauthenticated request to the API's `/health` route as `sync_probe`
(skipped when not logged in, because sync never activates then); `--json`
carries them as `sync_api_url`, `sync_api_source`, `sync_login` and
`sync_probe`; with `enabled = false` the human line reads `disabled`, the
JSON source is `disabled` and the other two fields say `skipped (sync
disabled)`. Doctor never refuses to run over the probe: sync failing is
never a reason to break the local chat.
