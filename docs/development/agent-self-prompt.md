# Agent system prompt (file-backed agents)

The agent system prompt for **agent mode** (tools enabled) comes from:

## Priority order

1. **Selected agent definition** under `.mivia/agents/<name>.toml` (or `~/.mivia/agents/`)
   - When `--agent` is omitted and a definition named **`mivia`** exists, it is auto-selected.
2. **`[chat].system_prompt`** in config (if set and not stripped by the workspace gate)
3. **Compiled-in fallback** (`defaultAgentPrompt` in `internal/cli/prompt.go`)

Workspace agent files **always load** from `<workspace>/.mivia/agents/`. They
replace the former `.mivia/agent-prompt.md` surface.

## How it works

On launch, `runChat`:

1. Discovers agent TOML files (user + workspace)
2. Resolves immutable definitions
3. Selects `--agent <name>`, or defaults to **`mivia`** when present
4. Applies that definition’s `system_prompt` and tool scope

**mivia never creates agent files.** You author them (or the agent writes them
with `write_file`); the next launch picks them up.

## What belongs in `.mivia/agents/*.toml`

| Workspace | Content |
|-----------|---------|
| **This mivia-agent repo** | Durable **meta-orientation** in `mivia.toml` + specialists (e.g. `go-engineer.toml`). Host (Go) vs model-facing tools (language-generic). **No** feature lists, test counts, or living state. Guarded by `internal/cli/agent_prompt_repo_test.go`. |
| **Any other project** | That project’s stable conventions in agent `system_prompt` fields. |

## User gate (`load_workspace_config`)

Put only in **`~/.mivia/mivia.toml`** (workspace `[agents]` values are ignored):

```toml
[agents]
load_workspace_config = true
```

That gate controls workspace **skill handlers** and workspace **`[chat]` /
`[subagents]` system prompts** — not agent file discovery.

## Compiled-in default

`internal/cli/prompt.go` → `defaultAgentPrompt`.

- Project/language-generic for any user workspace
- Guards: `internal/cli/prompt_generic_test.go`, rule 60

## Related

- This repo’s agents: `.mivia/agents/mivia.toml`, `.mivia/agents/go-engineer.toml`
- Namespace: `internal/workspace/namespace.go`, `internal/config/agents.go`
