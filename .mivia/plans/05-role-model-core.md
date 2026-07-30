# 05 — Role model core: hybrid TOML + markdown

**Status:** Design-ready.
**Date:** 2026-07-29
**Commits:** `feat(agent): add declarative agent roles`, `feat(cli): resolve roles from TOML and workspace files`
**Depends on:** `01` (enforcement) and `04` (namespace) — **both shipped 2026-07-29, so this is unblocked.** **Blocks:** `06`, `07`.
**Blast radius:** HIGH (privilege surface).

---

## 1. Goal

Named roles — `researcher` (read-only), `engineer` (full edit), `reviewer` (read-only + audit) — each with its own system prompt and scoped tools, authored **either** in `mivia.toml` **or** as `.mivia/agents/<name>.md`, with TOML able to tighten a markdown role.

## 2. Preconditions

`05` is decorative without these. `P1` is `01`; `P2` and `P3` land here.

| # | Change | Why | Where |
|---|---|---|---|
| **P1** | Dispatch-boundary enforcement | `Loop.Tools` is advertisement-only; without this every guarantee below is prompt-level only | plan `01` |
| **P2** | Frontmatter parser that handles lists | `internal/skills/loader.go:107-119` recognizes only `name`/`description`; `tools: [a, b]` would parse as the literal string `"[a, b]"`. **Live bug:** `.mivia/skills/concurrency-review/SKILL.md` declares a `triggers:` list that is silently dropped today | §6 |
| **P3** | Hoist `skills.LoadMarkdown` out of `attachSessionDispatcher` into `runChat` | Skills load *after* the tool registry (`chat_repl.go:87`), splitting role validation across two points that cannot see each other | `cli/chat_command.go`, `chat_repl.go:80-97`; 2 test call sites (`interactive_session_test.go:89,216`) |

## 3. Sources and precedence

| Rank | Source | On name collision |
|---|---|---|
| 1 | Built-in `default` role (compiled, generic per rule 60) | base |
| 2 | User markdown `~/.config/mivia/agents/<n>.md` | whole-role replacement |
| 3 | Project markdown `.mivia/agents/<n>.md` (gated, see §5) | whole-role replacement |
| 4 | TOML `[[agents.roles]]` | **narrowing merge** |
| 5 | CLI (`--agent` selects; `--disable-tool` narrows globally) | after resolution |

### Rule A — markdown vs markdown: whole-file replacement, closest scope wins

A project `researcher.md` fully replaces the user-level one. No field-level merge across two files of the same medium — cross-file field merge is the complexity every tool that tried it regrets. **Duplicate `name:` in the same directory is a hard error** (the reference implementation leaves this undefined; we do not).

### Rule B — TOML over markdown: field-presence narrowing merge

- **Scalars** (`description`, `system_prompt`, `model`): TOML present ⇒ TOML wins.
- **`inherits`**: **resolved in the markdown source only; NOT overridable from TOML.** Otherwise TOML widens without touching a list field: a markdown role `inherits: readonly_base` with no `tools:` key, plus a TOML role setting `inherits = "engineer"`, yields `engineer`'s pool with intersection/union never firing.
- **`max_turns`**: **min wins.** TOML may lower it, never raise it.
- **`tools`** (allowlist): **intersection**. TOML can never add a tool the markdown role lacks.
- **`disallowed_tools`**: **union**.
- **`skills`**: **intersection**.

This is the smallest rule satisfying "TOML must be able to tighten a markdown role." Free-form override could *widen*, turning `mivia.toml` — which is agent-writable, permanently — `04` §5 **rejected** hardening it (see `05` §9, which already states this correctly) — into an escalation vector over a reviewed markdown role. Intersect-on-allow / union-on-deny is (a) one sentence, (b) monotonic in the safe direction, (c) order-independent.

A TOML role with no markdown counterpart merges against the inherited base, so pure-TOML authoring is unaffected.

**Not supported:** TOML widening a markdown role's tools. The error must say exactly that, and say to edit the markdown file.

## 4. Schema

Only fields with a real enforcement point ship.

| Field | Type | Enforcement point |
|---|---|---|
| `name` | string, required | dispatcher registration key (`Kind=Subagent`); `--agent` lookup |
| `description` | string, required | injected at **runtime** into `dispatch_tasks`/`spawn_agent` `Description()` as the routing hint. **Requires the rule-60 amendment** — this plan is the first to inject workspace text into a compiled tool description, so the `chore(ai)` amendment ships here, not in `07`. |
| *(body)* | markdown | the role's system prompt |
| `inherits` | string, default `"default"` | resolved in `roles.Resolve`; cycle = load error |
| `tools` | []string | `ScopedRegistry` → `Loop.Tools` → **P1 gate** |
| `disallowed_tools` | []string | same, applied before `tools` |
| `skills` | []string | invocation allowlist (`06`) |
| `model` | string | `MultiStepHandler.Model` — real today, per-role handlers are separate instances. **Provider is not settable**; an invalid model fails at request time, not load time |
| `max_turns` | int | `MultiStepHandler.MaxSteps` |

**Reserved** (accepted, warned, ignored — TOML shape already published): `provider`, `permission_mode`, `run_program_allowlist`.

**Omitted entirely:** `can_spawn`, `max_depth`, `inherits_pool`, `mcpServers`, `hooks`, `memory`, `background`, `effort`, `isolation`, `color`, `initialPrompt`. See `00` §1 for why the first three are vacuous rather than merely unimplemented.

### Tool inheritance — one rule for both media

- `tools` **omitted** ⇒ inherit the resolved pool.
- `tools: []` **explicit empty** ⇒ zero tools ⇒ load error under `fail_on_empty_toolset`.
- Guardrail `require_explicit_tools = true` flips the workspace to deny-by-default. **Default `false`.** Under this flag, nil-vs-`[]` must be defined **per source**: a markdown role omitting `tools` resolves to ∅, and TOML supplying `tools` then intersects against ∅ (still ∅) — it must **not** be implemented as `if toml.Tools != nil && md.Tools == nil { use toml }`, which widens from ∅. Pin with `TestRequireExplicitTools_TOMLCannotWidenFromEmpty`.

> **This reverses the predecessor plan's global deny-by-default.** Two reasons. (1) With two media, "omitted means inherit" in markdown and "omitted means nothing" in TOML would be the most confusing thing in the design. (2) The predecessor justified deny-by-default as adopting Claude Code's model; [the actual schema](https://code.claude.com/docs/en/sub-agents) is inherit-by-default (`tools` omitted ⇒ inherits all). The stance was a mivia invention presented as industry-validated, and its `inherits_pool` escape hatch was ticked by the one role (`engineer`) most users define — buying friction, not security. The mandatory denylist remains non-overridable either way, and `require_explicit_tools` preserves the strict posture for shops that want it. **Call the reversal out in the changelog.**

### YAML

```yaml
---
name: researcher
description: Use for codebase exploration, locating code, answering "where is X".
inherits: default
tools: [read_file, grep, glob, list_dir]
disallowed_tools: [run_command]
skills: [bug-audit]
model: glm-4.5-air
max_turns: 12
---

You are a read-only research subagent. Search, read, summarize. Never edit.
```

### TOML (isomorphic)

```toml
[agents]
load_workspace_roles = true          # gate for .mivia/agents/*.md (default false)

[agents.guardrails]
mandatory_tool_denylist = ["delegate", "dispatch_tasks", "spawn_agent",
                           "inspect_agents", "join_run", "cancel_run"]
fail_on_empty_toolset  = true
require_explicit_tools = false

[[agents.roles]]
name             = "researcher"
description      = "Use for codebase exploration, locating code."
inherits         = "default"
tools            = ["read_file", "grep", "glob", "list_dir"]
disallowed_tools = ["run_command"]
skills           = ["bug-audit"]
model            = "glm-4.5-air"
max_turns        = 12
system_prompt    = """
You are a read-only research subagent. Search, read, summarize. Never edit.
"""
```

Identical key names, types, and defaults. The one non-1:1 mapping is `system_prompt` ↔ markdown body. Asserted by `TestRoleSchema_TOMLMarkdownIsomorphic`.

> **`mandatory_tool_denylist` is a compiled constant; config may only ADD to it.** The earlier draft proposed keeping it user-editable, mitigated by making `mivia.toml` tool-unwritable. That mitigation does not hold:
> 1. **`run_command` bypasses path guards entirely.** The file tools consult `isSecretPath` and `run_command` screens argv, but a shell invocation that builds the path at runtime slips past both — and the recommended `run_allowlist` in `.mivia/mivia.toml.example` includes `sh`, `bash`, `python`, `tee`, `sed`, `cp`, `mv`, `rm`. Any role holding `run_command` writes `mivia.toml` via `bash -c`. `09` §2.2 states this correctly, which made the earlier draft self-contradictory.
> 2. **`isSecretPath` does `strings.Contains`, not glob matching** (`tools.go:328`), so a `.mivia/**` pattern matches nothing — and a bare `mivia.toml` pattern also blocks `mivia.toml.example`, which `09` §4 requires the agent to edit.
> 3. **The guard is gone.** `04` §5 deleted the compiled-in secret pattern list outright and made config agent-editable by design, so there is no path-filter mitigation left to lean on. This makes the conclusion below stronger, not weaker.
>
> Same circularity as §5. Do not ship a lowerable floor.

## 5. Workspace roles are gated off by default — and the gate must live outside the workspace

Unlike skill bodies — which `loader.go:74-75` deliberately wraps as untrusted content under a fixed preamble — a role body **is** the system prompt, unwrapped. A cloned repo shipping `.mivia/agents/*.md` would otherwise get a real system message for free.

> **The gate cannot live in `mivia.toml`.** `DefaultConfigCandidates()` (`internal/config/paths.go:29-40`) resolves `$MIVIA_CONFIG`, then **`<cwd>/.mivia/mivia.toml`**, then `~/.config/mivia/config.toml`, and `loadFile` takes `FirstExisting` (`config/load.go:138-141`) — so a workspace `mivia.toml` **shadows user config entirely**. A hostile repo would ship `mivia.toml` containing `load_workspace_roles = true` and authorize itself. The gate would gate nothing.
>
> This is the same reasoning `04` §5 applies to the namespace directory: *a floor the agent can lower is not a floor.* An earlier draft of this section contradicted `04` and was wrong.

**Gate location:** user-level config (`~/.config/mivia/config.toml`), a CLI flag, or an env var — never the workspace file. Default off.

**The gate must also cover workspace-`mivia.toml` `[[agents.roles]]`, not only markdown.** Layer A loads TOML roles unconditionally, and a TOML role carries a `system_prompt` — so a hostile repo does not even need the markdown path. Either gate both sources identically, or state plainly that a workspace TOML role is an ungated system message.

User-level `~/.config/mivia/agents/` loads unconditionally.

> **Pre-existing, strictly worse, and unfixed:** `.mivia/agent-prompt.md` is already an ungated, unwrapped **root** system prompt read verbatim from workspace content (`prompt.go:77`, `:160-175` → `chat_command.go:44-52`). Gating roles while leaving that open is theatre. `04` must gate it behind the same switch — see `04` §5.

## 6. Parser (P2)

`internal/skills/loader.go:90-122` is a hand-rolled line scanner, not YAML: splits on the first `:`, strips quotes, recognizes exactly `name` and `description`, and has no notion of lists, nesting, comments, or multi-line scalars.

**New parser in `internal/skills/frontmatter.go`. No YAML dependency** (rule 30). Documented strict subset:

- `---` delimited frontmatter, first line only.
- `key: scalar`, optional surrounding quotes.
- `key: [a, b, c]` flow sequence.
- `key:` + indented `- item` block sequence.
- `#` comments and blank lines skipped.
- Anything else (nested maps, `>`/`|` block scalars, anchors, multi-doc) ⇒ **hard error with the line number**. Reject rather than guess.
- 256 KiB cap, mirroring `maxSkillBytes`.

**Then use it:** `skills.parseMarkdown` calls the same subset parser via `ParseFrontmatter`, so `Definition.Tools` and `triggers` stop being silently dropped. Do not maintain two frontmatter parsers.

## 7. Validation layers

`mivia.toml` is parsed in `config.Load` (`chat_command.go:34`) **before** the workspace root is resolved (`:59-62`), so markdown roles cannot be read at config-parse time. With P3 applied:

```
config.Load                    → Layer A
configureChatWorkspace         → tools.NewDefaultRegistry
skills.LoadMarkdown (hoisted)  → skill registry available
roles.LoadAndResolve           → Layer B   ← single validation point
tools.ScopedRegistry(...)      → only when --agent is set
attachSessionDispatcher        → Layer C
```

| Layer | Where | Validates | Error |
|---|---|---|---|
| **A** | `config.Load` / `internal/config/agents.go` | TOML types; duplicate `name` within `[[agents.roles]]`; name charset; guardrail types; reserved-field warnings | `parse config <path>: agents.roles[2]: duplicate role "reviewer"` — fatal |
| **B** | `internal/cli/agent_roles.go` | frontmatter syntax and unknown keys; duplicate `name` in one dir; Rule B merge; `inherits` cycle/unknown; **every tool name is a real `Tool.Name()`**; empty-toolset refusal naming *both* contributing sources; every `skills` entry is a real skill; role vs skill vs built-in handler collision; `--agent <name>` resolves | `role "researcher" (.mivia/agents/researcher.md): unknown tool "readfile"` — fatal |
| **C** | `attachSessionDispatcher` | one `MultiStepHandler` per role under `Kind=Subagent`; duplicate-handler backstop (unreachable after B — if it fires, that is a bug in B, and the message says so) | fatal |

Two consequences:

- **`--agent <name>` cannot be validated at flag-parse time** (`chat_command.go:17-30`). Validate at B; the error lists available roles.
- **Non-chat entry points get no roles.** Anything building a dispatcher outside `runChat` must call the same Layer-B helper or explicitly opt out. Make `roles.LoadAndResolve` the only constructor, taking the tool registry and skill registry as **required** arguments, so this cannot be forgotten.

## 8. File layout

Policy: soft 500 / hard 800 per file, soft 80 / hard 120 per func. No touched file is near a limit (`tools.go` 377, `orchestrate.go` 393 — the predecessor plan's "must not grow" premise was based on stale counts).

| File | Est. | Contents |
|---|---:|---|
| `internal/roles/role.go` | ~90 | `Role`, `Guardrails`, `Spec`, `ResolvedRole`, `Registry` |
| `internal/roles/markdown.go` | ~150 | `ParseFrontmatter`, `LoadDir` |
| `internal/roles/merge.go` | ~90 | precedence; Rule A; Rule B; per-field origin tracking |
| `internal/roles/resolve.go` | ~170 | `Resolve` + sub-funcs, each < 60 LOC |
| `internal/roles/validate.go` | ~70 | `ValidateAgainstRegistry` |
| `internal/tools/scope.go` | ~40 | `ScopedRegistry` — exposure filter; replaces `restrictedRegistry()` |
| `internal/config/agents.go` | ~60 | TOML types (keeps `types.go` at 152) |
| `internal/cli/agent_roles.go` | ~130 | Layer B; `--agent`; per-role handler registration; shadowing warnings |

Modified: `loop_tools.go` +6 (P1) · `skills/loader.go` +15 (P2) · `chat_command.go` 105→~125 · `chat_repl.go` 183→~180 · `cli/dispatcher.go` 209→~250 · `dispatch.go` +6 · `orchestrate.go` +6 · `multi_step.go` −14 · `config/types.go` +1.

`internal/cli/chat_repl.go` is grandfathered at `maxLines: 433` in `.mivia/policy/go-structure.json` (currently 183) — `check_go_structure.py` hard-fails growth past it.

## 9. Hybrid-specific failure modes

None of these exist in a single-source design. Each mitigation is required, not optional.

| ID | Failure mode | Mitigation |
|---|---|---|
| H1 | A cloned repo's `.mivia/agents/*.md` body becomes a real system message (skill bodies are wrapped as untrusted; role bodies are not) | `load_workspace_roles = false` default (§5) |
| H2 | TOML ∩ markdown ⇒ ∅ tools from two individually-valid sources | error naming **both** source paths, gated by `fail_on_empty_toolset` like every other empty-toolset case — it is not a separate always-fatal rule |
| H3 | Invisible shadowing — editing markdown does nothing because TOML wins | stderr warning listing shadowed fields; `mivia agents list --explain` shows per-field origin (`08`) |
| H4 | Key-name skew (`disallowedTools` vs `disallowed_tools`); markdown errors on unknown keys while `go-toml/v2` silently ignores them | camelCase aliases in YAML; documented asymmetry; unknown-key error in markdown (typos are likelier there) |
| H5 | Name collisions across four namespaces | lowercase-normalize; same-dir duplicate = error; cross-namespace = error at Layer B |
| H6 | Split-brain review: role reviewed in `.mivia/agents/` but effective tools decided by `mivia.toml` | Rule B is narrowing-only **for `tools`/`disallowed_tools`/`skills`/`max_turns`, and `inherits` is not TOML-overridable** — with those constraints the reviewed file is an upper bound. Without them it is not: see §3. |
| H7 | `--agent` unvalidatable at flag parse | validate at Layer B; error lists roles |
| H8 | `coordinator.requestFingerprint` (`coordinator/spawn.go:82-89`) hashes `Task.Name`; renaming a role or file breaks `ResumeInterruptedRun` (`recovery.go:96-100`) | operational note in docs; do not rename roles with runs in flight. Revisit in `07` |
| H9 | Roles load at Layer B but config at Layer A ⇒ `mivia doctor` sees TOML roles and no markdown roles | `roles.LoadAndResolve` is the single constructor; `doctor` calls it or prints "workspace roles not loaded" (`08`) |

## 10. Acceptance tests

1. `TestRoleSchema_TOMLMarkdownIsomorphic`
2. `TestMerge_TOMLNarrowsMarkdown` — intersects; asserts the widening error
3. `TestMerge_EmptyIntersectionErrors` — H2; error names both paths
4. `TestLoad_ProjectShadowsUser` — Rule A
5. `TestLoad_DuplicateNameSameDir`
6. `TestFrontmatter_BlockAndFlowLists` — both forms; nested map errors with a line number
7. `TestFrontmatter_UnknownKeyErrors` — H4
8. `TestValidateAgainstRegistry_UnknownToolName` — `readfile` vs `read_file`
9. `TestScopedRegistry`
10. `TestWorkspaceRolesGate` — `.mivia/agents/` ignored when `load_workspace_roles` is unset
11. `TestResolve_InheritanceCycle`
12. `TestResolve_EmptyToolsetRefused`
13. `TestResolve_EvalOrder` — mandatory → deny → allow; an allowlist entry equal to a mandatory-denylist entry is denied
14. `TestRoleScopedAgentCannotWriteFile` — **integration**: a role-scoped loop emits `write_file`, assert refusal *and* that the file is not on disk. Table-driven per rule 20.

### Mutation proofs

| # | Mutation | Test that MUST fail |
|---|---|---|
| M1 | Rule B unions `tools` instead of intersecting | `TestMerge_TOMLNarrowsMarkdown` |
| M2 | Apply allowlist before mandatory denylist | `TestResolve_EvalOrder` |
| M3 | Default `load_workspace_roles` to `true` | `TestWorkspaceRolesGate` |
| M4 | Skip `ValidateAgainstRegistry` | `TestValidateAgainstRegistry_UnknownToolName` |
| M5 | Treat `tools: []` as "inherit" | `TestResolve_EmptyToolsetRefused` |

### Invariant

```
| INV-AG-8 | Safety | Role configuration may only narrow the effective tool NAME SET: TOML intersects a markdown role's allowlist, unions its denylist, and cannot override `inherits` | `TestMerge_TOMLNarrowsMarkdown`, `TestResolve_EvalOrder`, `TestMerge_InheritsNotOverridable` | |
```

> Deliberately says **name set**, not *privilege*. `09` §2.2 establishes that tool-name inclusion is not a privilege ordering — `{run_command}` ⊄ `{read_file, write_file, grep}` yet is strictly more powerful. An invariant claiming to narrow *privilege* would be false. The privilege caveat belongs in `docs/security/overview.md` (`09` §1).

**`make invariants` will not run these tests.** `Makefile:129-133` is a hardcoded `-run` alternation with no `TestResolve`/`TestMerge`/`TestLoad`/`TestFrontmatter`/`TestScopedRegistry`/`TestRole` alternative, and `validate_invariants.py` only checks that a named test *exists*. **Add `Makefile` to §8's modified-files list.**

## 11. Known gap

**`--disable-tool` interaction.** `--disable-tool run_command` (or `[tools].disable_tools`) removes the tool from the registry (`default_registry.go:109-113`). A role allowlisting `run_command` would then fail `ValidateAgainstRegistry` and **the CLI refuses to start** — making `disable_tools` and roles mutually exclusive.

Allowlists must **intersect** with the live registry; only a name matching no tool in *any* configuration is a typo. Test: `TestRoleAllowlistIntersectsDisabledTools`.

**Rollback criterion:** if the hybrid's failure modes (H2/H3 in particular) prove confusing in practice, collapse to markdown-only with TOML holding just `[agents.guardrails]` and `load_workspace_roles`. Rule B is the piece to drop first — it is the sole source of H2, H3, and H6.
