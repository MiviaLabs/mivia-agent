# Agent System Prompt (workspace file)

The agent system prompt for **agent mode** (tools enabled) comes from two places:

## Priority order

1. **`.ai/agent-prompt.md`** in the workspace (if it exists) — loaded at runtime
2. **Compiled-in fallback** (`defaultAgentPrompt` in `internal/cli/prompt.go`) — used when no file exists

## How it works

On launch, `runChat` calls `loadAgentPrompt(workspaceDir)`:

1. Looks for `.ai/agent-prompt.md` relative to the workspace root
2. If found and non-empty, that content becomes the system prompt
3. If missing or empty, the compiled-in `defaultAgentPrompt` is used

On first run with tools enabled, `ensureAgentPromptFile` seeds `.ai/agent-prompt.md`
with the **generic** compiled default if the file is missing.

## What belongs in `.ai/agent-prompt.md`

Depends on the workspace:

| Workspace | Content |
|-----------|---------|
| **This mivia-agent repo** | Durable **meta-orientation**: you are working on the agent product itself; host (Go) vs model-facing tools (must stay language-generic). **No** feature lists, test counts, commit digests, or “current state.” |
| **Any other project** | That project’s stable conventions only (how to build/test, layout pointers). Still not a living status dump. |

Agents must **discover** current implementation with tools. Putting state in the prompt causes confusion and drift.

## What must stay out of the prompt

- Package inventories with test counts
- “Key features” / “NEW” changelogs
- Next priorities / roadmaps
- Session progress or commit-by-commit history

## Compiled-in default

`internal/cli/prompt.go` → `defaultAgentPrompt`.

- Short and **project/language-generic** (any user workspace).
- Must not hardcode this repo’s Go-only build/test menu.
- Guards: `internal/cli/prompt_generic_test.go`, `.ai/rules/60-tools-project-language-generic.md`.

## Updating the compiled default

Edit `defaultAgentPrompt` in `internal/cli/prompt.go` and rebuild when the **universal** fallback contract changes.

## Related

- This repo’s orientation prompt: `.ai/agent-prompt.md`
- Generic tools rule: `.ai/rules/60-tools-project-language-generic.md`
