# Memory store (`.agents/memories/`)

Team-shared, cross-tool operational memory. One Markdown file per memory,
each file is git-committed and read at the start of every task. The
frontmatter schema below is mandatory. `scripts/check_memories.py` enforces
it; nothing in the compiled binary reads this directory, so that gate is the
only control.

## Frontmatter schema (mandatory)

```yaml
---
id: <stable snake_case id; must be unique>
title: <short human-readable title>
content: <one-sentence statement of the fact or rule>
importance: <high | medium | low>
tags: [<comma-separated keywords>]
updated: <ISO date the memory last changed, YYYY-MM-DD>
---
```

The body that follows the frontmatter is the full explanation: when the
fact applies, why it matters, and what to do instead. A memory without
a body is a stub; it should be promoted to a `.agents/rules/*.md` rule
or deleted.

`updated` is the date the memory's content last changed (title, content,
or body). Stamp it on creation and bump it on every later edit: the
housekeeping staleness rule measures it, and `scripts/check_memories.py`
enforces the format. The gate judges the value alone - shape and calendar
validity - and never reads the clock; recency, including a stamp ahead of
the session date, is the audit's judgment. Write the date as a plain
scalar; the gate also accepts one matched quote pair around it. `id`,
`importance`, and `tags` must be plain scalars in the shapes shown; only
`title` accepts quoting freely.

## Filename convention

The filename is a kebab-case slug: `<slug>.md`, using only `[a-z0-9-]`.

Derive `id` from the filename: remove the `.md`, then replace every hyphen
with an underscore. `no-per-agent-spend-ceilings.md` therefore carries
`id: no_per_agent_spend_ceilings`. The two spellings differ on purpose: a
path is kebab-case and an identifier is snake_case.

## When to write

Write a memory only after a real correction or a stated standing
preference. Speculative memories rot. Stamp `updated` with the date of
the write. The same fact belongs in `.agents/rules/` (durable policy) if
it becomes a hard rule; memories are operational, not authoritative.

## When to delete or update

- Delete when the underlying fact is gone (e.g. the workflow it
  references no longer exists).
- Update when the fact is still true but the wording has drifted.
- Bump `updated` whenever an edit lands, so the staleness rule measures
  the age of the fact and not of the file.
- Never rewrite a memory to invert a previous decision without
  recording why; the diff itself is the audit trail.

## The archive

A memory that is no longer true but is worth keeping goes to
`.agents/memories/.archive/<filename>`, keeping the original kebab-case
filename, with `archived_on: <date>` added to its frontmatter. The `id`
field stays the identifier; the path stays kebab-case. Archived files are not read at the start of a task.

Write the archived copy first, read it back and confirm the content
matches, then delete the original. A delete that runs before its write
loses the memory and leaves only the git history to recover it. The
`memories-housekeeping` skill follows this order and gates the move on operator
approval.

## Reading

`AGENTS.md` mandates: "Read every file under `.agents/memories/` at the
start of a task, the same way you read this file." That mandate excludes
`.agents/memories/.archive/`, which holds records rather than active
constraints. Treat each memory
as an active constraint; if it conflicts with the request, raise it
before acting.
