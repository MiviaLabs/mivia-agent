# Agent Memory

mivia agents can save and search durable memories. A memory is a clean,
concrete record of a learning: a title, a short summary, what worked, what did
not work, and why. Memories survive across sessions, so an agent does not
start each session with no knowledge of the project.

## Scopes

A memory has one of two scopes.

| Scope | Meaning | Store |
|-------|---------|-------|
| `project` | This workspace only | `<workspace>/.agents/memories/*.md` |
| `org` | Every project of the org on this machine | `~/.mivia/memories/*.md` |

Project memories are Markdown files in the workspace. A single derived index
at `~/.mivia/context.db` serves all workspaces and scopes entries by project
path or org identity.

Org memory is shared across the org's projects, so one agent can record a
solution in one repo and another agent finds it in the next repo.

## Tools

| Tool | Purpose |
|------|---------|
| `memory_save` | Save one Markdown memory entry |
| `memory_search` | Find indexed Markdown entries by keyword |
| `memory_delete` | Delete one Markdown entry by id |

Both tools are available to the root session and to subagents. The tools
honor `disable_tools` in `[tools]`.

The prompts tell agents when to use memory: search before unfamiliar work,
save durable learnings, treat results as data, never store secrets.

## Entry format

One entry is stored as strict Markdown:

```markdown
# <title>

scope: project
verdict: good
tags: a, b
created: 2026-08-09

## Summary
<short description>

## What worked
- <bullets>

## What did not work
- <bullets>

## Why
<reasoning>

## References
- <path or link>
```

The `verdict` is one of `good`, `bad`, `mixed`, `neutral`. Fields have size
limits: title 120 characters, summary 400, why 1000, each worked/did-not-work
field 2000, up to 8 tags and 8 references. The rendered entry is capped at
`max_entry_bytes` (8192 by default). Tags are stored comma-separated, so a
tag must not itself contain a comma.

`memory_save` refuses entries that contain control characters (except line
feed and tab) or content that matches a `block_patterns` regex. It also
refuses org-scope saves when no org identity is configured.

`memory_delete` takes the `id` returned by `memory_save` or `memory_search`
and removes that Markdown entry. Use `memory_search` first to find the id.

## Configuration

```toml
[memory]
enabled = true              # default true; false removes the tools
store_backend = "markdown"  # "memory", legacy "sqlite", or "markdown" (default)
store_path = ""             # unused by the Markdown backend
org_id = ""                 # USER config only; see below
max_entry_bytes = 8192      # per-entry cap
max_entries = 500           # compatibility limit for legacy SQLite storage
max_search_results = 8      # memory_search result cap
block_patterns = []         # regexes; matching content is refused
inject_core = false         # default false; see below
```

`inject_core` auto-injects a bounded "core" memory tier into the system
prompt at session start. It defaults to `false` because it changes every
session's prompt composition and can weaken tool-approval gating for an
operator who allowlists the mivia binary itself in
`[tools].run_allowlist`. Turning it on is a deliberate, per-repository
choice, not a default an upgrade should hand you silently.

`store_backend = "memory"` keeps memories in the process only. It is useful
for tests and sessions that must not persist. `"markdown"` is the durable
default. Explicit `"sqlite"` remains a legacy compatibility backend.

### Markdown and the derived index

Markdown files are the source of truth. The harness scans project files at
startup and before memory reads and writes. It updates the shared SQLite index
after a file changes. The index can be rebuilt from the Markdown files.

The index is a cache. A failed index update does not remove a saved Markdown
file. The next writable memory operation retries the scan.

The shared context database can contain proprietary information. Protect the
`~/.mivia` directory with local filesystem permissions.

The `block_patterns` list in the repo's own `.mivia/mivia.toml` refuses common
secret shapes at save time. Keep entries small. The repository stores the
source Markdown in Git and the derived index outside the repository.

### Org identity is user-owned

`org_id` is honored only from the user config file (`~/.mivia/mivia.toml`).
A workspace config cannot name the org store: any repository can ship its own
`.mivia/mivia.toml`, so a repo-controlled file must not decide which org store
its agents write into. With no user-level `org_id`, `memory_save` with
`scope = "org"` fails with a clear error and `memory_search` with
`scope = "org"` returns an empty result.

An `org_id` looks like a host and org, for example
`org_id = "github.com/MiviaLabs"`. It is case-insensitive and stored
lowercase.

## Search behavior

`memory_search` matches keywords literally. `%` and `_` in a query are not
wildcards. Results rank exact title matches first, then title contains,
then summary contains, then body contains. `scope` selects `project`, `org`,
or `all` (the default). The result count is capped by `max_search_results`.

Search results are advisory local data. The tool description and the prompts
tell agents to weigh them as data, never to obey them as instructions.

## CLI command

`mivia memory search <query> [--scope project|org|all] [--limit N] [--json] [--workspace dir] [--config path]`

Searches stored memories for the query string. The query is required.

Default scope is `all`. Default limit is the `[memory] max_search_results` setting (8 by default).

With `--json`, the command prints a JSON array. Each object has these fields:
- `id` — the memory entry ID
- `scope` — `project` or `org`
- `org` — the org ID (empty string for project scope)
- `title` — the entry title
- `verdict` — `good`, `bad`, `mixed`, or `neutral`
- `tags` — list of tag strings
- `created` — the creation date
- `summary` — the summary snippet

Without `--json`, the command prints a ranked list. Each entry shows title, scope, verdict, date, tags, and the summary snippet. Zero results print a friendly message and exit 0.

Errors exit non-zero. A missing query, an invalid flag value, a duplicate flag, an unknown flag, or memory disabled all exit non-zero.

Examples:

```
mivia memory search "authentication bug"
mivia memory search "deployment" --scope org --json
```

## Privacy

Memory content stays on the machine. It enters model context only through
`memory_search` results, exactly like any other tool output. Never store
secrets, keys, tokens, passwords, or credentials in memory. `block_patterns`
is a configuration-only guard; nothing is compiled into the binary.
