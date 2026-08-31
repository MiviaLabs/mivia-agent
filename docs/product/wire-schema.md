# Chat wire schema

This document gives the wire contract for `mivia chat --json` and for the JSON output of the session commands. A frontend that drives mivia as a child process (for example a desktop app) uses this contract to send user input and to read structured output.

All output is newline-delimited JSON (NDJSON): one JSON object per line, on stdout. Every line has a `type` field. Read the `type` first, then the fields that belong to that type. Unknown types are safe to ignore: mivia adds new types, and old consumers must keep working.

## Start the sidecar

```text
mivia chat --json [--session <name>]
```

Requirements:

- `--json` works only with piped stdin. It does not work with an interactive terminal, and not with `-p/--prompt`.
- Send one line of text per user turn. The line goes through the slash-command parser first: a line that starts with `/` is a command, not a turn.
- Send `exit` or `quit` to end the process. Close stdin to end it also.

The `done` event carries `session_id`. A caller that did not set `--session` reads this id, then uses it later with `mivia sessions show/--session` or `mivia compact --session`.

## Turn content events

| Type | Fields | Meaning |
|------|--------|---------|
| `chunk` | `text` | One piece of the assistant answer. The parts, in order, make the full answer. A multi-byte character is never split across two chunks. |
| `thinking` | `text` | Model reasoning, for providers that expose it. |
| `tool_start` | `tool_call_id`, `name`, `input`, `origin_task_id`, `origin_agent`, `origin_depth`, `origin_task_description` | A tool call started. `input` is a bounded, redacted preview. The `origin_*` fields appear only when a subagent made the call, not the root loop. |
| `tool_end` | `tool_call_id`, `name`, `output`, `status`, `origin_*` | A tool call ended. `status` is `ok` or `failed`. An absent `status` means an older mivia build; read it as `ok`. |
| `subagent_done` | `origin_task_id` | One subagent finished all its work. |
| `subagent_heartbeat` | `origin_task_id`, `message` | A subagent is alive but produced no new event. |

A `tool_start` and its `tool_end` share the `tool_call_id`. Group the pair on this id.

## Accounting events

| Type | Fields | Meaning |
|------|--------|---------|
| `cache_usage` | `provider`, `model`, `cache_usage{input_tokens, cached_input_tokens, cache_write_tokens, hit_percent}` | Provider-reported prompt cache use for one completion. |
| `token_usage` | `provider`, `model`, `token_usage{input_tokens, output_tokens, estimated_tokens, calibration_ratio}` | Real provider-reported token counts. `input_tokens` is the true prompt size the provider charged. |
| `context_usage` | `context_usage{used_tokens, budget_tokens, context_window_tokens, output_reserve_tokens, percent}` | Session-level estimate of the prompt cost. Emitted at the end of each successful turn, before `done`, and after a `/compact`. |
| `compaction` | `message`, `compaction{trigger, before_tokens, after_tokens, elided_messages, elided_bytes, summary_version}` | The context was compacted. The payload has no conversation content. Emitted only after the compaction was made durable. |

A consumer that shows a context meter should prefer `token_usage.input_tokens` when present, and use `context_usage` as the fallback that also states the window and the budget.

## Turn lifecycle events

| Type | Fields | Meaning |
|------|--------|---------|
| `done` | `session_id` | The turn finished with success. |
| `cancelled` | none | The user stopped the turn. No `done` follows. |
| `error` | `message` | The turn failed. The message is redaction-safe and generic; the full error text goes to stderr. No `done` follows. |

`cancelled` and `error` are separate types, so a consumer does not need to read the message text to tell them apart.

## Slash command events

Slash feedback comes as these types:

| Type | Fields | Meaning |
|------|--------|---------|
| `slash_info` | `message` | Information output from any slash command. |
| `slash_error` | `message` | A slash command failed. This is the only failure signal for slash commands. |
| `model_changed` | `provider`, `model`, `discarded_effort` | `/model` switched the model. `discarded_effort` appears only when the switch dropped a reasoning effort. |
| `effort_changed` | `model`, `effort` | `/effort` changed the reasoning effort. |

### Trigger compaction: `/compact`

Send the line `/compact`. The response on stdout is:

1. One `compaction` event with the before/after numbers.
2. One `context_usage` event with the refreshed meter.

If the compaction cannot run, you get one `slash_error` event instead. `/compact` while a turn is active is refused; send it between turns.

## Events from other processes (`external_*`)

All mivia processes that share one store directory see each other's activity through a local hub (see [Configuration: live cross-process relay](config.md#live-cross-process-relay)). A `--json` sidecar renders activity from other processes on the same session as `external_*` events:

| Type | Meaning |
|------|---------|
| `external_turn_start` | A turn started in another process. `text` holds the user input, `run_id` identifies the turn. |
| `external_chunk` | Answer text from the other process. |
| `external_thinking` | Reasoning text from the other process. |
| `external_tool_start` / `external_tool_end` | A tool call made by the other process, with the same fields as the local types. |
| `external_done` | The other process's turn finished. |
| `external_error` | The other process's turn failed. |
| `external_compaction` | Another process compacted this session's context. Same payload as `compaction`, plus `run_id`. |
| `external_dropped` | Relayed events were lost before they reached you. `dropped` is how many since the previous report; `total_dropped` is the running total for the current hub connection. |

The `run_id` field links the `external_*` events of one turn. Events from other sessions in the same store are not relayed to your stream: each sidecar sees only its own session.

The relay is lossy on purpose. Every queue between the two processes is bounded and drops its oldest entry when a slow reader falls behind, so a busy turn can lose events rather than stall the process that is producing them. Two consequences for a consumer:

- Events of one turn always arrive in the order the other process published them. A later event never overtakes an earlier one, so `external_done` is the last event you receive for its `run_id`.
- Events can be missing, and you are told when. Each loss produces one `external_dropped` event; a stream with no loss never emits one. The total counts loss on your own connection to the hub only, and it restarts at zero if the hub owner changes (the process you were connected to exited and another took over), so treat a total lower than the previous one as a new connection rather than as an error. Treat `external_chunk` text as a live preview rather than the authoritative transcript, and read the stored session (`mivia sessions show`) for the complete answer - especially after an `external_dropped`.

A turn whose start was lost is not reported at all: you never receive `external_done` for a `run_id` you have not already seen, so a `run_id` that appears for the first time is always a real turn beginning.

## Session commands

These commands print JSON to stdout with `--json`:

```text
mivia sessions list [--workspace dir] --json
mivia sessions show <name> [--limit N] --json
mivia sessions usage <name> --json
mivia sessions rename <name> <title> --json
mivia sessions delete <name> --json
mivia compact --session <name> --json
```

- `sessions list` prints an array of session records: `name`, `title`, message counts, and timestamps.
- `sessions show` prints an array of message records with the full shape: `role`, `content`, `tool_calls`, `reasoning_content`. Messages are redacted and bounded to 8192 bytes each. The default limit is the last 50 messages.
- `sessions usage` prints `{"used_tokens", "budget_tokens", "context_window_tokens", "output_reserve_tokens", "percent"}` - the same numbers as the `context_usage` event. Use it to seed a context meter when a saved thread is reopened.
- `sessions rename` prints `{"renamed": {"session": <name>, "title": <title>}}` on success.
- `sessions delete` prints `{"deleted": <name>}` on success.
- `compact` compacts a stored session that no live process holds open, and prints `{"session", "before_tokens", "after_tokens", "elided_messages", "elided_bytes"}`. To compact the session a sidecar holds open, send `/compact` to that sidecar instead.

Without `--json`, these commands print human-readable text (rename and delete print nothing on success). Errors go to stderr and set a non-zero exit code, with or without `--json`.

## See also

- [Configuration](config.md) - store paths and the cross-process relay
- [Coding agent mode](agent.md) - the chat surfaces and their modes
