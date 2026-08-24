# Agent system prompt (file-backed agents)

The agent system prompt for **agent mode** (tools enabled) comes from:

## Priority order

1. **Selected agent definition** under `.agents/agents/<name>.md` (or `~/.agents/agents/`)
   - When `--agent` is omitted and a definition named **`mivia`** exists, it is auto-selected.
2. **`[chat].system_prompt`** in config (if set and not stripped by the workspace gate)
3. **Compiled-in fallback** (`defaultAgentPrompt` in `internal/cli/prompt.go`)

Workspace agent files **always load** from `<workspace>/.agents/agents/`. They
replace the former `.mivia/agent-prompt.md` surface.

## How it works

On launch, `runChat`:

1. Discovers agent TOML files (user + workspace)
2. Resolves immutable definitions
3. Selects `--agent <name>`, or defaults to **`mivia`** when present
4. Applies that definition’s `system_prompt` and tool scope

**mivia never creates agent files.** You author them (or the agent writes them
with `write_file`); the next launch picks them up.

## What belongs in `.agents/agents/*.md`

| Workspace | Content |
|-----------|---------|
| **This mivia-agent repo** | Durable **meta-orientation** in `mivia.toml` + specialists (e.g. `go-engineer.toml`). Host (Go) vs model-facing tools (language-generic). **No** feature lists, test counts, or living state. Guarded by `internal/cli/agent_prompt_repo_test.go`. |
| **Any other project** | That project’s stable conventions in agent `system_prompt` fields. |

### Optional fields

| Field | Meaning |
|-------|---------|
| `tools` / `tools_add` / `tools_remove` / `disallowed_tools` | Tool allowlist (see [Named agents](../product/agent.md#named-agents-and-skill-binding)) |
| `skills` | Skill **invocation** allowlist (see [Skill System Architecture](../architecture/skills.md#agent-skill-binding)): omit = all trusted skills; `[]` = none; list = only those skill handlers |
| `system_prompt`, `model`, `max_turns`, `inherits` | Prompt, model, turn cap, inheritance |

Example (this repo’s go-engineer — skills list as shipped in `.agents/agents/go-engineer.md`):

```toml
skills = [
  "architecture-review",
  "concurrency-review",
  "docs-update",
  "feature-delivery",
  "secure-change",
  "verify-change",
  "verify-code-change",
]
```

Root `mivia.toml` omits `skills` so the orchestrator may invoke any trusted
skill. Enforcement is at the task boundary when the model selects the task's
explicit `agent` and optional `skill` fields.

## User gate (`load_workspace_config`)

Put only in **`~/.mivia/mivia.toml`** (workspace `[agents]` values are ignored):

Workspace configuration is enabled by default. To opt out, put this only in
**`~/.mivia/mivia.toml`** (workspace `[agents]` values are ignored):

```toml
[agents]
load_workspace_config = false
```

That gate controls workspace **skills** (project skill discovery for
the session) and workspace **`[chat]` / `[subagents]` system prompts** - not
agent file discovery. When the gate is off, only user skills load, so a
project skill cannot shadow then remove a user skill of the same name.

## Compiled-in default

`internal/clichat/prompt.go` → `defaultAgentPrompt`.

- Project/language-generic for any user workspace
- Guards: `internal/clichat/prompt_generic_test.go`, rule 60

## Related

- This repo’s agents: `.agents/agents/*.md` (mivia, go-engineer, researcher,
  reviewer, security, docs, verifier)
- Namespace: `internal/workspace/namespace.go`, `internal/config/agents.go`
