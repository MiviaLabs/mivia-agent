# 05 — Role model core: hybrid TOML + markdown

**Status:** Design-ready. Challenge round 2 dispositioned 2026-07-31 (4 independent agents + direct verification); §3.1, §4, §5, §6, §7, §8, §9 materially rewritten. Gate scope decided (§5).
**Date:** 2026-07-29 · revised 2026-07-31
**Commits:** `feat(agent): add declarative agent roles`, `feat(cli): resolve roles from TOML and workspace files`
**Depends on:** `01` (enforcement) and `04` (namespace) — **both shipped 2026-07-29, so this is unblocked.** **Blocks:** `06`, `07`.
**Blast radius:** HIGH (privilege surface).

> **Anchors re-derived at HEAD `d88fe46` (2026-07-31).** They will drift again — per `00`'s standing note, grep the symbol, not the number. Every `file:line` below was verified on that tree; the previous revision's anchors were off by 1–35 lines and several pointed at unrelated code.

---

## 1. Goal

Named roles — `researcher` (read-only), `engineer` (full edit), `reviewer` (read-only + audit) — each with its own system prompt and scoped tools, authored **either** in `mivia.toml` **or** as `.mivia/agents/<name>.md`, with TOML able to tighten a markdown role.

## 2. Preconditions

| # | Change | Why | Where |
|---|---|---|---|
| **P1** | ✅ **Done — shipped in `01` (2026-07-29, `f0bf99b`).** Dispatch-boundary enforcement is live: `executeToolTask` rejects any tool absent from the visible registry (`internal/agent/loop_tools.go:363-374`), `newScopedLoop` builds the child dispatcher from the restricted registry (`internal/subagents/multi_step.go:206-216`). Pinned by INV-AG-7 | plan `01` |
| **P2** | ✅ **Done — shipped in `25` (2026-07-30).** Subset parser is `internal/skills/frontmatter.go` (`ParseFrontmatter:28`, `ParseFrontmatterKnown:41`); flow **and** block lists parse, unknown keys are rejected at load, 256 KiB frontmatter cap. `parseMarkdown` already calls it (`loader.go:169`). Pinned by INV-AG-17. Import it from `internal/roles`; **do not write a second parser** | plan `25` §5 |
| **P3** | Hoist `skills.LoadMarkdown` out of `attachSessionDispatcher` into `runChat` | Skills load after the tool registry (`chat_repl.go:76` inside `attachSessionDispatcher`, called from `chat_command.go:80`), splitting role validation across two points that cannot see each other | `cli/chat_command.go`, `chat_repl.go:69-86`; 2 test call sites (`interactive_session_test.go:89,216`) |
| **P4** | Gate the `skills` field on `06` | `skills.LoadMarkdown` returns an **empty registry, not an error**, when `.mivia/skills/` is absent (`internal/skills/loader.go:26-28`). Validating `skills:` entries at Layer B today would make a user-level role brick startup in every workspace that lacks that skill. See §4 | this plan |

> **P3 carries a `--no-tools` trap.** `attachSessionDispatcher` early-returns when `sess.Tools == nil` (`chat_repl.go:70-72`), which is exactly the `--no-tools` path (`configureChatWorkspace` returns early at `chat_repl.go:34-36`). **Today `--no-tools` never loads skills at all.** An unconditional hoist makes a malformed `SKILL.md` newly fatal in pure-chat mode. The hoist must be gated on `useTools`, and roles are not resolved when tools are off.

## 3. Sources and precedence

| Rank | Source | On name collision |
|---|---|---|
| 1 | Built-in `default` role (compiled, generic per rule 60) | base |
| 2 | User markdown `~/.config/mivia/agents/<n>.md` | whole-role replacement |
| 3 | Project markdown `.mivia/agents/<n>.md` (gated, see §5) | whole-role replacement |
| 4 | TOML `[[agents.roles]]` (gated, see §5) | **narrowing merge** |
| 5 | CLI (`--agent` selects; `--disable-tool` narrows globally) | after resolution |

### 3.1 Resolution procedure — normative

Roles resolve by this procedure, in order. Every step is total; **no step may be reordered.**

1. **Collect specs.** For each name, at most one markdown spec (Rule A) and at most one TOML spec. Every field on a spec carries an explicit *present/absent* bit (§8, `Spec` uses pointers throughout).
2. **Fix the inheritance edge.** `inherits` is taken from the markdown spec whenever a markdown spec exists — *including when that spec omits it*, which means `"default"`. A TOML `inherits` is honoured **only** for a name with no markdown spec; a TOML `inherits` on a name that also has a markdown spec is a **fatal error**, not a silent ignore (H3).
3. **Resolve the base.** Recursively resolve the inherited role by this same procedure. Cycle or unknown name ⇒ fatal. Cycles may cross media.
4. **Apply inheritance** to the markdown spec (or, for a TOML-only role, to the TOML spec), producing the *pre-merge role*. For each of `tools`, `disallowed_tools`, `skills`, `max_turns`, `model`, `system_prompt`: absent ⇒ take the base's resolved value; present ⇒ take the spec's own value. **Inheritance replaces; it does not intersect.** `description` is never inherited (§4).
5. **Apply Rule B** (TOML over markdown) to the pre-merge role. Only now do intersection / union / min fire. Skipped entirely when there is no markdown spec.
6. **Apply guardrails** (§4 evaluation order), then position-dependent registry intersection (§7).

> **Steps 4 and 5 must not be swapped.** Concretely: base `readonly_base` has `tools: [read_file, grep]`; `.mivia/agents/x.md` says `inherits: readonly_base` with **no `tools:` key**; `mivia.toml` says `tools = ["read_file", "run_command"]`.
> - *Inherit-then-merge (correct):* `{read_file, grep}` ∩ `{read_file, run_command}` = **`{read_file}`**.
> - *Merge-then-inherit (wrong):* markdown `tools` is absent so ∩ is identity ⇒ `{read_file, run_command}`; the field is now present so step 4 no longer fires ⇒ **`{read_file, run_command}`**.
>
> The second order hands `run_command` to a role whose reviewed file inherits a read-only base. That falsifies H6 and violates the invariant in §10. The same divergence hits `max_turns` (TOML exceeds the base's cap) and `disallowed_tools` (the base's denials are dropped). Pin with `TestResolve_InheritBeforeMerge`.

### Rule A — markdown vs markdown: whole-file replacement, closest scope wins

A project `researcher.md` fully replaces the user-level one. No field-level merge across two files of the same medium — cross-file field merge is the complexity every tool that tried it regrets. **Duplicate `name:` in the same directory is a hard error** (the reference implementation leaves this undefined; we do not).

### Rule B — TOML over markdown: field-presence narrowing merge

Applies **only** when a markdown counterpart exists (step 5).

- **`system_prompt`, `description`, `model`**: **not TOML-overridable.** Resolved in the markdown source only — same treatment as `inherits`. A TOML key for any of them on a name that also has a markdown spec is a **fatal error**.
- **`inherits`**: resolved in the markdown source only (step 2).
- **`max_turns`**: **min wins.** TOML may lower it, never raise it.
- **`tools`** (allowlist): **intersection**, against the *inherited* pool (step 4), never against the absent value.
- **`disallowed_tools`**: **union**.
- **`skills`**: **intersection** (inert until `06`; see §4).

> **Why the scalars moved out of TOML's reach.** The previous revision made `description`/`system_prompt`/`model` freely TOML-overridable, which broke H6 outright: a hostile repo shipping `.mivia/mivia.toml` with `[[agents.roles]] name = "reviewer", system_prompt = "…"` replaces a user-reviewed role's *instructions wholesale* while every list guard holds and the invariant still reads as satisfied. `model` compounds it — `[providers.<name>].base_url` and `.api_key_env` come from the same workspace file (`config/types.go:94-100`), which shadows user config entirely (§5), so the workspace would pick the endpoint and the credential env name for a role the user reviewed.
>
> The narrowing rule remains the smallest thing satisfying "TOML must be able to tighten a markdown role." Free-form override could *widen*, turning `mivia.toml` — agent-writable, permanently, since `04` §5 rejected hardening it — into an escalation vector over a reviewed markdown role. Intersect-on-allow / union-on-deny is (a) one sentence, (b) monotonic in the safe direction, (c) order-independent.

A TOML role with no markdown counterpart is resolved by steps 1–4 and 6 only, so pure-TOML authoring is unaffected and its `inherits` **is** honoured.

**Not supported:** TOML widening a markdown role's tools, or restating its prompt. The error must say exactly that, and say to edit the markdown file.

## 4. Schema

Only fields with a real enforcement point ship.

| Field | Type | Enforcement point |
|---|---|---|
| `name` | string, required | dispatcher registration key (`Kind=Subagent`); `--agent` lookup |
| `description` | string, required, **not inherited** | injected at **runtime** into `dispatch_tasks`/`spawn_agent` `Description()` as the routing hint. **Must pass `skills.SanitizeModelFacingText` at `descriptionMaxLen`** — see below |
| *(body)* | markdown | the role's system prompt |
| `inherits` | `*string`, default `"default"` | §3.1 step 2; cycle or unknown = load error; TOML `inherits` beside a markdown spec = load error |
| `tools` | `*[]string` | `ScopedRegistry` → `Loop.Tools` → P1 gate (`loop_tools.go:363-374`) |
| `disallowed_tools` | `*[]string` | same, applied before `tools` |
| `skills` | `*[]string` | invocation allowlist — **accepted, warned, ignored until `06`** (P4) |
| `model` | `*string` | `MultiStepHandler.Model` — real today, per-role handlers are separate instances. **Provider is not settable**; an invalid model fails at request time, not load time |
| `max_turns` | `*int` | `MultiStepHandler.MaxSteps` (spawned) / chat step budget (root) |

**Omitted entirely:** `provider`, `permission_mode`, `run_program_allowlist`, `can_spawn`, `max_depth`, `inherits_pool`, `mcpServers`, `hooks`, `memory`, `background`, `effort`, `isolation`, `color`, `initialPrompt`.

> **There is no "reserved" tier.** The previous revision reserved the first three "because the TOML shape is already published." It is not: `permission_mode`, `run_program_allowlist` and the whole `[agents]` section appear **nowhere** outside `.mivia/plans/` — not in `docs/product/config.md`, not in `.mivia/mivia.toml.example` (whose sections are `provider`, `providers.*`, `chat`, `tools`, `integrations.tavily`, `subagents`, `privacy`), not in `internal/config/types.go`. `00` §3 invariant 2 therefore applies with no exception: a field with no runtime hook is omitted. See `00` §1 for why the last three are vacuous rather than merely unimplemented.

### `description` is untrusted workspace text on the root agent's tool surface

Two corrections to the previous revision:

1. **This is not the first runtime injection.** `dispatchTasksTool.Description()` already appends `"Available skill handlers: …"` from `skillReg.ListModelFacing` (`internal/cli/dispatch.go:85-93`) and into the `handler` JSON-schema `enum` (`:143-148`); `spawnAgentTool` mirrors it (`internal/cli/orchestrate.go:95-103, 167-180`). Both register on the **root** registry via `registerSessionTool`, so the text already lands in root-agent context. The rule-60 `chore(ai)` amendment covers an **existing** condition and still ships here, but the "first" claim is deleted.
2. **The existing path sanitizes and this plan did not say to.** Skill name/description run through `skills.SanitizeModelFacingText` (`internal/skills/skills.go:104-121` — strips ASCII control chars, `\` and `"`, collapses whitespace) at `nameMaxLen = 64` / `descriptionMaxLen = 200` (`loader.go:155-160`). Role `description` is *required* and injected; it MUST use the same sanitizer and caps, and the joined role list MUST be length-capped.

**Rule-60 guard to survive:** `TestSessionToolSurfaceIsProjectAndLanguageGeneric` (`internal/cli/session_tool_surface_test.go`) scans the model-facing surface for `golang`, `go.mod`, `*.go`, `sql`, `database`, `github.com/MiviaLabs`. A role described as "Go code review" fails it. The amendment must state that a *workspace-authored* description is user config and out of the compiled-surface scope, and the test must exclude runtime-injected text explicitly rather than by accident.

### Tool inheritance — one rule for both media

- `tools` **omitted** ⇒ resolves to the **inherited pool** at §3.1 step 4, *before* Rule B runs. Rule B's intersection therefore always has a concrete operand and is never identity: a markdown role that omits `tools` is bounded by its base's pool no matter what TOML says.
- `tools: []` **explicit empty** ⇒ zero tools ⇒ load error under `fail_on_empty_toolset`.
- Guardrail `require_explicit_tools = true` flips **authored** roles to deny-by-default: a markdown role omitting `tools` resolves to ∅, and TOML then intersects against ∅ (still ∅). **Default `false`.**
  - It must **not** be implemented as `if toml.Tools != nil && md.Tools == nil { use toml }` — that widens from ∅, and is the same mistake as merge-then-inherit (§3.1). Pin with `TestRequireExplicitTools_TOMLCannotWidenFromEmpty`.
  - **The flag never applies to the compiled `default` role.** A compiled role cannot be "explicit" about a registry that varies with `disable_tools` and workspace resolution, and applying it makes plain `mivia chat` unstartable (∅ pool + `fail_on_empty_toolset` ⇒ fatal) with no user-editable cause. Pin with `TestRequireExplicitTools_DefaultRoleUnaffected`.

> **This reverses the predecessor plan's global deny-by-default.** Two reasons. (1) With two media, "omitted means inherit" in markdown and "omitted means nothing" in TOML would be the most confusing thing in the design. (2) The predecessor justified deny-by-default as adopting Claude Code's model; [the actual schema](https://code.claude.com/docs/en/sub-agents) is inherit-by-default (`tools` omitted ⇒ inherits all). The stance was a mivia invention presented as industry-validated, and its `inherits_pool` escape hatch was ticked by the one role (`engineer`) most users define — buying friction, not security. **Call the reversal out in the changelog.**

### `max_turns`

`*int`. **`nil` = unset**, participating in `min` as +∞, so `min(unset, n) = n` — and that is safe only because §3.1 step 4 has already substituted the inherited value before Rule B runs. **`max_turns = 0` is a load error in both media**: `0` already means "use the built-in 100" when spawned (`multi_step.go:227-230`) and "unlimited" at root (`config/types.go:108-111`, where `Resolved.MaxSteps` is `*int` for exactly this reason), and a role field that means opposite things by position is not shippable. The compiled `default` role's `max_turns` is `SubagentConfig.NestedSteps` (`config/defaults.go:24`).

### Guardrails — and where they may be set

```toml
[agents.guardrails]
mandatory_tool_denylist = []          # ADDITIONS ONLY; the six baseline names are compiled
fail_on_empty_toolset  = true
require_explicit_tools = false
```

> **Guardrails resolve from user config only; a workspace value may tighten, never loosen.** The previous revision spent §5 proving `load_workspace_roles` cannot live in `mivia.toml` — and then left the other two guardrails exactly there. `config.Load` takes `FirstExisting(DefaultConfigCandidates())` (`config/paths.go:31-43`, `config/load.go:155`) with **no layering**, so a user who sets `require_explicit_tools = true` in `~/.config/mivia/config.toml` has it silently discarded by any repo shipping `.mivia/mivia.toml`. The strict posture would evaporate on `git clone`.
>
> **Implementation:** `roles` reads guardrails from both fixed paths explicitly — `~/.config/mivia/config.toml` and `<cwd>/.mivia/mivia.toml` — rather than from `config.Load`'s single resolved file. The user value is the floor; a workspace value is applied only when it tightens (`false`→`true` for both booleans; denylist may only add). Pin with `TestGuardrails_WorkspaceCannotLoosen`.

> **`mandatory_tool_denylist` is a compiled constant; config may only ADD to it, and it is a mirror, not the gate.**
> The real gate is the `tools.PrivilegedTool` type marker: `restrictedRegistry` admits a tool only when `!blocked[t.Name()] && !privileged` (`internal/subagents/multi_step.go:251`), backstopped by a startup assertion in `registerSessionTool` (`internal/cli/dispatcher.go:191-194`) whose own comment says the marker exists "so future control tools do not depend solely on a name denylist." A config-surfaced name list can only drift from it. The example above therefore shows an **empty additions list**, never the six baseline values — printing them invites an edit that `go-toml/v2` accepts and that "may only ADD" then silently no-ops. Pin the mirror with `TestMandatoryDenylistMatchesPrivilegedMarker`.
>
> Keeping the floor compiled is not negotiable:
> 1. **`run_command` bypasses path guards entirely.** File tools consult `isSecretPath` and `run_command` screens argv, but a shell invocation that builds the path at runtime slips past both — and the recommended `run_allowlist` in `.mivia/mivia.toml.example:53-79` includes `sh`, `bash`, `python`, `tee`, `sed`, `cp`, `mv`, `rm`. Any role holding `run_command` writes `mivia.toml` via `bash -c`.
> 2. **`write_file` needs no allowlist at all.** Nothing stops a role holding `write_file`/`search_replace` from rewriting `.mivia/agent-prompt.md`, `.mivia/mivia.toml`, or `.mivia/skills/*/SKILL.md` — next session's root system prompt.
> 3. **`isSecretPath` does `strings.Contains`, not glob matching** (`internal/tools/tools.go:325`, `:338`), so a `.mivia/**` pattern matches nothing — and a bare `mivia.toml` pattern also blocks `mivia.toml.example`, which `09` §4 requires the agent to edit.
> 4. **There is no path-filter mitigation left.** `04` §5 deleted the compiled secret-pattern list; `config/defaults.go:30-37` ships none, and INV-SEC-1 records that an unconfigured workspace filters nothing.
>
> **Scope: positional, not per-role.** The denylist filters the registry handed to a **spawned** agent. It is deliberately **not** applied to the root session's registry, whichever role the root holds — see §7, and note that `06` §2 places its entire enforcement in `dispatchTasksTool.buildTasks`/`spawnAgentTool.Execute` and would become unreachable code if the root lost those tools. Pin with `TestMandatoryDenylist_RootExempt_SpawnedFiltered`.

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

Identical key names, types, and defaults. The one non-1:1 mapping is `system_prompt` ↔ markdown body. Asserted by `TestRoleSchema_TOMLMarkdownIsomorphic`, which compares the **field sets and presence semantics** of `roles.Spec` against the TOML struct — not merely struct tags.

**The gate does not appear in this block.** `load_workspace_roles` is user-config-only (§5) and showing it under a heading that reads as `mivia.toml` is exactly the wrong thing to ship, given `09` §4 ships `.mivia/mivia.toml.example` and `09` §7 treats a wrong example as a shipped bug:

```toml
# ~/.config/mivia/config.toml — NOT the workspace file
[agents]
load_workspace_roles = false   # default; gates BOTH .mivia/agents/*.md and workspace [[agents.roles]]
```

## 5. Workspace roles are gated off by default — and the gate must live outside the workspace

**DECIDED 2026-07-31: gate roles only.** Both role media (`.mivia/agents/*.md` **and** workspace `mivia.toml` `[[agents.roles]]`) are gated off by default under one user-level switch. The other workspace-supplied prompt surfaces are **not** gated by this plan; they are pre-existing and are documented in §9 H1 and in `docs/security/overview.md`. Rationale: do not add a *new* open surface; re-litigating the existing ones is a separate plan, not a rider on this one.

> **The previous revision's justification was factually wrong and is deleted.** It claimed role bodies are uniquely unwrapped because skill bodies "are deliberately wrapped as untrusted content under a fixed preamble." The preamble exists (`internal/skills/loader.go:141-142`) but only inside `skillRunner`, which serves the `runtime.Skill` one-shot path. The path the model actually reaches is `registerSkillHandlers` (`internal/cli/dispatcher.go:135-174`), which sets `SystemPrompt: skill.Instructions` verbatim (`:150-162`) on a `MultiStepHandler` holding `FullRegistry: reg` (`:159`), registered under `Kind=Subagent` (`:168`). A cloned repo's `.mivia/skills/*/SKILL.md` is **already** an ungated, unwrapped subagent system prompt. Roles are the third instance of this class, not a new one.

> **The gate cannot live in `mivia.toml`.** `DefaultConfigCandidates()` (`internal/config/paths.go:31-43`) resolves `$MIVIA_CONFIG`, then **`<cwd>/.mivia/mivia.toml`**, then `~/.config/mivia/config.toml`, and `loadFile` takes `FirstExisting` (`config/load.go:155`) — so a workspace `mivia.toml` **shadows user config entirely**. A hostile repo would ship `mivia.toml` containing `load_workspace_roles = true` and authorize itself. The gate would gate nothing. This is the same reasoning `04` §5 applies to the namespace directory: *a floor the agent can lower is not a floor.*

**Gate location:** `~/.config/mivia/config.toml` (read at its fixed path, not via `config.Load`), a CLI flag, or an env var. Default off.

> **If the gate is an env var, it is read with `os.LookupEnv` only.** `envfile.Lookup` MUST NOT be used: `DefaultEnvCandidates()` puts **`<cwd>/.env` first** (`config/paths.go:46-55`) and `config.Load` already resolves values through it (`config/load.go:39`, `:113`), so following the house pattern would let the workspace set its own gate — the exact circularity this section exists to prevent, one helper call away. `$MIVIA_CONFIG` itself is safe: `paths.go:33` uses `os.Getenv`. Name the variable in the implementation so review can grep for it. Pin with `TestWorkspaceDotEnvCannotEnableRoles`.

User-level `~/.config/mivia/agents/` loads unconditionally.

> **Pre-existing, ungated, and deliberately out of scope here.** Four workspace-controlled paths already reach a system prompt, none of them gated:
> 1. `[chat].system_prompt` in a workspace `mivia.toml` (`config/types.go:104` → `chat_command.go:56-62`) — the **root** system prompt, and it takes precedence over `.mivia/agent-prompt.md`, which never runs when it is set.
> 2. `[subagents].system_prompt` (`config/types.go:121` → `dispatcher.go:93-95`, `:110-113`) — every subagent's system prompt, including the full-registry `multi_step` handler.
> 3. `.mivia/agent-prompt.md`, read verbatim with no wrapper (`loadAgentPrompt`, `internal/cli/prompt.go:157-173` → `chat_command.go:58`). **`04` §5 explicitly DECIDED not to gate this** (2026-07-30); the exposure is accepted and recorded in `docs/security/overview.md:49`. The previous revision of this plan said "`04` must gate it behind the same switch" — that is stale and reversed; the gate is not coming from `04`.
> 4. Workspace skill bodies (above).
>
> `09` must state all four in `docs/security/overview.md` alongside the role gate, so the gate is not read as a claim about the class.

## 6. Parser — reuse, do not rebuild

**P2 shipped this.** `internal/skills/frontmatter.go` is the strict, dependency-free subset parser (rule 30; there is no YAML module in `go.mod`): `---`-delimited first-line frontmatter, `key: scalar` with optional quotes, `key: [a, b, c]` flow sequences, `key:` + indented `- item` block sequences, `#` comments and blank lines skipped, anything else a hard error with the line number, 256 KiB cap. `internal/roles` calls `ParseFrontmatterKnown(data, knownRoleKeys)` (`frontmatter.go:41`). Nothing is re-implemented.

The previous revision's §6 described `loader.go:90-122` as "a hand-rolled line scanner recognizing exactly `name` and `description`" and scheduled rewiring `parseMarkdown`. **All of that shipped in `25`** — `parseMarkdown` calls `ParseFrontmatterKnown` at `loader.go:169`, `knownSkillKeys = {name, description, triggers}` at `:165`, and `Definition.Tools` is not populated by the loader and is not a role concern.

Two changes to the shared parser are required by roles, both pinned by new tests, both touching a file covered by INV-AG-17:

- **`key: []` must yield an empty, non-nil `[]string`.** `splitFlowSequence` returns `nil` for an empty inner string (`frontmatter.go:209-213`). The map key *is* present, so presence is recoverable via a two-value lookup — but the natural `len(list) > 0` presence check conflates `tools: []` with an omitted key, which **is mutation M5 shipping as the default implementation** (see §10). Return `[]string{}`. Pin with `TestFrontmatter_EmptyFlowSequenceIsNotNil`.
- **Bare `key:` with no flow sequence and no following indented item must be a hard error naming the line.** Today it stores the empty *string* (`frontmatter.go:151-156`), so `m["tools"].([]string)` fails and it reads as absent. A bare key is ambiguous between null, empty scalar, and empty list and the loader cannot recover intent. Pin with `TestFrontmatter_BareKeyErrors`.

**Mirror obligation:** `scripts/verify_agent_config.py:32` hard-codes `SKILL_KNOWN_KEYS` to mirror the Go set. Either add `knownRoleKeys` there too, or state in the script why roles are not mirrored.

## 7. Validation layers

`mivia.toml` is parsed in `config.Load` (`chat_command.go:46`) **before** the workspace root is resolved (`:71-74`), so markdown roles cannot be read at config-parse time. With P3 applied:

```
config.Load                    → Layer A
configureChatWorkspace         → tools.NewDefaultRegistry
skills.LoadMarkdown (hoisted, gated on useTools)
roles.LoadAndResolve           → Layer B   ← single validation point
attachSessionDispatcher        → Layer C   (registers per-role handlers)
tools.ScopedRegistry(sess.Tools, rootRole) → Layer D, only when --agent is set
```

| Layer | Where | Validates | Error |
|---|---|---|---|
| **A** | `config.Load` / `internal/config/agents.go` | TOML types; duplicate `name` within `[[agents.roles]]`; name charset; guardrail types | `parse config <path>: agents.roles[2]: duplicate role "reviewer"` — fatal |
| **B** | `internal/cli/agent_roles.go` | frontmatter syntax and unknown keys; duplicate `name` in one dir; Rule A; §3.1; Rule B; `inherits` cycle/unknown; **every tool name is in `tools.AllToolNames()`**; role vs skill vs reserved-handler collision; `--agent <name>` resolves | `role "researcher" (.mivia/agents/researcher.md): unknown tool "readfile"` — fatal |
| **C** | `attachSessionDispatcher` | one `MultiStepHandler` per role under `Kind=Subagent`; registry intersection for the *spawned* position; empty-toolset refusal naming *both* contributing sources | fatal |
| **D** | after `attachSessionDispatcher` returns | root-session scoping when `--agent` is set | fatal |

### The registry is not the same object at B, C and D

This is the correction that reorders the pipeline. Three facts:

1. **`NewSessionDispatcher` mutates the registry it is handed.** `registerSessionTool` ends with `reg.Register(tool)` (`internal/cli/dispatcher.go:201`), and it is called for `delegate`, `dispatch_tasks` (`:176-184`) and `spawn_agent`, `inspect_agents`, `join_run`, `cancel_run` (`registerOrchestrationTools`, `:83`), plus the ledger tools (`:86`). The root loop's enforced registry **is** that object (`internal/chat/session.go:282` builds `agent.Loop{… Tools: s.Tools …}`).
   ⇒ Scoping the root registry *before* `attachSessionDispatcher` is silently undone one line later. Root scoping is **Layer D**, after it returns.
2. **At Layer B the registry holds only `registerDefaultTools`' output**, and `find_references` registers only for a resolved workspace (`default_registry.go:114`).
   ⇒ Layer B must validate names against **`tools.AllToolNames()`** — a new compiled catalogue (§11) — not against the constructed registry.
3. **The mandatory denylist is positional.** At Layer D the root keeps `dispatch_tasks`/`spawn_agent` (else `06` has nothing to gate and orchestration disappears); at Layer C the spawned registry drops them *and* everything implementing `tools.PrivilegedTool`.

### `ScopedRegistry` must keep the `PrivilegedTool` marker

`restrictedRegistry` filters on **two** things — the six-name denylist **and** the type marker (`multi_step.go:251`). A name-only `ScopedRegistry(reg, allow, deny)` drops the marker check, which is pinned by `TestRestrictedRegistryExcludesPrivilegedMarker` (`internal/subagents/multi_step_scoped_test.go:102-109`), by `internal/cli/session_tool_privilege_test.go:41-56,70-86`, and by **INV-AG-7**. `ScopedRegistry` applies the marker exclusion unconditionally, and `restrictedRegistry` **is retained as a thin delegation to it**, not deleted — it is a method, and three test files call it directly (`multi_step_test.go:148,226`, `multi_step_scoped_test.go:106`). Moving the restriction to construction time would also strip the marker filter from the built-in `multi_step` handler (`dispatcher.go:121-128`) and every skill handler (`:157-167`), which are constructed with the raw `reg`.

### Two further consequences

- **`--agent <name>` cannot be validated at flag-parse time.** It does not exist yet in any form; it must be added to the flag table (`chat_command.go:29-42`) or the unknown-arg check at `:43-45` rejects it, and to `printUsage` (`internal/cli/root.go:36-66`) or the usage text lies. Validate at Layer B; the error lists available roles.
- **Non-chat entry points get no roles, and Go cannot prevent that.** The only production dispatcher construction is `chat_repl.go:80` (shared by `oneShot`, `repl` and `runTUI`). `NewSessionDispatcherWithLedger` (`dispatcher.go:49`) has **zero callers, production or test** — a second exported doorway that would silently get no roles; **delete it** rather than document it. There is no package allowlist, no import-boundary lint, and `semgrep/agent-standards.yml` holds content patterns, not dependency rules, so "`roles.LoadAndResolve` is the only constructor" is not enforceable by tooling. The enforceable version is the one the repo already uses for privileged tools: make the role registry a **required, non-variadic parameter** that does not compile if omitted. Note `NewSessionDispatcher` currently takes `skillReg ...*skills.Registry` — a second registry cannot be added variadically, so the signature changes and 7 test call sites move (`delegation_test.go:484,531,613,646,684`, `session_tool_surface_test.go:79,227`).

## 8. File layout

Policy: soft 500 / hard 800 per file, soft 80 / hard 120 per func (`.mivia/policy/go-structure.json`).

| File | Est. | Contents |
|---|---:|---|
| `internal/roles/role.go` | ~110 | `Spec`, `ResolvedRole`, `Guardrails`, `Origin`, `Registry` |
| `internal/roles/markdown.go` | ~150 | `LoadDir` (lstat + size cap), frontmatter → `Spec` |
| `internal/roles/merge.go` | ~110 | Rule A; Rule B; per-field origin tracking |
| `internal/roles/resolve.go` | ~180 | §3.1 procedure + sub-funcs, each < 60 LOC |
| `internal/roles/validate.go` | ~80 | `ValidateAgainstCatalogue`, `IntersectWithRegistry` |
| `internal/tools/scope.go` | ~50 | `ScopedRegistry` — exposure filter incl. `PrivilegedTool` |
| `internal/tools/names.go` | ~40 | `AllToolNames()` compiled catalogue (§11) |
| `internal/subagents/names.go` | ~15 | `ReservedHandlerNames = {"delegate","oneshot","multi_step"}` (§9 H5) |
| `internal/config/agents.go` | ~70 | TOML types (keeps `types.go` off the growth path) |
| `internal/cli/agent_roles.go` | ~150 | Layer B; `--agent`; per-role handler registration; Layer D; shadowing warnings |

### Types

```go
// Spec: one authored source, pre-merge. Presence-preserving — every optional
// field is a pointer, and presence is never inferred from len().
type Spec struct {
    Name            string
    Description     *string
    SystemPrompt    *string   // markdown body, or TOML system_prompt
    Model           *string
    Inherits        *string
    MaxTurns        *int
    Tools           *[]string
    DisallowedTools *[]string
    Skills          *[]string
    Origin          Origin    // path + medium, per field, for H3 and 08's --explain
}

// ResolvedRole: post-inherit, post-merge, post-guardrail.
type ResolvedRole struct {
    Name, Description, SystemPrompt, Model string
    MaxTurns       int
    EffectiveTools []string          // sorted, deduped, position-dependent
    Skills         *[]string         // nil still means "all" — 06 requires it
    FieldOrigin    map[string]Origin
}
```

`Role` is dropped from the previous revision's list — it was a third name for one of these two shapes, and the `Spec`/`ResolvedRole` split is what makes Rule B expressible. `roles.Registry` is the third `Registry` in the tree (`tools.Registry`, `skills.Registry`); always qualify it.

**Presence notes.** `go-toml/v2 v2.2.3` distinguishes a missing `[]string` key (`nil`) from `= []` (non-nil, len 0) — verified empirically — so slices need no pointer on the TOML side, but `Spec` uses one anyway for medium-symmetry. It does **not** distinguish a missing string key from `= ""`, so `SystemPrompt`/`Description`/`Model`/`Inherits` must be `*string` in the TOML struct; an explicit `system_prompt = ""` is a load error, not a silent "keep the markdown body".

### Modified

`internal/agent/loop_tools.go` +0 (P1 already shipped) · `internal/skills/frontmatter.go` +10 (§6) · `internal/skills/frontmatter_test.go` +2 tests · `internal/cli/chat_command.go` 114→~140 · `internal/cli/chat_repl.go` 172→~180 · `internal/cli/dispatcher.go` 237→~275 (signature change; delete `NewSessionDispatcherWithLedger`) · `internal/cli/root.go` (`--agent` in `printUsage`) · `internal/cli/orchestrate.go` +6 · `internal/cli/dispatch.go` +6 · `internal/subagents/multi_step.go` ±0 (`restrictedRegistry` delegates to `ScopedRegistry`) · `internal/config/types.go` +1 · **`Makefile`** (§10 — mandatory, not optional) · **`.mivia/invariants.md`** (new row) · `scripts/verify_agent_config.py` (§6 mirror) · test call sites: `interactive_session_test.go:89,216`, `delegation_test.go:484,531,613,646,684`, `session_tool_surface_test.go:79,227`, `multi_step_test.go:148,226`, `multi_step_scoped_test.go:106`.

### Structure-check headroom — the real numbers

The previous revision's "no touched file is near a limit" rested on stale counts. Actual at HEAD:

| File | Plan said | Actual | Headroom |
|---|---:|---:|---|
| `internal/cli/orchestrate.go` | 393 | **475** | **19 lines to the soft-500 warn** |
| `internal/cli/dispatcher.go` | 209 | **237** | 263 |
| `internal/config/types.go` | 152 | **176** | 324 |
| `internal/cli/chat_command.go` | 105 | **114** | 386 |
| `internal/cli/chat_repl.go` | 183 | **172** | 261 (grandfathered `maxLines: 433`) |
| `internal/tools/tools.go` | 377 | 374 | 126 |
| `internal/subagents/multi_step.go` | "`multi_step.go −14`" | **259** — and the path was wrong; there is no `internal/agent/multi_step.go` | 241 |

`make verify` runs `check_go_structure.py --strict --all` (`Makefile:64`), and `--strict` promotes the soft-500 **warning to a hard failure** (`check_go_structure.py:239-241`). `orchestrate.go` +6 ⇒ 481 leaves 19 lines, not 100. `chat_repl.go`'s baseline hard-fails on any growth past 433 (`check_go_structure.py:179-186`). No other touched file is baselined, so all are subject to the hard 800.

## 9. Hybrid-specific failure modes

| ID | Failure mode | Mitigation |
|---|---|---|
| H1 | A cloned repo's `.mivia/agents/*.md` body or workspace `[[agents.roles]]` `system_prompt` becomes a real system message | `load_workspace_roles = false` default, covering **both** media, gate in user config only (§5). Does **not** close the four pre-existing surfaces — those are documented, not fixed |
| H2 | TOML ∩ markdown ⇒ ∅ tools from two individually-valid sources | error naming **both** source paths, gated by `fail_on_empty_toolset` like every other empty-toolset case — it is not a separate always-fatal rule |
| H3 | Invisible shadowing — editing markdown does nothing because TOML wins | scalars are now a **hard error** rather than a silent override (Rule B); list narrowing emits a stderr warning listing shadowed fields; `mivia agents list --explain` shows per-field origin (`08`) |
| H4 | Key-name skew (`disallowedTools` vs `disallowed_tools`); markdown errors on unknown keys while `go-toml/v2` silently ignores them | camelCase aliases in YAML; documented asymmetry; unknown-key error in markdown (typos are likelier there) |
| H5 | Name collisions across four namespaces | lowercase-normalize; same-dir duplicate = error. Cross-namespace: a role colliding with a **skill** name or a **reserved handler** name is a Layer-B error naming both sources. Reserved names are exported as `subagents.ReservedHandlerNames` (§8), which `internal/roles` may import without a cycle, and `dispatcher.go:100,103,129` must use it so the list cannot drift — today they are bare string literals in `internal/cli`, which `internal/roles` cannot see. A role colliding with a **tool** name is an error **by policy, not by necessity**: `Kind=Tool` and `Kind=Subagent` are separate maps (`runtime/dispatcher.go:108`) so nothing breaks at runtime, but `dispatch_tasks`' `handler` field is a free-form string and an ambiguous name routes unpredictably. `--agent multi_step` resolves to `default` with a deprecation note; `multi_step` may not be used as a role name |
| H6 | Split-brain review: role reviewed in `.mivia/agents/` but effective behaviour decided by `mivia.toml` | Rule B is narrowing-only for `tools`/`disallowed_tools`/`skills`/`max_turns`, and `inherits`/`system_prompt`/`description`/`model` are not TOML-overridable at all. With those constraints the reviewed file is an upper bound **on the tool name set** — see §10's invariant note; it is not an upper bound on privilege |
| H7 | `--agent` unvalidatable at flag parse | validate at Layer B; error lists roles. Also add the flag to `flagValue` and to `printUsage` (§7) |
| H8 | Renaming a role breaks resume | The mechanism is **not** the fingerprint: `ResumeInterruptedRun` passes an empty one (`internal/coordinator/recovery.go:126`). The handler name is **persisted** as `TaskSnapshot.HandlerName` (`coordinator/spawn.go:198`) and re-dispatched verbatim (`recovery.go:383-397`), failing at `runtime/dispatcher.go:228-230` with `unknown subagent %q`. **This is pre-existing for skills.** Separately and genuinely fingerprint-related: renaming a role changes `fingerprintTask.Name` (`spawn.go:113-128`), so a repeat `spawn_agent` with the same idempotency key returns `ErrIdempotencyConflict`. Operational note in docs for both; do not rename roles with runs in flight. Revisit in `07` |
| H9 | Roles load at Layer B but config at Layer A ⇒ `mivia doctor` sees TOML roles and no markdown roles | `roles.LoadAndResolve` is the single constructor; `doctor` prints "workspace roles not loaded" until `08` wires it. `doctor.go`/`config_cmd.go` are **not** modified by this plan |
| **H10** | **Role scoping bounds a role, not a session** | `multi_step` (`dispatcher.go:121-128`) and every workspace skill handler (`:157-167`) remain **full-registry** and remain selectable by a model-supplied free-form `handler` string (`dispatch.go:255-258`), and the compiled root prompt tells the model to prefer `multi_step` for file access. The program's stated #1 risk is prompt injection of the **root** agent (`00` §2); an injected root does not select `researcher`, it selects `multi_step`. Roles are a bound on work the model chooses to route through them. `09` must state this in `docs/security/overview.md`. Removing or gating the unscoped default handler is **new work this plan does not budget** — it belongs to `07` |

## 10. Acceptance tests

1. `TestRoleSchema_TOMLMarkdownIsomorphic` — compares field sets **and presence semantics**, not struct tags
2. `TestMerge_TOMLNarrowsMarkdown` — intersects; asserts the widening error
3. `TestMerge_EmptyIntersection_GatedByGuardrail` — H2; table-driven over both `fail_on_empty_toolset` values; error names both paths
4. `TestLoad_ProjectShadowsUser` — Rule A
5. `TestLoad_DuplicateNameSameDir`
6. `TestFrontmatter_BlockAndFlowLists` — both forms; nested map errors with a line number
7. `TestFrontmatter_UnknownKeyErrors` — H4
8. `TestValidateAgainstCatalogue_UnknownToolName` — `readfile` vs `read_file`; **stays fatal even when the tool is also disabled**
9. `TestScopedRegistry` — including `PrivilegedTool` exclusion
10. `TestWorkspaceRolesGate` — `.mivia/agents/` ignored when the gate is unset
11. `TestResolve_InheritanceCycle`
12. `TestResolve_EmptyToolsetRefused` — asserted on the **markdown** path, not only TOML
13. `TestResolve_EvalOrder` — builds a registry *containing* a mandatory-denylist tool and asserts the allowlist entry is still denied, with reason `"mandatory denylist"` rather than `"unknown tool"`, so M2 cannot pass by accident
14. `TestRoleScopedAgentCannotWriteFile` — **integration**: a role-scoped loop emits `write_file`, assert refusal *and* that the file is not on disk. Table-driven per rule 20
15. `TestMerge_DisallowedToolsUnion`
16. `TestMerge_MaxTurnsMinWins` — unset×set, set×set both directions, inherited
17. `TestMerge_InheritsNotOverridable` — both the shadowed-error and TOML-only-honoured cases
18. `TestWorkspaceRolesGate_TOMLRoles` — the gate covers `[[agents.roles]]`, not only markdown
19. `TestRoleAllowlistIntersectsDisabledTools` — §11
20. `TestResolve_InheritBeforeMerge` — §3.1
21. `TestMerge_ScalarsNotOverridableWhenMarkdownExists`
22. `TestRoleDescriptionSanitized` — control chars, quotes, over-length, injection string
23. `TestGuardrails_WorkspaceCannotLoosen`
24. `TestMandatoryDenylist_RootExempt_SpawnedFiltered`
25. `TestMandatoryDenylistMatchesPrivilegedMarker`
26. `TestRequireExplicitTools_TOMLCannotWidenFromEmpty`
27. `TestRequireExplicitTools_DefaultRoleUnaffected`
28. `TestLoadDir_RejectsSymlink` — §11
29. `TestLoadDir_RejectsOversizedBody` — §11
30. `TestRoleSpec_NilVsEmptyPerMedium` — `tools: []`, bare `tools:` (error), omitted, and the three TOML equivalents produce distinct `Spec` values
31. `TestFrontmatter_EmptyFlowSequenceIsNotNil`, `TestFrontmatter_BareKeyErrors` — §6
32. `TestAllToolNamesMatchesFullRegistry` — §11
33. `TestWorkspaceDotEnvCannotEnableRoles` — §5

### Mutation proofs

| # | Mutation | Test that MUST fail |
|---|---|---|
| M1 | Rule B unions `tools` instead of intersecting | `TestMerge_TOMLNarrowsMarkdown` |
| M2 | Apply allowlist before mandatory denylist | `TestResolve_EvalOrder` |
| M3 | Default the gate to `true` | `TestWorkspaceRolesGate` |
| M4 | Skip catalogue validation | `TestValidateAgainstCatalogue_UnknownToolName` |
| M5 | Treat `tools: []` as "inherit" | `TestResolve_EmptyToolsetRefused` (markdown path) |
| M6 | TOML `disallowed_tools` replaces instead of unions | `TestMerge_DisallowedToolsUnion` |
| M7 | `max_turns`: TOML wins instead of min | `TestMerge_MaxTurnsMinWins` |
| M8 | Gate applies to markdown only | `TestWorkspaceRolesGate_TOMLRoles` |
| M9 | Merge-then-inherit | `TestResolve_InheritBeforeMerge` |
| M10 | TOML `system_prompt` overrides a markdown body | `TestMerge_ScalarsNotOverridableWhenMarkdownExists` |
| M11 | `ScopedRegistry` filters on names only | `TestScopedRegistry` |
| M12 | Skip `description` sanitization | `TestRoleDescriptionSanitized` |

### Invariant

```
| INV-AG-25 | Safety | Role configuration may only narrow the effective tool NAME SET: TOML intersects a markdown role's allowlist against the inherited pool, unions its denylist, takes the min of max_turns, and cannot override inherits, system_prompt, description or model | `TestMerge_TOMLNarrowsMarkdown`, `TestMerge_DisallowedToolsUnion`, `TestMerge_MaxTurnsMinWins`, `TestMerge_InheritsNotOverridable`, `TestMerge_ScalarsNotOverridableWhenMarkdownExists`, `TestResolve_InheritBeforeMerge`, `TestResolve_EvalOrder` | |
```

> **The ID is `INV-AG-25`, not `INV-AG-8`.** `INV-AG-8` has been taken since 2026-07-30 by the message-loss invariant (`.mivia/invariants.md:58`), and IDs run contiguously through `INV-AG-24` (`:74`). `validate_invariants.py` hard-fails on duplicate IDs (`:47-86`), so the previous revision's ID would have broken `make validate-invariants` at commit time. **`.mivia/INDEX.md:105` is also stale** — it says "`INV-AG-8` is a permanent gap; 12 through 17 are taken"; both halves are wrong at HEAD. Fix that line in the same commit.

> **Deliberately says name set, not privilege, and role not session.** `09` §2.2 establishes that tool-name inclusion is not a privilege ordering — `{run_command} ⊄ {read_file, write_file, grep}` yet is strictly more powerful. And per H10, the invariant bounds a *role*, not a session: the unscoped `multi_step` handler stays selectable. Both caveats belong in `docs/security/overview.md` (`09` §1).

**`make invariants` will not run these tests, and this is a hard gate, not a nicety.** `Makefile:132` is a single hardcoded `-run` alternation with no `TestResolve`/`TestMerge`/`TestFrontmatter`/`TestScopedRegistry`/`TestRole`/`TestValidateAgainstCatalogue` alternative — and `TestLoadMarkdown`, the only `TestLoad` match, does not match `TestLoad_ProjectShadowsUser` under Go's unanchored `-run` regex. `validate_invariants.py` does three checks, not one: duplicate IDs (`:47-86`), test existence (`:98-108`), **and that every referenced test is selected by the Makefile regex** (`:110-121`, exit 1 with "invariant test(s) are not selected by Makefile invariants regex"). The `Makefile` edit is therefore **required for the commit to pass**, and it is in §8's modified list.

## 11. Known gaps and required additions

**`--disable-tool` interaction, and how a typo differs from a disabled tool.** `--disable-tool run_command` (or `[tools].disable_tools`) removes the tool at registry construction (`disabledToolNames` + the `register` closure, `default_registry.go:87-100`). A role allowlisting `run_command` would then fail Layer B and the CLI refuses to start. But "intersect silently" cannot be the whole answer either: there is no way today to enumerate "every name in any configuration", so a typo and a disabled tool are indistinguishable and M4 survives for any workspace using `disable_tools`.

Resolution — introduce `tools.AllToolNames() []string`, a compiled, sorted catalogue of every `Tool.Name()` the binary can register, asserted complete by `TestAllToolNamesMatchesFullRegistry` (constructs a registry with a workspace set, Tavily configured, `DisableTools` empty, and diffs). Then:

1. A name **not in the catalogue** is a typo ⇒ **fatal at Layer B**, with a nearest-name suggestion.
2. A name **in the catalogue but absent from the registry the role's position receives** is *disabled*, not misspelled ⇒ dropped at Layer C/D with **one stderr warning** naming the role, the tool, and the reason (`--disable-tool`, `[tools].disable_tools`, unresolved workspace, or mandatory denylist). Silent dropping is what makes H3 possible.
3. The same name in `disallowed_tools` is always a no-op, never an error.
4. A drop that empties the set is subject to `fail_on_empty_toolset` (H2), and the error names the drop reason.

**`skills` entries are not validated here.** Deferred to `06` per P4. When `06` lands it takes the same shape as rule 2 above — a `skills` entry naming an absent skill is a **warning and a drop, never fatal**. The skill corpus is workspace content and legitimately differs per repo; a user-level role must not be able to brick startup in an unrelated workspace.

**Role files are read outside symlink containment.** Every analogous loader uses bare `os.ReadFile` (`cli/prompt.go:161-162`, `skills/loader.go:56`, `config/load.go:163`), and `FirstExisting` accepts anything `Mode().IsRegular()` — which a symlink to a regular file satisfies (`config/paths.go:58-69`). The only symlink resolution in the tree is `internal/workspace/root.go`, reached by *tool* paths only, and rule 10's symlink clause covers **writes**. So `.mivia/agents/leak.md` → `~/.ssh/id_ed25519` becomes a system prompt sent to the provider. `LoadDir` MUST `os.Lstat` each entry and skip non-regular files, and MUST enforce a **whole-file** cap mirroring `maxSkillBytes` — §6's 256 KiB cap is on frontmatter only and bounds nothing about the body.

**Rollback criterion:** if the hybrid's failure modes (H2/H3 in particular) prove confusing in practice, collapse to markdown-only with TOML holding just `[agents.guardrails]` and the gate. Rule B is the piece to drop first — it is the sole source of H2, H3, and H6.
