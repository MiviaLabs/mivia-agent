# Agent System Prompt (workspace file)

The agent system prompt for **agent mode** (tools enabled) comes from two places:

## Priority order

1. **`.mivia/agent-prompt.md`** in the workspace (if it exists) — loaded at runtime
2. **Compiled-in fallback** (`defaultAgentPrompt` in `internal/cli/prompt.go`) — used when no file exists

## How it works

On launch, `runChat` calls `loadAgentPrompt(workspaceDir)`:

1. Looks for `.mivia/agent-prompt.md` relative to the workspace root
2. If found and non-empty, that content becomes the system prompt
3. If missing or empty, the compiled-in `defaultAgentPrompt` is used

**mivia never creates this file.** You write it, or the agent writes it with
`write_file`; the next launch picks it up. Nothing is seeded on startup.

> ### Moved from `.ai/` — migrate by hand
>
> The workspace prompt used to be read from `.ai/agent-prompt.md`, and workspace
> skills from `.ai/skills/`. Both now live under `.mivia/`. **There is no
> fallback and no warning:** a workspace still holding the old paths silently
> gets the compiled default prompt and no skills.
>
> ```sh
> mkdir -p .mivia
> mv .ai/agent-prompt.md .mivia/agent-prompt.md
> mv .ai/skills .mivia/skills
> ```
>
> `.ai/` was a generic name that any agent tool might claim, so mivia stopped
> claiming it. The binary now attaches no meaning to `.ai/` at all — agents read
> and edit it with the normal file tools, like any other directory.
>
> *(In this repo, `.ai/` remains the home of mivia's **own** development process
> — rules, doctrines, dev skills, plans. That is a convention of this workspace,
> read by the humans and dev agents working on mivia, not by the product.)*

mivia dogfoods this: its own workspace prompt moved to `.mivia/agent-prompt.md`
and is tracked in git. `.gitignore` excludes only the generated subtrees
(`.mivia/sessions/`, `.mivia/worktrees/`), so committed workspace configuration
stays visible and reviewable.

## What belongs in `.mivia/agent-prompt.md`

Depends on the workspace:

| Workspace | Content |
|-----------|---------|
| **This mivia-agent repo** | Durable **meta-orientation**: you are working on the agent product itself; host (Go) vs model-facing tools (must stay language-generic). **No** feature lists, test counts, commit digests, or “current state.” Guarded by `internal/cli/agent_prompt_repo_test.go`. |
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
- Guards: `internal/cli/prompt_generic_test.go`, rule 60 (tools, project and language generic).
- The namespace itself is guarded by `TestNoHardcodedLegacyNamespace` (`internal/workspace/`), which fails the build if `.ai` reappears anywhere in the shipped tree.

## Updating the compiled default

Edit `defaultAgentPrompt` in `internal/cli/prompt.go` and rebuild when the **universal** fallback contract changes.

## Related

- This repo’s orientation prompt: `.mivia/agent-prompt.md`
- Namespace resolver: `internal/workspace/namespace.go`
- Namespace decision and rationale: `.ai/plans/04-workspace-namespace-mivia.md`
