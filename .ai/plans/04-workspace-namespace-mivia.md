# 04 — Workspace namespace: `.mivia/`

**Status:** Design-ready.
**Date:** 2026-07-29
**Commits:** `feat(cli): read workspace config from .mivia with .ai fallback`, `docs: document the .mivia workspace namespace`
**Depends on:** `03`. **Blocks:** `05`.
**Blast radius:** MODERATE (changes a public convention).

---

## 1. Problem

`.ai/` currently serves two unrelated purposes under one name:

| Path | Read by | Nature |
|---|---|---|
| `.ai/rules/`, `.ai/doctrines/`, `.ai/policy/`, `.ai/plans/`, `.ai/invariants.md` | Claude Code and dev agents via `CLAUDE.md` | **This repo's development process** — not product surface |
| `.ai/skills/`, `.ai/agent-prompt.md` | the **`mivia` binary**, in any workspace | **Product runtime convention** |

The product half means mivia claims the generic `.ai/` namespace in every user repo it runs in — and, until `03`, writes to it unprompted (`cmd/mivia/main.go:18`).

`.ai/` is a name any agent tool might assume. Squatting it guarantees collisions with tools mivia does not control, and gives users no way to tell mivia's files from another tool's.

## 2. Research

Verified 2026-07-29:

- **No neutral standard directory exists.** [dotagentsprotocol.com](https://dotagentsprotocol.com/) (`.agents/`) is a single-author draft, "DRAFT · 2026-02-24", naming no production implementations. [bgreenwell/dotagents](https://github.com/bgreenwell/dotagents) self-describes as *proposed*. Adopting either buys interop with nothing and couples us to a spec that may change.
- **`AGENTS.md` is the real standard** (Linux Foundation / Agentic AI Foundation; 60k+ repos; 30+ tools) — but it is an instructions file, not agent definitions, and mivia already ships one. Orthogonal to this decision.
- **Agent definitions converged on tool-namespaced directories:** [`.claude/agents/*.md`](https://code.claude.com/docs/en/sub-agents), `.cursor/`, `.roomodes`. Every implementation namespaces to its own tool.

## 3. Decision

**Product runtime namespace is `.mivia/`.** Development-process files stay in `.ai/`.

```
.mivia/
  sessions/            # ALREADY LIVE at HEAD — see note below
  agent-prompt.md      # workspace system prompt        (was .ai/agent-prompt.md)
  agents/<name>.md     # role definitions               (new — see 05)
  skills/<name>/SKILL.md
```

> **`.mivia/` is already occupied.** `chat_command.go:73` does `sess.SessionDir = filepath.Join(wsRoot, ".mivia", "sessions")` with `os.MkdirAll` on every chat (documented at `chat/session.go:50`). So `.mivia/` exists in every workspace mivia has ever run in. Two consequences: (a) the new `internal/workspace/namespace.go` resolver must reconcile with that hardcoded path rather than compete with it; (b) the `.mivia/` → `.ai/` fallback must key on **the specific file being absent**, not on "`.mivia/` does not exist" — otherwise the deprecation notice never fires for existing users.

`mivia.toml` stays at the workspace root — it is user-facing config, not agent-internal state, and moving it breaks every existing install for no benefit.

### Rejected

- **`.agents/`** — a draft with no implementations. Tracking it means tracking someone else's unratified spec.
- **`.claude/agents/`** — another vendor's namespace. Reading it makes mivia silently execute definitions written for a different tool with different enforcement semantics (`permissionMode`, `hooks`, `mcpServers` — none of which mivia honors). Silent partial honoring of a foreign schema is the worst option available. Portability is served instead by an explicit opt-in converter (`mivia agents import --from .claude/agents`) that **errors on fields mivia cannot enforce**. Deferred.
- **Keeping `.ai/`** — leaves the squat in place and the two purposes conflated.

## 4. Compatibility

**Read-path fallback, not a migration.** For each path, prefer `.mivia/`; if absent, read the `.ai/` equivalent. Never write to `.ai/`. Emit a one-line stderr deprecation notice when the fallback fires, naming the file and its `.mivia/` destination.

No auto-migration: moving files in a user's repo without being asked is the same class of action as `03`'s auto-write.

Sequencing note: `03` deletes the write path first, so `04` never has to decide *where* to auto-write — only where to read.

## 5. Configurability — and its boundary

The workspace root is already resolvable. What `04` adds is the namespace directory name.

**Configurable via CLI flag and environment variable only. Not from `mivia.toml`.**

Rationale: `mivia.toml` lives in the workspace and is **agent-writable** — `.toml` is not in `DefaultSecretPathPatterns` (`internal/tools/tools.go:300-306`), so `write_file` can edit it. If the agent-definitions directory were settable from `mivia.toml`, an agent could point role definitions at a directory it controls and define itself a role with `tools = ["run_command"]`. That converts a config knob into a privilege-escalation primitive.

Same reasoning applies to any future guardrail: **a floor the agent can lower is not a floor.**

Companion hardening — **but not as originally drafted.** Three corrections:

1. **`.mivia/**` matches nothing.** `isSecretPath` does `strings.Contains(base, pattern)` (`tools.go:314-330`), a substring test, not a glob. Use a `.mivia/agents/` prefix; do **not** blanket-protect `.mivia/`, which would make the agent unable to read its own `.mivia/sessions/` files.
2. **A bare `mivia.toml` pattern also blocks `mivia.toml.example`,** which `09` §4 requires the agent to edit. Match exactly, or add an exception.
3. **The guard is configurable from the file it guards.** `configuredSecretPaths` (`default_registry.go:67-75`) *replaces* rather than appends, so `[tools].secret_path_patterns` in a workspace `mivia.toml` wipes the defaults. Make mivia-owned patterns non-replaceable.

**And accept the limit honestly:** none of this stops `run_command`. `DefaultAllowlist` includes `sh`/`bash`/`python`/`tee`/`sed`/`cp`/`mv`, and `isSecretPath` is consulted only by the file tools. Any role holding `run_command` writes any file. Path hardening raises the bar for file-tool roles; it is not a boundary. See `09` §2.2.

### Gate `agent-prompt.md` (moved here from `05`)

`.ai/agent-prompt.md` is today an **ungated, unwrapped root system prompt** read verbatim from workspace content: `prompt.go:77` → `loadAgentPrompt` (`:160-175`) → `res.SystemPrompt` (`chat_command.go:44-52`). No gate, no untrusted-content wrapper (contrast `skills/loader.go:74-75`). A cloned repo owns the root agent's system prompt on first `mivia chat`.

This is strictly worse than the role surface `05` §5 gates, and it is already live. Gate it behind the same non-workspace-writable switch, or `05`'s gate is theatre. This plan already edits the resolver, so it lands here.

## 6. Changes

| Site | File | Change |
|---|---|---|
| Prompt path | `internal/cli/prompt.go:77` | `agentPromptPath` const → resolver with `.mivia/` → `.ai/` fallback |
| Prompt bootstrap | `internal/cli/prompt.go:179-190`, `chat_repl.go:69-73` | writes `.ai/agent-prompt.md` today; retarget to `.mivia/` **and** reconcile with `03` (should this still auto-create at all?) |
| Skills dir | `internal/cli/chat_repl.go:87` | resolve `.mivia/skills` → `.ai/skills` |
| Compiled prompt text | `internal/cli/prompt.go:31,72,93,134` | four literals reference `.ai/`; update. Must stay generic — `prompt_generic_test.go` applies |
| Protected paths | `internal/tools/tools.go:300-306` | add `mivia.toml`, `.mivia/**` |

New: `internal/workspace/namespace.go` (~60 LOC) — single resolver, so no site hardcodes a directory name again.

## 7. Verification

```bash
go build ./... && go vet ./...
go test ./internal/cli/... ./internal/workspace/... ./internal/tools/... -race
make verify && make invariants
```

**New tests:** `TestNamespacePrefersMivia`; `TestNamespaceFallsBackToAI` (asserts the deprecation notice); `TestNeverWritesToAIDir`; `TestProtectedPathsCoverMiviaToml`.

**Mutation proofs:**

| # | Mutation | Test that MUST fail |
|---|---|---|
| M1 | Reverse resolver precedence (`.ai/` before `.mivia/`) | `TestNamespacePrefersMivia` |
| M2 | Remove `mivia.toml` from protected patterns | `TestProtectedPathsCoverMiviaToml` |
| M3 | Make the namespace readable from `config.File` | *(none — enforced by review; residual risk named per rule 20)* |

**Docs:** `docs/product/config.md` and `docs/architecture/overview.md` (OWNERS-registered, in-place per rule 40). The namespace is a public convention change and rule 00 requires the canonical doc to ship with it.

**Rollback criterion:** if the fallback proves ambiguous in real workspaces (both directories present with conflicting content), make `.mivia/` exclusive and require explicit migration rather than silently preferring one.
