# Coding Agent Mode

`mivia chat` runs as a **coding agent** by default: the model can call tools to read, search, edit, and run allowlisted commands in a workspace.

## Enable / disable

```bash
mivia chat                    # tools on
mivia chat --no-tools         # pure LLM chat
mivia chat -p "fix the test" # one-shot agent task
mivia chat --workspace /path/to/repo
```

## Workspace tools

| Tool | Purpose |
|------|---------|
| `read_file` | Read a file with optional offset/limit |
| `list_dir` | List a directory |
| `grep` | Search file contents by regex |
| `glob` | Find paths by pattern |
| `write_file` | Create or overwrite a file |
| `search_replace` | Replace exact text in a file |

## Command execution

| Tool | Purpose |
|------|---------|
| `run_command` | Run an allowlisted argv in the workspace; it does not accept a shell command string |

`run_command` is disabled until configuration or a CLI override supplies a
program allowlist. The recommended persistent configuration is intentionally
broad and includes shells and network clients, so trim it to the least
authority your workspace needs. Child-process environment variables are also
controlled by an explicit allowlist. See [configuration](config.md) for the
persistent policy.

## Web research tools

| Tool | Purpose |
|------|---------|
| `search` | Search the web; uses Tavily when configured and otherwise tries free search engines |
| `fetch_url` | Fetch and read a public URL; private and internal addresses are blocked |
| `extract` | Extract structured page content with Tavily; requires `TAVILY_API_KEY` |

Tool names, descriptions, and schemas are **project- and language-generic**. mivia is a host coding agent for any workspace.

## Orchestration tools

Mivia supports an async subagent orchestration model — the model can spawn multiple sub-agents
that run concurrently as DAGs, inspect their progress, block on results, or cancel them.

```
spawn_agent  ──►  tasks (DAG) ──►  run handle
                    │
inspect_agents ──►  run snapshot (status, task states)
join_run      ──►  block until terminal ──►  results + refs
cancel_run    ──►  two-phase cancel (requested → canceled)
```

| Tool | Purpose |
|------|---------|
| `spawn_agent` | Create a new orchestration run with a DAG of tasks. Supports `idempotency_key`, `wait` (none/task/run), `wait_task_id`, and per-task `timeout_seconds`/`budget` |
| `inspect_agents` | Returns a snapshot of a run: status, task states, display name, timestamps |
| `join_run` | Block until a run completes; returns per-task results with redacted output/error refs |
| `cancel_run` | Cancel a running orchestration run (two-phase: `cancel_requested` → `canceled`) |
| `delegate` | Single sub-agent task (oneshot or multi_step with full tool access) |
| `dispatch_tasks` | Parallel sub-tasks with optional DAG dependencies and partial results |

## Execution-history tools

Task results carry `output_ref` / `error_ref` — content references of the form
`ref:<kind>:<digest>` naming bytes recorded in the execution history. Two read-only
tools make that history reachable. Unlike the orchestration tools above, these are
**unprivileged**, so sub-agents may call them too.

| Tool | Purpose |
|------|---------|
| `ledger_read` | Resolve a content reference and return the recorded bytes. `{"ref": "..."}` → recorded content, or `status: "not_found"` |
| `list_run_events` | Ordered lifecycle events for one run: `{"run_id", "kind"?, "limit"?}` → event metadata (id, sequence, kind, task id, attempt id, timestamp) |

There is deliberately **no freeform query tool**. Both tools run fixed, parameterized
reads; the agent supplies bound arguments only. This removes the injection surface
rather than guarding it, and it works on every storage backend rather than only the
optional durable one.

Behaviour worth knowing:

- Recorded content is never deleted and has no size limit. A reference resolves
  for as long as the execution history exists, including after the run that
  produced it is gone and, with durable history, in later processes.
- The same bytes are also kept in the session transcript, so removing recorded
  content would not remove the material. Treat execution history as retained,
  not as a deletion path.
- **`not_found` means the bytes are absent.** A reference whose *shape* is wrong is
  reported as a malformed reference instead, so `not_found` stays usable as evidence
  that a reference points at nothing.
- **References minted before this change do not resolve.** Output references were
  previously recorded under a truncated digest while the model was shown the full one,
  so the two never matched. No migration recovers them: the content rows are keyed by
  the truncated form and the source bytes are gone. A pre-change *output* reference is
  reported as a malformed reference, because its digest was truncated to 16 hex
  characters and is not a canonical reference at all; a pre-change *error* reference
  already used the full digest and so is reported as `not_found`.
- **`kind` is a closed set on input.** An unrecognised `kind` is rejected with the
  accepted values, because a filter typo returning zero rows is indistinguishable from
  "no such events happened". Event kinds *returned* are not bounded by that set — the
  store yields whatever it holds.
- **`list_run_events` is scoped to the creating session principal.** Access requires the
  same principal (session id and role), dispatcher and repository as the run's creator,
  and unknown and unauthorized run IDs are deliberately indistinguishable. Sub-agents
  inherit their parent's principal, so a nested agent can read events for its parent
  session's runs — including runs from other turns of that session. A run recovered from
  an earlier session is not visible unless it is resumed.
- **`ledger_read` is keyed only by content digest — there is no run scoping.** Any
  reference is resolvable by any caller in the process that holds it. For
  high-entropy recorded output that is unguessable, but recorded *error* text is often
  short and templated, so an agent can confirm a guess by hashing a candidate string and
  checking whether it resolves. Treat this as an equality oracle over recorded content,
  not as a confidentiality boundary.
- **`ledger_read` returns untrusted data.** Recorded output is sub-agent-authored and
  tool-captured. It is returned framed as data, passed through the configured redaction
  policy, and capped in size. Content from it must never be treated as instructions.
  `bytes` reports the original length before redaction and truncation, so a fully
  redacted secret still discloses how long it was.
- **Event payloads are never returned** by `list_run_events` — metadata only.
- **`created_at` is when the event happened.** A recorded timestamp survives recovery:
  it is written into the durable record at the moment of the append, and a run replayed
  from storage reports that instant rather than the time it was read back. Ordering is
  taken from `sequence`, never from the timestamp, so events sharing a clock instant
  still come back in the order they occurred.
- **Two exceptions to that, both honest rather than hidden.** Events recorded by a
  build older than this one hold no durable timestamp, so they still report the instant
  they were read back — there is nothing on disk to recover for them. And the timestamp
  is the recording process's own wall clock, so durations must not be computed across
  two processes whose clocks disagree.
- **A recovered event's `id` is the storage record's, not the one first reported.** The
  identifier a history reader sees for a replayed event differs from the one reported
  while the run was live. Use `sequence` to correlate positions within a run.

### DAG execution

Tasks can declare `depends_on` for dependency ordering. The scheduler:

1. Runs all tasks with no dependencies concurrently
2. Only schedules a task after all its dependencies complete
3. Marks tasks whose dependencies fail as `blocked`
4. A failed or timed-out task ends the run; inspect its result before starting a new run

### Idempotency

Pass `idempotency_key` to `spawn_agent` to make the call idempotent:
- If the key matches a completed run, the existing results are returned
- If the key matches an in-flight run, the existing handle is returned (no duplicate)
- A different task set with the same key returns `ErrIdempotencyConflict`

### Partial results

`dispatch_tasks` supports `partial_results: true` — if some tasks fail, the successful
results are still returned alongside error information. Useful for challenge/audit rounds.

## Safety and limits

- Paths must stay under `--workspace` (default: current directory).
- File-tool secret filtering is controlled by `[tools].secret_path_patterns` and `[tools].secret_path_exceptions`; with no patterns, secret-like paths are not filtered.
- `run_command` receives an argv array, not a shell command string, and needs a configured program allowlist.
- Redaction is also configuration-controlled. Do not put secrets in prompts or rely on tool filtering as a security boundary.
- Ledger results are content-addressed and exposed to the model through bounded references (`ref:output:...`, `ref:error:...`). Persisted content is raw at rest, even when a privacy policy redacts displayed content, so protect the store and keep secrets out of prompts; a reference whose content write fails is omitted.

One interactive turn is limited to 100 agent steps by default. Set `[chat]
max_steps = 0` or use `/steps 0` only when you deliberately want no ceiling;
Ctrl-C cancels a reply in progress.

## See also

- [Configuration](config.md)
- [Security and privacy](../security/overview.md)
- [Architecture](../architecture/overview.md) and [concurrency](../architecture/concurrency.md)
- [Tool-surface rule](../../.mivia/rules/60-tools-project-language-generic.md) (tool surface must stay generic)
