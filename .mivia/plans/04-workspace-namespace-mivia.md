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

**Project config is `.mivia/mivia.toml`.** The earlier draft kept it at the workspace root on the grounds that it is user-facing config rather than agent-internal state. That distinction does not survive the rest of this plan: `.mivia/` holds every other reviewable input the agent reads — instructions, rules, skills, roles — and config is one more. A root `mivia.toml` would be the last item outside the namespace, which is the fragmentation §3 exists to end.

It carries no secrets by construction (API keys resolve through `api_key_env` names and an env file, never literals), so it is **committed**, not ignored. A tracked config is reviewable in diff, which matters more here than usual: `[[agents.roles]]` entries are privilege grants, and an untracked file grants them invisibly.

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

Rationale: `.mivia/mivia.toml` lives in the workspace and is **agent-writable by design** (see below), so `write_file` can edit it. If the agent-definitions directory were settable from `mivia.toml`, an agent could point role definitions at a directory it controls and define itself a role with `tools = ["run_command"]`. That converts a config knob into a privilege-escalation primitive.

Same reasoning applies to any future guardrail: **a floor the agent can lower is not a floor.**

### Config is deliberately agent-editable — DECIDED

The earlier draft proposed hardening `.mivia/mivia.toml` and `.mivia/agents/` into `DefaultSecretPathPatterns` so the agent could not rewrite its own configuration. **That is rejected.** Agents edit config like any other workspace file.

Two reasons, and the second is why the first is safe:

1. **The guard never worked.** `isSecretPath` does `strings.Contains`, not glob matching (`tools.go`), so the drafted `.mivia/**` pattern matches nothing; a bare `mivia.toml` pattern also blocks `mivia.toml.example`, which `09` §4 requires the agent to edit; and `configuredSecretPaths` *replaced* rather than appended, so `[tools].secret_path_patterns` in a workspace config wiped its own guard. Three defects in a four-line feature is a sign the feature was wrong, not that it needed three fixes.
2. **It was never a boundary.** File tools consult the filter and `run_command` screens argv, but any shell invocation that builds a path at runtime reaches the file anyway. Program invariant 1 (`00` §3) already says enforcement lives at the dispatch boundary; a path filter in front of config was enforcement theatre in exactly the place the program forbids it.

**There is no compiled-in secret pattern list at all.** What counts as a secret is a property of a workspace, not of this binary. `DefaultSecretPathPatterns` / `DefaultSecretPathExceptions` are deleted; `[tools].secret_path_patterns` and `.secret_path_exceptions` are the only source, recommended values ship in `.mivia/mivia.toml.example`, and an unconfigured workspace filters nothing (`TestIsSecretPathUnconfiguredFiltersNothing`). The filter's remaining job is keeping credentials out of model context by accident.

**What this costs, stated plainly:** a workspace with no config gets no `.env` filtering, where it previously got five patterns for free. That is a real reduction in accident resistance for zero-config users, accepted because the alternative is a binary asserting a policy it cannot enforce and cannot get right for every repo.

**What this does *not* change:** the namespace directory still must not be settable from config, for the reason above — config is agent-writable *by design* now, so anything read from it can be lowered by the agent. **A floor the agent can lower is not a floor.** Any future guardrail must resolve from a CLI flag, an environment variable, or a compiled constant that config may only tighten — never from `mivia.toml`. `05` §127 applies this to `mandatory_tool_denylist`.

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
| Secret patterns | `internal/tools/tools.go`, `default_registry.go` | **delete** `DefaultSecretPathPatterns` / `DefaultSecretPathExceptions`; config is the only source. Recommended values move to `.mivia/mivia.toml.example` |

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
- `TestIsSecretPathUnconfiguredFiltersNothing` — no configuration means no filtering, so the removal of the compiled list is asserted rather than assumed.
- `TestAgentCanEditLegacyAIDir` — `.mivia/` is ordinary content: `write_file` into it succeeds, proving it was not made a protected path by accident.

**Mutation proofs:**

| # | Mutation | Test that MUST fail |
|---|---|---|
| M1 | Add a `.mivia/` fallback when the `.mivia/` file is absent | `TestNoHardcodedLegacyNamespace`, `TestWorkspaceIgnoresLegacyAIDir` |
| M2 | Reintroduce a compiled-in secret pattern list | `TestIsSecretPathUnconfiguredFiltersNothing` |
| M3 | Add `.mivia/` to the protected patterns | `TestAgentCanEditLegacyAIDir` |
| M4 | Make the namespace readable from `config.File` | *(none — enforced by review; residual risk named per rule 20)* |

**Docs:** `docs/product/config.md` and `docs/architecture/overview.md` (OWNERS-registered, in-place per rule 40). The namespace is a public convention change and rule 00 requires the canonical doc to ship with it. Because §4 ships no compatibility code, the docs **are** the migration: state plainly that `.mivia/agent-prompt.md` → `.mivia/agent-prompt.md` and `.mivia/skills/` → `.mivia/skills/` must be moved by hand, and that nothing warns when they are not.

**Rollback criterion:** if the clean break proves too sharp in real workspaces, the answer is **not** a fallback (§4 forbids it) — it is to keep `.mivia/` as the namespace and abandon `.mivia/`. Choose one name and compile in exactly one.
