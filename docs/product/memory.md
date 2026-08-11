# Agent Memory

mivia agents can save and search durable memories. A memory is a clean,
concrete record of a learning: a title, a short summary, what worked, what did
not work, and why. Memories survive across sessions, so an agent does not
start each session with no knowledge of the project.

## Scopes

A memory has one of two scopes.

| Scope | Meaning | Store |
|-------|---------|-------|
| `project` | This workspace only | `<workspace>/.mivia/memory.db` by default |
| `org` | Every project of the org on this machine | `~/.mivia/memory/org.db` (user level) |

The default project path is inside the current workspace. This keeps default
project memory separate from other projects. A custom `store_path` can point
outside the workspace, and SQLite follows symlinks. Treat a custom path as
shared data and verify its target before use.

Org memory is shared across the org's projects, so one agent can record a
solution in one repo and another agent finds it in the next repo.

## Tools

| Tool | Purpose |
|------|---------|
| `memory_save` | Save one memory entry |
| `memory_search` | Find entries by keyword |

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

## Configuration

```toml
[memory]
enabled = true              # default true; false removes the tools
store_backend = "sqlite"    # "memory" (ephemeral) or "sqlite" (default)
store_path = ""             # project DB; default <workspace>/.mivia/memory.db
org_id = ""                 # USER config only; see below
max_entry_bytes = 8192      # per-entry cap
max_entries = 500           # row cap per store file
max_search_results = 8      # memory_search result cap
block_patterns = []         # regexes; matching content is refused
```

`store_backend = "memory"` keeps memories in the process only. It is useful
for tests and for sessions that must not persist. `"sqlite"` is the durable
default and mirrors `[subagents] store_backend`.

### Transporting memory with the repository

The default project path `<workspace>/.mivia/memory.db` is machine-local: it
is a SQLite file and is not part of the repository tree unless the owner
commits it. To transport project memory with the repository, set
`store_path` to a path inside the tree (for example
`store_path = ".mivia/memory.db"`) and commit the database. The repository
ships with this setting.

Before you commit the database, run a WAL checkpoint so recent writes are in
the main file:

```sql
PRAGMA wal_checkpoint(TRUNCATE);
```

The store attempts this checkpoint after every save and on close. A concurrent
SQLite reader can keep the write-ahead log active. Before you commit a memory
database, stop other mivia processes, run the checkpoint, and confirm that no
`-wal` or `-shm` sidecar contains newer data. Do not commit those sidecar files.

Project memory databases can contain proprietary information. The project
database path does not provide a cross-user privacy boundary. Protect the file
and its parent directory with local filesystem permissions.

Two automated controls protect the committed artifact: `scripts/secret_scan.py`
decodes the database and scans its text columns on every commit (staged,
tracked, and base-range modes), and the `block_patterns` list in the repo's own
`.mivia/mivia.toml` refuses common secret shapes at save time. Keep entries
small: the repository's staged-file size gate (500 KiB) bounds how much memory
history the committed database can carry.

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

## Privacy

Memory content stays on the machine. It enters model context only through
`memory_search` results, exactly like any other tool output. Never store
secrets, keys, tokens, passwords, or credentials in memory. `block_patterns`
is a configuration-only guard; nothing is compiled into the binary.
