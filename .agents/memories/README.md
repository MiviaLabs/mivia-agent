# Memory store (`.agents/memories/`)

Team-shared, cross-tool operational memory. One Markdown file per memory,
each file is git-committed and read at the start of every task. The
frontmatter schema is fixed; the schema is also enforced at skill load
(see `INV-AG-17` in `.mivia/invariants.md`).

## Frontmatter schema (mandatory)

```yaml
---
id: <stable snake_case id; must be unique>
title: <short human-readable title>
content: <one-sentence statement of the fact or rule>
importance: <high | medium | low>
tags: [<comma-separated keywords>]
---
```

The body that follows the frontmatter is the full explanation: when the
fact applies, why it matters, and what to do instead. A memory without
a body is a stub; it should be promoted to a `.agents/rules/*.md` rule
or deleted.

## Filename convention

`<topic>_<slug>.md`. The `id` field must match the filename without the
`.md`. Example: `id: no_per_agent_spend_ceilings` lives in
`no-per-agent-spend-ceilings.md`.

## When to write

Write a memory only after a real correction or a stated standing
preference. Speculative memories rot. The same fact belongs in
`.agents/rules/` (durable policy) if it becomes a hard rule; memories
are operational, not authoritative.

## When to delete or update

- Delete when the underlying fact is gone (e.g. the workflow it
  references no longer exists).
- Update when the fact is still true but the wording has drifted.
- Never rewrite a memory to invert a previous decision without
  recording why; the diff itself is the audit trail.

## The archive

A memory that is no longer true but is worth keeping goes to
`.agents/memories/.archive/<id>.md`, with `archived_on: <date>` added to
its frontmatter. Archived files are not read at the start of a task.

Write the archived copy first, read it back and confirm the content
matches, then delete the original. A delete that runs before its write
loses the memory and leaves only the git history to recover it. The
`housekeeping` skill follows this order and gates the move on operator
approval.

## Reading

`AGENTS.md` mandates: "Read every file under `.agents/memories/` at the
start of a task, the same way you read this file." Treat each memory
as an active constraint; if it conflicts with the request, raise it
before acting.