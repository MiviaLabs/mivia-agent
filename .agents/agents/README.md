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

These Markdown definitions are the canonical workspace agent definitions loaded
by both the `mivia` binary and workflow engine from `<workspaceRoot>/.agents/agents/*.md`.
The runtime parser (`internal/config/agents_parse.go`) decodes the YAML frontmatter
into `AgentFileSpec` and maps the Markdown body to the agent's `SystemPrompt`.

When orchestration tools or workflow steps dispatch an agent, the runtime discovers
and resolves definitions from this directory.

## Adding a role

1. Pick a name that matches lowercase identifier conventions (`[a-z0-9_-]+`).
2. Match the frontmatter schema above (`name`, `description`, `tools`, etc.).
3. Include clear guidelines and, for ADLC roles, a "Disallowed operations" section.
4. Run `make agents-check` before committing.