# Agent Self-Contained System Prompt

The agent system prompt for **agent mode** (tools enabled) comes from two places:

## Priority order

1. **`.ai/agent-prompt.md`** in the workspace (if it exists) — loaded at runtime
2. **Compiled-in fallback** (`defaultAgentPrompt` in `internal/cli/prompt.go`) — used when no file exists

## How it works

On launch, `runChat` calls `loadAgentPrompt(workspaceDir)`:

1. It looks for `.ai/agent-prompt.md` relative to the workspace root
2. If found and non-empty, that content becomes the system prompt
3. If missing or empty, the compiled-in `defaultAgentPrompt` is used

On first run with tools enabled, `ensureAgentPromptFile` seeds `.ai/agent-prompt.md`
with the default content so the file exists and can be edited.

## Self-maintenance

The key design: **the agent can update its own prompt**.

Since the agent has access to `write_file`, it can write a richer, more
up-to-date version of `.ai/agent-prompt.md` that captures:

- All commits and what each one did
- Full package architecture with file descriptions
- What's been implemented and tested (with test counts)
- Next development priorities in order
- Build, test, and commit conventions

No rebuild needed. The next launch (even after `make build`) reads the file.

## Compiled-in default

Located at **`internal/cli/prompt.go`** → `defaultAgentPrompt`.

It is intentionally **short (~600 bytes)** — just rules, conventions, and
the instruction to update `.ai/agent-prompt.md`. All project state knowledge
lives in the file on disk, not in the binary.

## Updating the compiled default

If the compiled default itself needs updating (new rules, changed conventions),
edit `defaultAgentPrompt` in `internal/cli/prompt.go` and rebuild.

## Human reference

The compiled default and the file-based prompt share the same format.
See `internal/cli/prompt.go` for the exact content.
