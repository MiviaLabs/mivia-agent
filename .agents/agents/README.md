# Subagent role definitions (`.agents/agents/`)

This directory holds Markdown subagent role definitions for the human and
ADLC-driven development workflow. The four standard roles mirror the
delivery loop in `AGENTS.md` and `.agents/rules/05-adlc-agentic-development-lifecycle.md`:

| Role | ADLC step | Reads | Writes | Output verdict |
|------|-----------|-------|--------|----------------|
| `planner.md` | 0-1 (Plan + Breakdown) | yes | no | plan block in context |
| `plan-reviewer.md` | 0 (challenge) | yes | no | `Block` / `PASS` / `REJECT` |
| `builder.md` | 5 (implement) | yes | yes | chunk log + `## Done` |
| `reviewer.md` | 6 (review) | yes | only scratch fixtures under `/tmp` | `Block` / `PASS` / `REJECT` |

## File schema

Every file in this directory has YAML frontmatter with at least:

```yaml
---
name: <role-name>          # must equal the filename without .md
description: <one-line trigger description>
tools:
  - <tool>
  - <tool>
---
```

`scripts/check_agents.py` enforces: `name` matches the filename, the
required keys are present, and the role-specific disallowed-operations
list appears in the body. Run `make agents-check` to validate.

## Loading

These files are **not** loaded by the `mivia` binary. The binary reads
`.mivia/agents/*.toml` for its workflow-engine agent roles. This
directory is for the human/ADLC dispatch surface; the workflow-engine
roles will be migrated here as Markdown in a follow-up refactor.

When a future orchestration tool wants to dispatch one of these roles,
it reads the frontmatter to discover tools and the body for the role
contract.

## Adding a role

1. Pick a name that matches the ADLC vocabulary (`Block / PASS / REJECT /
   findings`); avoid introducing a second vocabulary.
2. Match the frontmatter schema above.
3. Include a "Disallowed operations" section listing the tools the role
   must NOT use, with the reason.
4. Run `make agents-check` before committing.

## Legacy roles

The legacy role definitions still live under `.mivia/agents/*.toml`. They
will be migrated here as Markdown in a follow-up. Until then both sets
coexist; do not edit a `.toml` to change role semantics - the Markdown
set is the source of truth for human/ADLC work, and the TOML set is the
binary's workflow-engine contract.