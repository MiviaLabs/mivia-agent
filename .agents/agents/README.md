# Subagent role definitions (`.agents/agents/`)

This directory holds Markdown subagent role definitions for the human and
ADLC-driven development workflow. The four standard roles mirror the
delivery loop in `AGENTS.md` and `.agents/rules/05-adlc-agentic-development-lifecycle.md`:

| Role | ADLC step | Reads | Writes | Output verdict |
|------|-----------|-------|--------|----------------|
| `planner.md` | 0-1 (Plan + Breakdown) | yes | no | plan block in context |
| `plan-reviewer.md` | 0 (challenge) | yes | no | `approved` / `changes_requested` |
| `builder.md` | 5 (implement) | yes | yes | chunk log + `## Done` |
| `reviewer.md` | 6 (review) | yes | no | `approved` / `changes_requested` |

**Real dispatch note:** `planner.md` and `plan-reviewer.md` map to ADLC
steps 0-1 conceptually, but neither the ADLC rule's own Step 0 dispatch
example nor the compiled workflow engine calls them by name today. Step
0's example dispatches the generic `reviewer` + `auditor` roles. The
compiled engine's shape varies by workflow: `feature-delivery.toml` uses
`workflow-engineer` plus a `panel-reviewer`/`review-synthesizer` panel;
`bug-fix.toml`/`bug-fix-fast.toml` instead gate review with an active
`agent = "reviewer"` triage step (their panel/review layers are currently
cut, see `docs/development/debug-cut.md`). `planner`/`plan-reviewer` are
standalone roles a human can select or dispatch directly for ad-hoc plan
work outside the automated loop. Treat this table as role-to-step mapping
by design intent, not as a claim about what currently executes
automatically.

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
and resolves definitions from this directory. Two compiled built-ins also ship
inside the binary and need no files here: `general-orchestrator` (the reserved
root session identity) and `general-purpose` (a spawnable agent present in every
session). A file of the same name overrides `general-purpose`; the
`general-orchestrator` name is reserved and cannot be defined by a file.

## Adding a role

1. Pick a name that matches `^[a-z0-9][a-z0-9_-]*$`. The `mivia` role is a
   compiled root role and has no Markdown file here.
2. Match the frontmatter schema above (`name`, `description`, `tools`, etc.).
3. Include clear guidelines and, for ADLC roles, a "Disallowed operations" section.
4. Run `make agents-check` before committing.
