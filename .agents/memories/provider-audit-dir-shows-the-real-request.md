---
id: provider_audit_dir_shows_the_real_request
title: MIVIA_PROVIDER_AUDIT_DIR is the ground truth for "the model behaved oddly"
content: Set MIVIA_PROVIDER_AUDIT_DIR to dump the EFFECTIVE provider request and response of every agent-loop iteration; a checkpoint transcript is not the wire and cannot answer what the model was actually sent.
importance: high
x-scope: project
x-verdict: good
tags: [debugging, provider, agent-loop, evidence, audit-dump]
related: [sdk_surface_advertised_replaces_wholesale, non_stream_header_wait_is_generation_time]
updated: 2026-09-11
---

# The wire dump answers what the stored transcript cannot

`MIVIA_PROVIDER_AUDIT_DIR=<dir> ./mivia <anything>` writes one JSONL file
per session (`internal/agent/audit_dump.go`), one line per agent-loop
iteration, each carrying the **effective** request - model, stream,
temperature, max_tokens, reasoning level and dialect, timeout,
`tool_names`, `message_count` - plus the response's `finish_reason`,
content, reasoning content and tool calls, and the full message array.

It hangs off the completer's own per-call seam, AFTER `mergeTurnDefaults`,
which is the whole point: the SDK's own Audit hook sees only Model,
Messages and Tools, so the fields that decide how a model behaves are
empty there.

## Why reach for it first

On 2026-09-11 an automation running the `bug-audit` skill "succeeded" in
12 seconds after one tool roundtrip. Four plausible hypotheses were on the
table (a stream cut at `finish_reason=length`, a reasoning budget eating
the content stream, a shrunken system prompt, a weak model). The dump
settled it in one run and refuted all four: request 1 carried 24 tools,
request 2 carried **zero**, and the model simply had nothing left to call.
It also showed, in the same file, that headless sessions were sending no
`system` message at all - a second defect nobody had suspected.

Reading `chat_sessions` or `context_checkpoints` could not have shown
either: a stored transcript records the conversation, never the request
fields or the tool array.

## How to use it

- One run, then read `tool_names`, `message_count` and `finish_reason` per
  iteration before forming any hypothesis about the model.
- Compare a working surface against a broken one by diffing those three
  across two dumps; that is what localized this defect.
- It is 0600, operator-named, redacted through the process policy, and
  costs nothing when the variable is unset. It still holds prompts and
  model output - keep it out of the workspace.
- `tool_names` records names only. To check whether the wire array carries
  tool DESCRIPTIONS, assert on the pinned specs in a test instead.
