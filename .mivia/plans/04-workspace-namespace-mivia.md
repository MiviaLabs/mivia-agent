# 04 — Workspace namespace: `.mivia/`

**Status:** Design-ready.
**Date:** 2026-07-29
**Commits:** `feat(cli): read workspace config from .mivia with .ai fallback`, `docs: document the .mivia workspace namespace`
**Depends on:** nothing. **Blocks:** `05`.
**Blast radius:** MODERATE (changes a public convention; breaking for `.mivia/` users — §4).

---

## 1. Problem

`.mivia/` currently serves two unrelated purposes under one name:

| Path | Read by | Nature |
|---|---|---|
| `.mivia/rules/`, `.mivia/doctrines/`, `.mivia/policy/`, `.mivia/plans/`, `.mivia/invariants.md` | Claude Code and dev agents via `CLAUDE.md` | **This repo's development process** — not product surface |
| `.mivia/skills/`, `.mivia/agent-prompt.md` | the **`mivia` binary**, in any workspace | **Product runtime convention** |

The product half means mivia claims the generic `.mivia/` namespace in every user repo it runs in. The auto-write half is already gone (`121ee0b`, `f439686`); what remains is the read-path squat.

`.mivia/` is a name any agent tool might assume. Squatting it guarantees collisions with tools mivia does not control, and gives users no way to tell mivia's files from another tool's.

## 2. Research

Verified 2026-07-29:

- **No neutral standard directory exists.** [dotagentsprotocol.com](https://dotagentsprotocol.com/) (`.agents/`) is a single-author draft, "DRAFT · 2026-02-24", naming no production implementations. [bgreenwell/dotagents](https://github.com/bgreenwell/dotagents) self-describes as *proposed*. Adopting either buys interop with nothing and couples us to a spec that may change.
- **`AGENTS.md` is the real standard** (Linux Foundation / Agentic AI Foundation; 60k+ repos; 30+ tools) — but it is an instructions file, not agent definitions, and mivia already ships one. Orthogonal to this decision.
- **Agent definitions converged on tool-namespaced directories:** [`.claude/agents/*.md`](https://code.claude.com/docs/en/sub-agents), `.cursor/`, `.roomodes`. Every implementation namespaces to its own tool.

## 3. Decision

**Product runtime namespace is `.mivia/`, and it is the only one.** `.mivia/` loses all special meaning to the binary.

> **`.mivia/` is not a fallback, a second namespace, or a protected path — it is ordinary workspace content.** The string `.ai` must not appear anywhere in the shipped codebase: not as a path constant, not in compiled prompt text, not in a secret-path pattern. Agents read and edit `.mivia/` with the normal file tools exactly as they would `docs/` or `src/`, because to the binary it is not distinguishable from them. This repo keeps using `.mivia/` for its own development process, but that is a convention of *this workspace*, invisible to the product.
>
> This is stricter than "prefer `.mivia/`". A fallback would keep `.ai` compiled in and keep the squat alive in weaker form; see §4.

```
.mivia/
  sessions/            # ALREADY LIVE at HEAD — see note below
  agent-prompt.md      # workspace system prompt        (was .mivia/agent-prompt.md)
  agents/<name>.md     # role definitions               (new — see 05)
  skills/<name>/SKILL.md
```

> **`.mivia/` is already occupied.** `chat_command.go:73` does `sess.SessionDir = filepath.Join(wsRoot, ".mivia", "sessions")` with `os.MkdirAll` on every chat (documented at `chat/session.go:50`). So `.mivia/` exists in every workspace mivia has ever run in. Two consequences: (a) the new `internal/workspace/namespace.go` resolver must reconcile with that hardcoded path rather than compete with it; (b) the `.mivia/` → `.mivia/` fallback must key on **the specific file being absent**, not on "`.mivia/` does not exist" — otherwise the deprecation notice never fires for existing users.

`mivia.toml` stays at the workspace root — it is user-facing config, not agent-internal state, and moving it breaks every existing install for no benefit.

### Rejected

- **`.agents/`** — a draft with no implementations. Tracking it means tracking someone else's unratified spec.
- **`.claude/agents/`** — another vendor's namespace. Reading it makes mivia silently execute definitions written for a different tool with different enforcement semantics (`permissionMode`, `hooks`, `mcpServers` — none of which mivia honors). Silent partial honoring of a foreign schema is the worst option available. Portability is served instead by an explicit opt-in converter (`mivia agents import --from .claude/agents`) that **errors on fields mivia cannot enforce**. Deferred.
- **Keeping `.mivia/`** — leaves the squat in place and the two purposes conflated.
- **`.mivia/` with a `.mivia/` read-path fallback** — the original draft of this plan. Rejected: a fallback keeps `.ai` hardcoded in the binary, which is the squat it claims to end. It also creates a two-source-of-truth resolver whose precedence is invisible to the user, and the deprecation notice it needs is itself another `.ai` hardcode. See §4.

## 4. Compatibility — a clean break

**No fallback, no migration, no deprecation notice.** `.mivia/` is read; `.mivia/` is not consulted. A workspace with `.mivia/agent-prompt.md` and `.mivia/skills/` and no `.mivia/` equivalents gets the compiled default prompt and no workspace skills — silently, because the binary has no way to know `.mivia/` was ever meaningful.

That silence is the honest cost of §3's rule, and it is accepted deliberately:

- **A notice would require hardcoding `.ai`.** Detecting "you have files in the old place" means compiling in the old place. The rule in §3 admits no exception for diagnostics.
- **The blast radius is small and self-correcting.** Both affected paths are opt-in workspace customizations, not defaults; a user who set one up notices immediately on the next `mivia chat` because their prompt or skills are gone.
- **No auto-migration.** Moving files in a user's repo without being asked is the same class of action as the auto-write that `121ee0b`/`f439686` removed. Users move two paths by hand.

Migration is therefore a **documentation** deliverable, not a code path — see §7 Docs.

> **No legacy path ships. Not a fallback, not a deprecation window, not an importer, not a `--from-legacy` flag.** Any of those is a compiled-in `.ai` and a second code path to maintain forever in exchange for a one-time two-directory rename the user can do with `mv`. If this section ever grows a compatibility mechanism, §3's rule has been abandoned — reopen the decision instead of weakening it quietly.

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

`.mivia/agent-prompt.md` is today an **ungated, unwrapped root system prompt** read verbatim from workspace content: `prompt.go:77` → `loadAgentPrompt` (`:160-175`) → `res.SystemPrompt` (`chat_command.go:44-52`). No gate, no untrusted-content wrapper (contrast `skills/loader.go:74-75`). A cloned repo owns the root agent's system prompt on first `mivia chat`.

This is strictly worse than the role surface `05` §5 gates, and it is already live. Gate it behind the same non-workspace-writable switch, or `05`'s gate is theatre. This plan already edits the resolver, so it lands here.

## 6. Changes

Re-derived at HEAD 2026-07-29 (`grep -rn '\.ai' --include=*.go`). Two functional hardcodes and six in text; there is no longer any write path to retarget.

| Site | File | Change |
|---|---|---|
| Prompt path | `internal/cli/prompt.go:77` | `const agentPromptPath = ".mivia/agent-prompt.md"` → resolver call, `.mivia/agent-prompt.md`. Sole read site is `:164` |
| Skills dir | `internal/cli/chat_repl.go:78` | `filepath.Join(root, ".mivia", "skills")` → resolver call, `.mivia/skills` |
| Compiled prompt text | `internal/cli/prompt.go:31,72,93,134` | four model-facing literals name `.mivia/`; retarget to `.mivia/`. Also a standing rule-60 leak — `.mivia/` is *this repo's* convention compiled into every user's binary |
| Source comments | `internal/cli/prompt.go:14,21,24,25,152,157` | comments referencing `.mivia/` paths and `.mivia/rules/60-*.md`; update so the grep gate in §7 can be exact-match |
| Protected paths | `internal/tools/tools.go:300-306` | add `mivia.toml` (exact match — see §5.2) and a `.mivia/agents/` prefix (**not** `.mivia/**` — see §5.1) |

**Prompt bootstrap: nothing to do.** The original draft listed `prompt.go:179-190` and `chat_repl.go:69-73` as write sites to retarget. `121ee0b` deleted both (`ensureAgentPromptFile` and its caller). mivia no longer creates `agent-prompt.md` anywhere, and §3 forbids reintroducing it. The `.mivia/` prompt file is created by the user or by the agent via `write_file`, like any other workspace file.

New: `internal/workspace/namespace.go` (~40 LOC) — single resolver owning the namespace directory name, so no call site hardcodes it again. Smaller than the ~60 LOC originally scoped: with no fallback there is no precedence logic, no staleness detection, and no notice emitter to write.

## 7. Verification

```bash
go build ./... && go vet ./...
go test ./internal/cli/... ./internal/workspace/... ./internal/tools/... -race
make verify && make invariants
```

**New tests:**

- `TestNamespaceResolvesMivia` — the resolver returns `.mivia/` paths for the prompt and skills.
- `TestWorkspaceIgnoresLegacyAIDir` — a workspace containing `.mivia/agent-prompt.md` and `.mivia/skills/` and **no** `.mivia/` yields the compiled default prompt and an empty skill registry. This is the §4 clean break asserted as behavior, not just documented.
- `TestNoHardcodedLegacyNamespace` — **the load-bearing test for §3.** Walks the Go sources and fails on any occurrence of `".mivia"` or `.mivia/` outside `_test.go`. Mechanical, so the rule cannot erode through an innocent-looking one-line fallback later. Must ignore string matches inside URLs (`openrouter.ai`, `api.z.ai` in `providerregistry/registry.go:24,28` match a naive substring search).
- `TestProtectedPathsCoverMiviaToml` — and that `mivia.toml.example` stays writable (§5.2).
- `TestAgentCanEditLegacyAIDir` — `.mivia/` is ordinary content: `write_file` into it succeeds, proving it was not made a protected path by accident.

**Mutation proofs:**

| # | Mutation | Test that MUST fail |
|---|---|---|
| M1 | Add a `.mivia/` fallback when the `.mivia/` file is absent | `TestNoHardcodedLegacyNamespace`, `TestWorkspaceIgnoresLegacyAIDir` |
| M2 | Remove `mivia.toml` from protected patterns | `TestProtectedPathsCoverMiviaToml` |
| M3 | Add `.mivia/` to the protected patterns | `TestAgentCanEditLegacyAIDir` |
| M4 | Make the namespace readable from `config.File` | *(none — enforced by review; residual risk named per rule 20)* |

**Docs:** `docs/product/config.md` and `docs/architecture/overview.md` (OWNERS-registered, in-place per rule 40). The namespace is a public convention change and rule 00 requires the canonical doc to ship with it. Because §4 ships no compatibility code, the docs **are** the migration: state plainly that `.mivia/agent-prompt.md` → `.mivia/agent-prompt.md` and `.mivia/skills/` → `.mivia/skills/` must be moved by hand, and that nothing warns when they are not.

**Rollback criterion:** if the clean break proves too sharp in real workspaces, the answer is **not** a fallback (§4 forbids it) — it is to keep `.mivia/` as the namespace and abandon `.mivia/`. Choose one name and compile in exactly one.
