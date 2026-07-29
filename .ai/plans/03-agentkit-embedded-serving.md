# 03 — Agentkit: serve embedded instructions, stop materializing them

**Status:** Design-ready; one open decision (§6).
**Date:** 2026-07-29
**Commits:** `fix(agent): serve embedded instructions from the binary`, `fix(build): restrict ship corpus to generic content`, `docs: document embedded-instruction resolution`
**Depends on:** nothing. **Blocks:** `04`, `05`, `06` (owns the shared frontmatter parser).
**Blast radius:** MODERATE (changes what mivia writes into user repos).

---

## 1. Intent vs. implementation

**Intended:** certain skills and instructions are embedded *in the harness* and served from the binary at runtime. Workspace files override.

**Actual:** the serving half was built and never wired; the only live path writes the corpus to disk.

`internal/agentkit` exposes six runtime-serving functions — `Rule()`, `Doctrine()`, `Skill()`, `Resolve()`, `AgentInstructions()`, `Version()`. **Zero production callers** (verified by grep across `internal/`, `cmd/`, excluding tests). The single wired call is:

```go
// cmd/mivia/main.go:14-15, 18
// On startup, ensure agent instructions are present in the workspace.
if err := agentkit.EnsureInstructions(cwd); err != nil {
```

→ `EnsureInstructions` (`agentkit.go:109`) → `WriteInstructions` (`:52`) → writes every embedded file into the user's cwd.

## 2. Three distinct defects

### D1 — Host-specific content leaks into the ship corpus

`agentkitdata/gen_embed.go:14-17` states the design: *"ship/ has GENERIC versions, .ai/ has HOST-SPECIFIC versions. We embed ship/ content into the binary; .ai/ stays on disk as host override."* Sound. But the generator walks `ship/` **then falls back to `.ai/`** for anything not in `ship/` (`gen_embed.go:35,46`), filtered by a `skipPrefixes` denylist (`:55`).

`ship/` contains 7 files (`INDEX.md`, `AGENTS.md`, rules `00`/`01`/`05`/`10`/`80`). **19 files are embedded.** The other 12 come from the `.ai/` fallback:

```
.ai/doctrines/evidence-before-claims.md
.ai/doctrines/verification-is-part-of-delivery.md
.ai/rules/50-concurrency-subagents.md
.ai/rules/60-tools-project-language-generic.md
.ai/rules/70-long-running-heartbeat.md
.ai/skills/{bug-audit,concurrency-review,engineering-working-contract,
            feature-delivery,secure-change,verify-change,verify-code-change}/SKILL.md
```

These are **mivia's own development process**, compiled into the shipped binary. This is a rule-60 generic-surface leak that no test catches — `generic_surface_test.go` covers tool `Description()` and `prompt_generic_test.go` covers prompts, but nothing asserts the embedded corpus is generic.

The denylist design is inverted: a *skip* list fails open, so every new `.ai/` file ships by default.

### D2 — The corpus is materialized to disk

`WriteInstructions` writes 19 files into the user's working directory. It runs from `main()` for **every subcommand**, including `mivia version`. `HasLocalOverride` (`:78`) short-circuits when `AGENTS.md` or any `.ai/**/*.md` exists — which is why this repo never sees it — but a fresh user project gets mivia's rules, doctrines, and dev skills dumped into it, unprompted.

This is the opposite of "embedded in the harness."

### D3 — The serving API is dead

`Rule`/`Doctrine`/`Skill`/`Resolve`/`AgentInstructions` have no callers. `Resolve` (`:118`) already implements exactly the right semantics — local file first, embedded fallback — and is unused.

---

## 3. Target design

**Embedded content is served, never written.** Resolution order for any instruction path:

```
workspace override  (.mivia/… , then .ai/… for back-compat — see 04)
  ↓ miss
embedded ship corpus (compiled in)
  ↓ miss
error / empty
```

`agentkit.Resolve` is already this function. Wire it; delete what materializes.

### Consumers to wire (D3)

| Consumer | Today | After |
|---|---|---|
| `loadAgentPrompt` (`cli/prompt.go:160`) | reads `.ai/agent-prompt.md`, else compiled fallback const | `agentkit.Resolve(root, "<ns>/agent-prompt.md")`, else compiled fallback |
| `skills.LoadMarkdown` (`cli/chat_repl.go:87`) | workspace dir only | union: embedded skills + workspace skills, workspace wins on name |
| Role definitions (`05`) | — | same union; built-in roles ship embedded |

The skill union is the reason `03` blocks `06`: `06` cannot define role→skill binding until "which skills exist" has one answer.

---

## 4. Changes

### 4a. `agentkitdata/gen_embed.go` — invert the filter (D1)

Replace the `skipPrefixes` denylist with an **explicit ship allowlist**. A file is embedded only if it is in `ship/`. Remove the `.ai/` fallback walk (`:46-62`) entirely.

Anything currently reaching the binary via fallback must either be **copied into `ship/` as a genericized version** or **dropped from the corpus**. Per-file disposition is the §6 open decision.

Add `agentkitdata/generic_corpus_test.go`: every embedded file is checked against the same forbidden patterns as `internal/tools/generic_surface_test.go` (`go test`, `cmd/mivia`, `github.com/MiviaLabs`, module path). This closes the rule-60 gap and makes D1 non-recurring.

### 4b. Delete BOTH startup write paths (D2)

> **There are two, not one.** Besides `EnsureInstructions`, `configureChatWorkspace` writes: `chat_repl.go:69-74` → `ensureAgentPromptFile` → `os.MkdirAll(<root>/.ai)` + `os.WriteFile(.ai/agent-prompt.md, …)` (`prompt.go:182-200`), unconditionally on every `mivia chat` in a fresh workspace. Deleting only the `agentkit` path leaves `TestNoStartupFilesystemWrite` failing and M2 unsatisfiable. `04` §6 raised this as an open question; it is resolved **here**, because it is the same defect class. `04` then only decides *where to read*.

#### `internal/agentkit/agentkit.go`

Delete `WriteInstructions`, `EnsureInstructions`, `HasLocalOverride`. Delete the `agentkit.EnsureInstructions` call and the import from `cmd/mivia/main.go:8, 16-21`.

Keep and wire `Resolve`, `Rule`, `Doctrine`, `Skill`, `AgentInstructions`, `Version`. Anything still unused after `04`/`06` land gets deleted rather than kept speculatively.

`agentkit.go` drops from 125 to ~60 LOC.

### 4c. Wire the consumers (D3)

`cli/prompt.go`: `loadAgentPrompt` resolves through `agentkit.Resolve`. The compiled generic fallback (`prompt.go:21-76`) stays — it is the last resort and is already covered by `prompt_generic_test.go`.

`internal/skills`: add `LoadEmbedded()` returning definitions parsed from the embedded corpus, and merge with `LoadMarkdown` results (workspace wins). Note this depends on the frontmatter parser rewrite in `05` §6 — the current parser silently drops list-valued keys (`loader.go:107-119`), so embedded skills declaring `tools:` would lose them. **The parser lands HERE**, in a shared location `05` §6 then consumes. Taking the alternative — sequencing `05`'s parser first — reinstates the `03 → 05 → 04 → 03` cycle (`00` §4).

---

## 5. Migration and user impact

**Breaking for anyone relying on auto-materialization.** Today a fresh project gets a `.ai/` tree written on first run; after this it does not, and mivia serves the same content from the binary instead. Behavior when a workspace override exists is unchanged.

Replacement for users who *want* files on disk: an explicit `mivia init` (or `mivia agents init`) that writes the corpus on request. Opt-in, never on startup. Scope it in `04` alongside the namespace decision, or defer — but the auto-write must not survive.

**Do not extend materialization to roles.** `.mivia/agents/` must never be auto-populated: silently writing role definitions into a user's repo is writing privilege grants into their repo.

---

## 6. Disposition of the 12 fallback files — DECIDED

| File | Disposition |
|---|---|
| `.ai/rules/50-concurrency-subagents.md` | **Genericize → `ship/rules/`** |
| `.ai/rules/60-tools-project-language-generic.md` | **Genericize → `ship/rules/`** |
| `.ai/rules/70-long-running-heartbeat.md` | **Genericize → `ship/rules/`** |
| `.ai/doctrines/evidence-before-claims.md` | **Genericize → `ship/doctrines/`** |
| `.ai/doctrines/verification-is-part-of-delivery.md` | **Genericize → `ship/doctrines/`** |
| `.ai/skills/bug-audit/SKILL.md` | **Drop** |
| `.ai/skills/concurrency-review/SKILL.md` | **Drop** |
| `.ai/skills/engineering-working-contract/SKILL.md` | **Drop** |
| `.ai/skills/feature-delivery/SKILL.md` | **Drop** |
| `.ai/skills/secure-change/SKILL.md` | **Drop** |
| `.ai/skills/verify-change/SKILL.md` | **Drop** |
| `.ai/skills/verify-code-change/SKILL.md` | **Drop** |

**Rationale.** The five rules/doctrines encode broadly applicable engineering discipline and are worth shipping once genericized. The seven skills are mivia's *own* engineering process — they name mivia's Makefile targets, its `mivia-report/v1` template, its invariant IDs, and its Go toolchain. Shipping them puts this repo's development workflow into every user's binary: both a rule-60 leak and a product statement nobody made deliberately.

Dropped skills stay in `.ai/skills/` and continue to work for mivia's own development. They simply stop being embedded.

**Genericizing is real work, not a copy.** Each of the five must lose mivia/Go specifics (`make verify`, `go test ./...`, module paths, `.ai/` references) and is then covered by `TestEmbeddedCorpusIsGeneric` (§4a). **If a file cannot be genericized without losing its meaning, drop it** rather than ship a watered-down version — a vague rule in a user's binary is worse than no rule.

---

## 7. Verification

```bash
go build ./... && go vet ./...
go test ./internal/agentkit/... ./agentkitdata/... ./internal/cli/... ./internal/skills/... -race
make verify          # includes docs-check, secret-scan, structure-check, semgrep
make invariants
```

**New tests:** `TestEmbeddedCorpusIsGeneric` (4a); `TestResolvePrefersWorkspace`; `TestSkillsUnionWorkspaceWins`; `TestNoStartupFilesystemWrite` — **built-binary** subprocess run in a temp cwd asserting the directory is unchanged, covering both `mivia version` and `mivia chat` (rule 20 forbids fake-only closure for shipped commands, and this changes behavior of every invocation).

**Docs:** §5 calls this "breaking for anyone relying on auto-materialization" — a user-visible behavior removal, so rule 00 requires the canonical doc to ship with it. `docs/product/config.md` (OWNERS `product-config`).

**Mutation proofs:**

| # | Mutation | Test that MUST fail |
|---|---|---|
| M1 | Restore the `.ai/` fallback walk in `gen_embed.go` | `TestEmbeddedCorpusIsGeneric` |
| M2 | Re-add `agentkit.EnsureInstructions(cwd)` to `main()` | `TestNoStartupFilesystemWrite` |
| M3 | Make `Resolve` check embedded before local | `TestResolvePrefersWorkspace` |

**Rollback criterion:** if serving-from-binary breaks a documented workflow that depends on the materialized tree, keep the serving path and reintroduce materialization **only** behind an explicit `mivia init` — never on startup.
