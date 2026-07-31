# 05 — Role model core: roles in TOML

**Status:** Design-ready — no open decisions.
**Date:** 2026-07-29 · rewritten 2026-07-31
**Commits:** `feat(agent): add declarative agent roles`, `feat(cli): resolve and scope roles from config`
**Depends on:** `01` (enforcement) and `04` (namespace) — **both shipped 2026-07-29, so this is unblocked.** **Blocks:** `06`, `07`.
**Blast radius:** HIGH (privilege surface).

> **Anchors re-derived at HEAD `d88fe46` (2026-07-31).** They will drift again — per `00`'s standing note, grep the symbol, not the number.

## Revision history

Challenge round 2 (2026-07-31, 4 independent agents + direct verification) falsified several premises of the hybrid design; the decisions taken in response are:

1. **Roles are TOML-only.** The markdown medium (`.mivia/agents/*.md`) is dropped. This deletes Rule A, Rule B, the frontmatter work, and four of the nine failure modes — see §12.
2. **Prompts live inline** as `system_prompt = """…"""`. No prompt file, no path to resolve.
3. **The workspace gate stays**, and is a **user-config key** — not a CLI flag, not an env var (§5).
4. **`--agent` and root-session scoping move here** from `08` §2 (§7).
5. **`tools_add` / `tools_remove`** are added so a derived role need not restate its parent's list (§4).

---

## 1. Goal

Named roles — `researcher` (read-only), `engineer` (full edit), `reviewer` (read-only + audit) — each with its own system prompt, scoped tools, model and turn budget, declared as `[[agents.roles]]` in `mivia.toml`.

**A role is an agent.** There is no separate agent entity that a role is attached to: the role definition *is* the agent definition. Each role becomes one handler registered under its own name (`Kind=Subagent`); a task selects a role and mivia runs a disposable instance of it. Ten concurrent tasks on `researcher` are ten instances of one role.

## 2. Preconditions

| # | Change | Why | Where |
|---|---|---|---|
| **P1** | ✅ **Done — shipped in `01` (2026-07-29, `f0bf99b`).** Dispatch-boundary enforcement is live: `executeToolTask` rejects any tool absent from the visible registry (`internal/agent/loop_tools.go:363-374`); `newScopedLoop` builds the child dispatcher from the restricted registry (`internal/subagents/multi_step.go:206-216`). Pinned by INV-AG-7 | plan `01` |
| ~~P2~~ | **No longer applicable.** The frontmatter parser was a precondition only for markdown roles. Roles now parse through `go-toml/v2` with the rest of the config. `internal/skills/frontmatter.go` is **not touched by this plan**, so INV-AG-17 is undisturbed and no `SKILL.md` behaviour changes | — |
| **P3** | Hoist `skills.LoadMarkdown` out of `attachSessionDispatcher` into `runChat` | Skills load after the tool registry (`chat_repl.go:76` inside `attachSessionDispatcher`, called from `chat_command.go:80`). Layer B needs the skill **names** to reject role/skill collisions (H5), and cannot see them from inside the function it precedes | `cli/chat_command.go`, `chat_repl.go:69-86`; 2 test call sites (`interactive_session_test.go:89,216`) |
| **P4** | Gate the `skills` field on `06` | `skills.LoadMarkdown` returns an **empty registry, not an error**, when `.mivia/skills/` is absent (`internal/skills/loader.go:26-28`). Validating `skills:` entries today would make a user-level role fail startup in every workspace lacking that skill. See §4 | this plan |

> **P3 carries a `--no-tools` trap.** `attachSessionDispatcher` early-returns when `sess.Tools == nil` (`chat_repl.go:70-72`), which is exactly the `--no-tools` path (`configureChatWorkspace` returns early at `chat_repl.go:34-36`). **Today `--no-tools` never loads skills at all.** An unconditional hoist makes a malformed `SKILL.md` newly fatal in pure-chat mode. Gate the hoist on `useTools`; roles are not resolved when tools are off.

## 3. Sources and precedence

Both config files are read at **fixed paths**, not through `config.Load`. This is required, not stylistic: `config.Load` takes `FirstExisting(DefaultConfigCandidates())` (`config/paths.go:31-43`, `config/load.go:155`) with **no layering**, so a workspace `.mivia/mivia.toml` shadows `~/.config/mivia/config.toml` *entirely* — user roles would vanish the moment a repo shipped a config file.

| Rank | Source | Trust | On name collision |
|---|---|---|---|
| 1 | Built-in `default` role (compiled, generic per rule 60) | compiled | base |
| 2 | User `~/.config/mivia/config.toml` `[[agents.roles]]` | trusted; always loads | wins |
| 3 | Workspace `<cwd>/.mivia/mivia.toml` `[[agents.roles]]` | untrusted; **gated off by default** (§5) | **rejected, with a warning naming both files** |
| 4 | CLI (`--agent` selects; `--disable-tool` narrows globally) | — | after resolution |

A workspace role never replaces a user role. Silently letting it win would make the gate pointless for any name a user had already defined; erroring on a name the user chose first would let a repo deny service by guessing common names. Warn and ignore. Pin with `TestWorkspaceRoleCannotShadowUserRole`.

### 3.1 Resolution procedure — normative

1. **Collect specs.** For each name, at most one spec. Every field carries an explicit present/absent bit (§8 — pointers throughout; presence is never inferred from `len()`).
2. **Resolve the base.** Recursively resolve `inherits` (default `"default"`). Cycle or unknown name ⇒ fatal.
3. **Apply inheritance.** For `tools`, `disallowed_tools`, `skills`, `max_turns`, `model`, `system_prompt`: absent ⇒ take the base's resolved value; present ⇒ take the spec's own value. **Inheritance replaces.** `description` is never inherited (§4).
4. **Apply deltas.** `tools_add` / `tools_remove` apply to the result of step 3. A spec setting both `tools` and either delta is a fatal error — the two forms answer the same question differently and there is no useful reading of both.
5. **Apply guardrails** (§4 evaluation order: mandatory denylist → `disallowed_tools` → allowlist).
6. **Intersect with the registry the role's position actually receives** (§7).

Steps 3 and 4 must not be merged into one pass. `tools_add` over an inherited pool is the whole point; `tools_add` over a *stated* `tools` list is a second way to write one list, which is why step 4 rejects it.

## 4. Schema

Only fields with a real enforcement point ship.

| Field | Type | Enforcement point |
|---|---|---|
| `name` | string, required | dispatcher registration key (`Kind=Subagent`); `--agent` lookup |
| `description` | string, required, **not inherited** | injected at **runtime** into `dispatch_tasks`/`spawn_agent` `Description()` as the routing hint. **Must pass `skills.SanitizeModelFacingText` at `descriptionMaxLen`** — see below |
| `system_prompt` | `*string`, required | the role's system prompt. Multi-line `"""…"""`. An explicit `""` is a load error, not "inherit the base" |
| `inherits` | `*string`, default `"default"` | §3.1 step 2; cycle or unknown = load error |
| `tools` | `*[]string` | `ScopedRegistry` → `Loop.Tools` → P1 gate (`loop_tools.go:363-374`) |
| `tools_add` | `*[]string` | delta over the inherited pool (§3.1 step 4); mutually exclusive with `tools` |
| `tools_remove` | `*[]string` | same; mutually exclusive with `tools` |
| `disallowed_tools` | `*[]string` | applied before `tools` |
| `skills` | `*[]string` | invocation allowlist — **accepted, warned, ignored until `06`** (P4) |
| `model` | `*string` | `MultiStepHandler.Model` — real today; per-role handlers are separate instances. **Provider is not settable**; an invalid model fails at request time, not load time |
| `max_turns` | `*int` | `MultiStepHandler.MaxSteps` (spawned) / chat step budget (root) |

**Omitted entirely:** `provider`, `permission_mode`, `run_program_allowlist`, `can_spawn`, `max_depth`, `inherits_pool`, `mcpServers`, `hooks`, `memory`, `background`, `effort`, `isolation`, `color`, `initialPrompt`.

> **There is no "reserved" tier.** An earlier revision reserved the first three "because the TOML shape is already published." It is not: `permission_mode`, `run_program_allowlist` and the whole `[agents]` section appear **nowhere** outside `.mivia/plans/` — not in `docs/product/config.md`, not in `.mivia/mivia.toml.example` (whose sections are `provider`, `providers.*`, `chat`, `tools`, `integrations.tavily`, `subagents`, `privacy`), not in `internal/config/types.go`. `00` §3 invariant 2 applies with no exception: a field with no runtime hook is omitted. See `00` §1 for why the last three are vacuous rather than merely unimplemented.

### `description` is untrusted workspace text on the root agent's tool surface

1. **This is not the first runtime injection.** `dispatchTasksTool.Description()` already appends `"Available skill handlers: …"` from `skillReg.ListModelFacing` (`internal/cli/dispatch.go:85-93`) and into the `handler` JSON-schema `enum` (`:143-148`); `spawnAgentTool` mirrors it (`internal/cli/orchestrate.go:95-103, 167-180`). Both register on the **root** registry, so the text already lands in root-agent context. The rule-60 `chore(ai)` amendment covers an **existing** condition and still ships here.
2. **The existing path sanitizes and this plan must too.** Skill name/description run through `skills.SanitizeModelFacingText` (`internal/skills/skills.go:104-121` — strips ASCII control chars, `\` and `"`, collapses whitespace) at `nameMaxLen = 64` / `descriptionMaxLen = 200` (`loader.go:155-160`). Role `description` is required and injected; it uses the same sanitizer and caps, and the joined role list is length-capped.

**Rule-60 guard to survive:** `TestSessionToolSurfaceIsProjectAndLanguageGeneric` (`internal/cli/session_tool_surface_test.go`) scans the model-facing surface for `golang`, `go.mod`, `*.go`, `sql`, `database`, `github.com/MiviaLabs`. A role described as "Go code review" fails it. The amendment must state that a workspace-authored description is user config and outside the compiled-surface scope, and the test must exclude runtime-injected text explicitly rather than by accident.

### Tool inheritance

- `tools` **omitted, no deltas** ⇒ inherit the resolved pool.
- `tools` **omitted, `tools_add` set** ⇒ inherited pool **plus** the delta. This is the intended way to derive a role:
  ```toml
  [[agents.roles]]
  name      = "researcher_plus"
  inherits  = "researcher"
  tools_add = ["write_file"]
  ```
  It stays correct when `researcher` changes. Restating the parent's list would not.
- `tools = []` **explicit empty** ⇒ zero tools ⇒ load error under `fail_on_empty_toolset`.
- Guardrail `require_explicit_tools = true` flips **authored** roles to deny-by-default: a role omitting `tools` resolves to ∅, and `tools_add` then applies over ∅ — which is exactly right, since the author named every tool. **Default `false`.**
  - **The flag never applies to the compiled `default` role.** A compiled role cannot be "explicit" about a registry that varies with `disable_tools` and workspace resolution, and applying it makes plain `mivia chat` unstartable (∅ pool + `fail_on_empty_toolset` ⇒ fatal) with no user-editable cause. Pin with `TestRequireExplicitTools_DefaultRoleUnaffected`.

> **This reverses the predecessor plan's deny-by-default.** It justified the stance as adopting Claude Code's model; [the actual schema](https://code.claude.com/docs/en/sub-agents) is inherit-by-default (`tools` omitted ⇒ inherits all). The stance was a mivia invention presented as industry-validated, and its `inherits_pool` escape hatch was ticked by the one role (`engineer`) most users define — buying friction, not security. `require_explicit_tools` preserves the strict posture for shops that want it. **Call the reversal out in the changelog.**

### `max_turns`

`*int`. **`nil` = unset**; unset inherits. **`max_turns = 0` is a load error**: `0` already means "use the built-in 100" when spawned (`multi_step.go:227-230`) and "unlimited" at root (`config/types.go:108-111`, where `Resolved.MaxSteps` is `*int` for exactly this reason), and a role field that means opposite things by position is not shippable. The compiled `default` role's `max_turns` is `SubagentConfig.NestedSteps` (`config/defaults.go:24`).

### Guardrails — and where they may be set

```toml
[agents.guardrails]
mandatory_tool_denylist = []          # ADDITIONS ONLY; the baseline names are compiled
fail_on_empty_toolset  = true
require_explicit_tools = false
```

> **Guardrails resolve from user config; a workspace value may tighten, never loosen.** Because `config.Load` does not layer (§3), a user who sets `require_explicit_tools = true` in `~/.config/mivia/config.toml` would otherwise have it silently discarded by any repo shipping `.mivia/mivia.toml` — the strict posture would evaporate on `git clone`. Read both fixed paths (the same helper §3 already requires); the user value is the floor; a workspace value applies only when it tightens (`false`→`true` for both booleans; the denylist may only add). Pin with `TestGuardrails_WorkspaceCannotLoosen`.

> **`mandatory_tool_denylist` is a compiled constant that config may only ADD to — and it is a mirror, not the gate.**
> The real gate is the `tools.PrivilegedTool` type marker: `restrictedRegistry` admits a tool only when `!blocked[t.Name()] && !privileged` (`internal/subagents/multi_step.go:251`), backstopped by a startup assertion in `registerSessionTool` (`internal/cli/dispatcher.go:191-194`) whose comment says the marker exists "so future control tools do not depend solely on a name denylist." A config-surfaced name list can only drift from it. The example therefore shows an **empty additions list**, never the baseline values — printing them invites an edit that `go-toml/v2` accepts and that "may only ADD" then silently no-ops. Pin the mirror with `TestMandatoryDenylistMatchesPrivilegedMarker`.
>
> Keeping the floor compiled is not negotiable:
> 1. **`run_command` bypasses path guards entirely.** File tools consult `isSecretPath` and `run_command` screens argv, but a shell invocation that builds the path at runtime slips past both — and the recommended `run_allowlist` in `.mivia/mivia.toml.example:53-79` includes `sh`, `bash`, `python`, `tee`, `sed`, `cp`, `mv`, `rm`. Any role holding `run_command` writes `mivia.toml` via `bash -c`.
> 2. **`write_file` needs no allowlist at all.** Nothing stops a role holding `write_file`/`search_replace` from rewriting `.mivia/agent-prompt.md`, `.mivia/mivia.toml`, or `.mivia/skills/*/SKILL.md` — next session's root system prompt.
> 3. **`isSecretPath` does `strings.Contains`, not glob matching** (`internal/tools/tools.go:325`, `:338`), so a `.mivia/**` pattern matches nothing — and a bare `mivia.toml` pattern also blocks `mivia.toml.example`, which `09` §4 requires the agent to edit.
> 4. **There is no path-filter mitigation left.** `04` §5 deleted the compiled secret-pattern list; `config/defaults.go:30-37` ships none, and INV-SEC-1 records that an unconfigured workspace filters nothing.
>
> **Scope: positional, not per-role.** The denylist filters the registry handed to a **spawned** agent. It is deliberately **not** applied to the root session's registry unless the root's own role excludes those tools — `06` §2 places its entire enforcement in `dispatchTasksTool.buildTasks`/`spawnAgentTool.Execute` and would become unreachable code if the root always lost them. Pin with `TestMandatoryDenylist_RootExempt_SpawnedFiltered`.

### Full example

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

The gate does **not** appear in this block — it is user-config-only (§5), and showing it under a heading that reads as `mivia.toml` is exactly the wrong thing to ship, given `09` §4 ships `.mivia/mivia.toml.example` and `09` §7 treats a wrong example as a shipped bug:

```toml
# ~/.config/mivia/config.toml — NOT the workspace file
[agents]
load_workspace_roles = false   # default; gates workspace [[agents.roles]]
```

## 5. Workspace roles are gated off by default — and the gate lives outside the workspace

A role's `system_prompt` **is** the system prompt, unwrapped. A cloned repo shipping `[[agents.roles]]` in `.mivia/mivia.toml` would otherwise get a real system message for free, on a handler the model can select by name.

**Gate:** `[agents] load_workspace_roles`, read from `~/.config/mivia/config.toml` **at its fixed path**, never via `config.Load` (§3). Default `false`. User-level roles load unconditionally.

> **The gate cannot live in `mivia.toml`.** `DefaultConfigCandidates()` (`internal/config/paths.go:31-43`) resolves `$MIVIA_CONFIG`, then **`<cwd>/.mivia/mivia.toml`**, then `~/.config/mivia/config.toml`, and `loadFile` takes `FirstExisting` (`config/load.go:155`). A hostile repo would ship `mivia.toml` containing `load_workspace_roles = true` and authorize itself. Same reasoning `04` §5 applies to the namespace directory: *a floor the agent can lower is not a floor.* Pin with `TestGate_IgnoredInWorkspaceConfig` — a workspace value warns; it never authorizes.

> **Rejected: an env-var gate.** It would have to use `os.LookupEnv` only — and the house pattern is `envfile.Lookup`, whose `DefaultEnvCandidates()` puts **`<cwd>/.env` first** (`config/paths.go:46-55`) and which `config.Load` already uses (`config/load.go:39`, `:113`). An implementer following the established pattern would hand the workspace its own gate, one helper call away. There is no env override, so there is nothing to get wrong. (`$MIVIA_CONFIG` itself is safe — `paths.go:33` uses `os.Getenv` — but it selects the config *file*, which is why the gate is read from a fixed path.)
>
> **Rejected: a CLI flag.** Retyped every session, so in practice it gets aliased — and an alias is a gate that is on everywhere, including in the hostile repo it exists to stop.

> **Pre-existing, ungated, and deliberately out of scope.** Four workspace-controlled paths already reach a system prompt, none of them gated. This plan does not close them; `09` must state all four in `docs/security/overview.md` so the role gate is not read as a claim about the class.
> 1. `[chat].system_prompt` in a workspace `mivia.toml` (`config/types.go:104` → `chat_command.go:56-62`) — the **root** system prompt, and it takes precedence over `.mivia/agent-prompt.md`, which never runs when it is set.
> 2. `[subagents].system_prompt` (`config/types.go:121` → `dispatcher.go:93-95`, `:110-113`) — every subagent's system prompt, including the full-registry `multi_step` handler.
> 3. `.mivia/agent-prompt.md`, read verbatim with no wrapper (`loadAgentPrompt`, `internal/cli/prompt.go:157-173` → `chat_command.go:58`). **`04` §5 explicitly DECIDED not to gate this** (2026-07-30); the exposure is accepted and recorded in `docs/security/overview.md:49`. An earlier revision of this plan said "`04` must gate it behind the same switch" — that is stale and reversed.
> 4. **Workspace skill bodies.** `registerSkillHandlers` (`internal/cli/dispatcher.go:135-174`) sets `SystemPrompt: skill.Instructions` **verbatim** (`:150-162`) on a `MultiStepHandler` holding `FullRegistry: reg` (`:159`), registered under `Kind=Subagent` (`:168`). The untrusted-content preamble at `skills/loader.go:141-142` lives in `skillRunner`, which serves the `runtime.Skill` path the model never reaches. An earlier revision of this plan justified the role gate by contrasting it with wrapped skill bodies; that contrast was false.

## 6. Parsing

Roles parse through `go-toml/v2` with the rest of the config. **No new parser, and no change to any existing one.** `internal/skills/frontmatter.go` is untouched, so INV-AG-17 stands and no `SKILL.md` behaviour changes.

`go-toml/v2 v2.2.3` distinguishes a missing `[]string` key (`nil`) from `= []` (non-nil, len 0) — verified empirically — but does **not** distinguish a missing string key from `= ""`. So `system_prompt`, `description`, `model` and `inherits` are `*string` in the TOML struct, and an explicit `system_prompt = ""` is a load error rather than a silent "inherit the base". Slices use pointers too, for uniformity and so presence is never inferred from `len()`.

## 7. Validation layers

```
config.Load                                   → Layer A
configureChatWorkspace  → tools.NewDefaultRegistry
skills.LoadMarkdown (hoisted, gated on useTools)
roles.LoadAndResolve (reads both fixed paths)  → Layer B   ← single validation point
attachSessionDispatcher(…, rootRole)           → Layer C
  ├─ NewSessionDispatcher registers session tools CONDITIONALLY on rootRole
  └─ registers one MultiStepHandler per role under Kind=Subagent
```

| Layer | Where | Validates | Error |
|---|---|---|---|
| **A** | `config.Load` / `internal/config/agents.go` | TOML types; duplicate `name` within one file; name charset; `tools` vs `tools_add`/`tools_remove` exclusivity; guardrail types | `parse config <path>: agents.roles[2]: duplicate role "reviewer"` — fatal |
| **B** | `internal/cli/agent_roles.go` | §3.1; `inherits` cycle/unknown; workspace-vs-user collision (warn+ignore); **every tool name is in `tools.AllToolNames()`**; role vs skill vs reserved-handler collision; `--agent <name>` resolves | `role "researcher" (~/.config/mivia/config.toml): unknown tool "readfile"` — fatal |
| **C** | `attachSessionDispatcher` / `NewSessionDispatcher` | conditional registration of session tools for the root role; one `MultiStepHandler` per role; registry intersection for the *spawned* position; empty-toolset refusal naming the source file | fatal |

> **`--agent` root scoping moved here from `08` §2 (decided 2026-07-31).** `08` keeps the *inspection* surface — `mivia agents list`, `--explain`, `doctor`, `/agents`, the TUI banner — and its §2 is now a pointer here. The flag's parsing, validation, scoping and tests ship with the role model, because a scoping guarantee and its only caller must not land in different cycles.

### The registry is not the same object at B and C

1. **`NewSessionDispatcher` mutates the registry it is handed.** `registerSessionTool` ends with `reg.Register(tool)` (`internal/cli/dispatcher.go:201`), and it is called for `delegate`, `dispatch_tasks` (`:176-184`), `spawn_agent`, `inspect_agents`, `join_run`, `cancel_run` (`:83`) and the ledger tools (`:86`). The root loop's enforced registry **is** that object (`internal/chat/session.go:282` builds `agent.Loop{… Tools: s.Tools …}`).
   ⇒ **Scoping the root registry before `attachSessionDispatcher` is the one insertion point where scoping is guaranteed to be undone.** `mivia chat --agent researcher` would end up holding all six delegation tools. The predecessor plan prescribed exactly that insertion point.
   ⇒ Compounding it: `MultiStepHandler.FullRegistry` is the **same pointer** as `sess.Tools` (`dispatcher.go:122`), so the spawner's effective pool would keep mutating after resolution.
   ⇒ **Therefore: conditional registration, not post-hoc filtering.** The root role is passed *into* `NewSessionDispatcher`, which registers a session tool only when the role admits it. One registry, one truth, nothing to undo. (Scoping after `attachSessionDispatcher` returns also works and is the documented fallback; it is rejected as the primary because it leaves a window in which `sess.Tools` — and every handler aliasing it — is wider than the role.)
2. **At Layer B the registry holds only `registerDefaultTools`' output**, and `find_references` registers only for a resolved workspace (`default_registry.go:114`).
   ⇒ Layer B validates names against **`tools.AllToolNames()`** — a new compiled catalogue (§11) — not against the constructed registry.
3. **The mandatory denylist is positional.** The root keeps `dispatch_tasks`/`spawn_agent` unless its own role excludes them; the spawned registry drops them *and* everything implementing `tools.PrivilegedTool`.

`TestRootSession_AgentFlag` must assert the **final** registry contents *after* dispatcher attach — asserted before it, the test passes vacuously — and must assert absence of every tool the role excludes.

### `ScopedRegistry` must keep the `PrivilegedTool` marker

`restrictedRegistry` filters on **two** things — the name denylist **and** the type marker (`multi_step.go:251`). A name-only `ScopedRegistry(reg, allow, deny)` drops the marker check, which is pinned by `TestRestrictedRegistryExcludesPrivilegedMarker` (`internal/subagents/multi_step_scoped_test.go:102-109`), by `internal/cli/session_tool_privilege_test.go:41-56,70-86`, and by **INV-AG-7**. `ScopedRegistry` applies the marker exclusion unconditionally, and `restrictedRegistry` **is retained as a thin delegation to it**, not deleted — it is a method, and three test files call it directly (`multi_step_test.go:148,226`, `multi_step_scoped_test.go:106`). Moving the restriction to construction time would also strip the marker filter from the built-in `multi_step` handler (`dispatcher.go:121-128`) and every skill handler (`:157-167`), which are constructed with the raw `reg`.

### Two further consequences

- **`--agent <name>` cannot be validated at flag-parse time.** It does not exist yet in any form. Parse it with `flagValue` (`internal/cli/root.go:69`, as `--provider` does at `chat_command.go:18`) — **not** the `chatFlags` switch (`chat_repl.go:20-34`), which handles boolean flags only — or the unknown-arg check at `chat_command.go:43-45` rejects it. Add it to `printUsage` (`root.go:36-66`) or the usage text lies. Validate at Layer B; the error lists available roles. The same error text must serve a model-emitted bad `role`: today an unknown name reaches `Dispatcher.Invoke` and returns a bare `unknown subagent "foo"` (`runtime/dispatcher.go:228-230`) with no list of valid names.
- **Non-chat entry points get no roles, and Go cannot prevent that.** The only production dispatcher construction is `chat_repl.go:80` (shared by `oneShot`, `repl` and `runTUI`). `NewSessionDispatcherWithLedger` (`dispatcher.go:49`) has **zero callers, production or test** — a second exported doorway that would silently get no roles; **delete it**. There is no package allowlist, no import-boundary lint, and `semgrep/agent-standards.yml` holds content patterns rather than dependency rules, so "`roles.LoadAndResolve` is the only constructor" is not enforceable by tooling. The enforceable version is the one the repo already uses for privileged tools: a **required, non-variadic parameter** that does not compile if omitted. `NewSessionDispatcher` currently takes `skillReg ...*skills.Registry` — a second registry cannot be added variadically, so the signature changes and 7 test call sites move (`delegation_test.go:484,531,613,646,684`, `session_tool_surface_test.go:79,227`).

## 8. File layout

Policy: soft 500 / hard 800 per file, soft 80 / hard 120 per func (`.mivia/policy/go-structure.json`).

| File | Est. | Contents |
|---|---:|---|
| `internal/roles/role.go` | ~90 | `Spec`, `ResolvedRole`, `Guardrails`, `Origin`, `Registry` |
| `internal/roles/resolve.go` | ~170 | §3.1 procedure + sub-funcs, each < 60 LOC |
| `internal/roles/validate.go` | ~80 | `ValidateAgainstCatalogue`, `IntersectWithRegistry` |
| `internal/tools/scope.go` | ~50 | `ScopedRegistry` — exposure filter incl. `PrivilegedTool` |
| `internal/tools/names.go` | ~40 | `AllToolNames()` compiled catalogue (§11) |
| `internal/subagents/names.go` | ~15 | `ReservedHandlerNames = {"delegate","oneshot","multi_step"}` (§9 H5) |
| `internal/config/agents.go` | ~90 | TOML types + the two-fixed-path reader (§3) |
| `internal/cli/agent_roles.go` | ~150 | Layer B; `--agent` parse and validate; per-role handler registration |

`internal/roles/markdown.go` and `internal/roles/merge.go` are **gone** with the markdown medium.

### Types

```go
// Spec: one authored role, pre-resolution. Presence-preserving — every optional
// field is a pointer, and presence is never inferred from len().
type Spec struct {
    Name            string
    Description     *string
    SystemPrompt    *string
    Model           *string
    Inherits        *string
    MaxTurns        *int
    Tools           *[]string
    ToolsAdd        *[]string
    ToolsRemove     *[]string
    DisallowedTools *[]string
    Skills          *[]string
    Origin          Origin    // which file, for errors and 08's --explain
}

// ResolvedRole: post-inherit, post-delta, post-guardrail.
type ResolvedRole struct {
    Name, Description, SystemPrompt, Model string
    MaxTurns       int
    EffectiveTools []string          // sorted, deduped, position-dependent
    Skills         *[]string         // nil still means "all" — 06 requires it
    Origin         Origin
}
```

`roles.Registry` is the third `Registry` in the tree (`tools.Registry`, `skills.Registry`); always qualify it.

### Modified

`internal/cli/chat_command.go` 114→~140 · `internal/cli/chat_repl.go` 172→~180 · `internal/cli/dispatcher.go` 237→~275 (signature change; delete `NewSessionDispatcherWithLedger`) · `internal/cli/root.go` (`--agent` in `printUsage`) · `internal/cli/orchestrate.go` +6 · `internal/cli/dispatch.go` +6 · `internal/subagents/multi_step.go` ±0 (`restrictedRegistry` delegates to `ScopedRegistry`) · `internal/config/types.go` +1 · **`Makefile`** (§10 — mandatory) · **`.mivia/invariants.md`** (new row) · **`.mivia/plans/08-role-cli-and-observability.md`** (§2 → pointer) · **`.mivia/plans/09-role-docs-and-examples.md:63`** (drops the markdown example) · **`.mivia/plans/00-agent-roles-program-overview.md:5,64`** (scope line and program invariant 3) · **`.mivia/INDEX.md:105`** (stale invariant numbering, §10) · test call sites: `interactive_session_test.go:89,216`, `delegation_test.go:484,531,613,646,684`, `session_tool_surface_test.go:79,227`, `multi_step_test.go:148,226`, `multi_step_scoped_test.go:106`.

### Structure-check headroom — the real numbers

| File | Actual | Headroom |
|---|---:|---|
| `internal/cli/orchestrate.go` | **475** | **19 lines to the soft-500 warn** |
| `internal/cli/dispatcher.go` | 237 | 263 |
| `internal/config/types.go` | 176 | 324 |
| `internal/cli/chat_command.go` | 114 | 386 |
| `internal/cli/chat_repl.go` | 172 | 261 (grandfathered `maxLines: 433`) |
| `internal/subagents/multi_step.go` | 259 | 241 |

`make verify` runs `check_go_structure.py --strict --all` (`Makefile:64`), and `--strict` promotes the soft-500 **warning to a hard failure** (`check_go_structure.py:239-241`). `orchestrate.go` +6 ⇒ 481 leaves 19 lines, not 100 — an earlier revision's "no touched file is near a limit" rested on a stale count of 393. `chat_repl.go`'s baseline hard-fails on any growth past 433 (`check_go_structure.py:179-186`). No other touched file is baselined, so all are subject to the hard 800.

## 9. Failure modes

| ID | Failure mode | Mitigation |
|---|---|---|
| H1 | A cloned repo's `[[agents.roles]]` `system_prompt` becomes a real system message on a model-selectable handler | `load_workspace_roles = false` default, gate in user config only (§5). Does **not** close the four pre-existing surfaces — those are documented, not fixed |
| H5 | Name collisions across three namespaces | lowercase-normalize; duplicate in one file = Layer-A error; workspace-vs-user = warn and ignore (§3). A role colliding with a **skill** name or a **reserved handler** name is a Layer-B error naming both sources. Reserved names are exported as `subagents.ReservedHandlerNames` (§8), which `internal/roles` may import without a cycle, and `dispatcher.go:100,103,129` must use it so the list cannot drift — today they are bare string literals in `internal/cli`, invisible to `internal/roles`. A role colliding with a **tool** name is an error **by policy, not by necessity**: `Kind=Tool` and `Kind=Subagent` are separate maps (`runtime/dispatcher.go:108`) so nothing breaks at runtime, but `dispatch_tasks`' `handler` field is free-form and an ambiguous name routes unpredictably. `--agent multi_step` resolves to `default` with a deprecation note; `multi_step` may not be a role name |
| H7 | `--agent` unvalidatable at flag parse | validate at Layer B; error lists roles. Add the flag to `flagValue` and to `printUsage` (§7) |
| H8 | Renaming a role breaks resume | The mechanism is **not** the fingerprint: `ResumeInterruptedRun` passes an empty one (`internal/coordinator/recovery.go:126`). The handler name is **persisted** as `TaskSnapshot.HandlerName` (`coordinator/spawn.go:198`) and re-dispatched verbatim (`recovery.go:383-397`), failing at `runtime/dispatcher.go:228-230` with `unknown subagent %q`. **This is pre-existing for skills.** Separately and genuinely fingerprint-related: renaming a role changes `fingerprintTask.Name` (`spawn.go:113-128`), so a repeat `spawn_agent` with the same idempotency key returns `ErrIdempotencyConflict`. Operational note in docs for both; do not rename roles with runs in flight. Revisit in `07` |
| H9 | Roles load at Layer B but config at Layer A ⇒ `mivia doctor` sees config and no resolved roles | `roles.LoadAndResolve` is the single constructor; `doctor` prints "roles not loaded" until `08` wires it. `doctor.go`/`config_cmd.go` are **not** modified by this plan |
| H10 | **Role scoping bounds a role, not a session** | `multi_step` (`dispatcher.go:121-128`) and every workspace skill handler (`:157-167`) remain **full-registry** and remain selectable by a model-supplied free-form `handler` string (`dispatch.go:255-258`), and the compiled root prompt tells the model to prefer `multi_step` for file access. The program's stated #1 risk is prompt injection of the **root** agent (`00` §2); an injected root does not select `researcher`, it selects `multi_step`. Roles are a bound on work the model chooses to route through them. `09` must state this in `docs/security/overview.md`. **Fixed in `07`**, which owns routing; removing or gating the unscoped default handler is not budgeted here |

H2, H3, H4 and H6 are **deleted** — each existed only because two media could disagree about one role. See §12.

## 10. Acceptance tests

1. `TestRoleResolve_InheritsPool` — `tools` omitted inherits the base's pool
2. `TestRoleResolve_ToolsAddExtendsParent` — `inherits` + `tools_add` yields parent ∪ delta, and tracks a change to the parent
3. `TestRoleResolve_ToolsAndToolsAddIsError` — §3.1 step 4 exclusivity
4. `TestRoleResolve_ToolsRemove`
5. `TestResolve_InheritanceCycle`
6. `TestResolve_EmptyToolsetRefused`
7. `TestResolve_EvalOrder` — builds a registry *containing* a mandatory-denylist tool and asserts the allowlist entry is still denied, with reason `"mandatory denylist"` rather than `"unknown tool"`, so M2 cannot pass by accident
8. `TestValidateAgainstCatalogue_UnknownToolName` — `readfile` vs `read_file`; **stays fatal even when the tool is also disabled**
9. `TestRoleAllowlistIntersectsDisabledTools` — §11
10. `TestAllToolNamesMatchesFullRegistry` — §11
11. `TestScopedRegistry` — including `PrivilegedTool` exclusion
12. `TestWorkspaceRolesGate` — workspace `[[agents.roles]]` ignored when the gate is unset
13. `TestGate_IgnoredInWorkspaceConfig` — a workspace `mivia.toml` setting the gate warns; it never authorizes
14. `TestWorkspaceRoleCannotShadowUserRole` — §3
15. `TestGuardrails_WorkspaceCannotLoosen`
16. `TestMandatoryDenylist_RootExempt_SpawnedFiltered`
17. `TestMandatoryDenylistMatchesPrivilegedMarker`
18. `TestRequireExplicitTools_DefaultRoleUnaffected`
19. `TestRoleSpec_NilVsEmpty` — missing key, `= []`, and `= ""` produce distinct `Spec` values; `system_prompt = ""` is an error
20. `TestRoleMaxTurnsZeroIsError`
21. `TestRoleDescriptionSanitized` — control chars, quotes, over-length, injection string
22. `TestRoleNameCollidesWithSkill`, `TestRoleNameCollidesWithReservedHandler` — H5
23. `TestRootSession_AgentFlag` — asserts the registry **after** dispatcher attach, and absence of every tool the role excludes
24. `TestRootSession_AgentFlagUnknownName` — error lists available roles
25. `TestRoleScopedAgentCannotWriteFile` — **integration**: a role-scoped loop emits `write_file`; assert refusal *and* that the file is not on disk. Table-driven per rule 20
26. **Built-binary integration test for `mivia chat --agent <role>`** — rule 20 forbids fake-only closure for a shipped command

### Mutation proofs

| # | Mutation | Test that MUST fail |
|---|---|---|
| M1 | `tools_add` replaces the inherited pool instead of extending it | `TestRoleResolve_ToolsAddExtendsParent` |
| M2 | Apply allowlist before mandatory denylist | `TestResolve_EvalOrder` |
| M3 | Default the gate to `true` | `TestWorkspaceRolesGate` |
| M4 | Skip catalogue validation | `TestValidateAgainstCatalogue_UnknownToolName` |
| M5 | Treat `tools = []` as "inherit" | `TestResolve_EmptyToolsetRefused` |
| M6 | Let a workspace role replace a same-named user role | `TestWorkspaceRoleCannotShadowUserRole` |
| M7 | Read guardrails from `config.Load`'s single resolved file | `TestGuardrails_WorkspaceCannotLoosen` |
| M8 | `ScopedRegistry` filters on names only | `TestScopedRegistry` |
| M9 | Skip `description` sanitization | `TestRoleDescriptionSanitized` |
| M10 | Scope the root registry *before* `attachSessionDispatcher` instead of registering conditionally | `TestRootSession_AgentFlag` |

### Invariant

```
| INV-AG-28 | Safety | Workspace configuration cannot widen a role's effective tool NAME SET beyond what the user authorized: the workspace cannot enable its own roles (the gate is read from user config at a fixed path), cannot loosen `[agents.guardrails]`, cannot lower the compiled mandatory denylist, and a workspace role never replaces a same-named user role | `TestWorkspaceRolesGate`, `TestGate_IgnoredInWorkspaceConfig`, `TestGuardrails_WorkspaceCannotLoosen`, `TestMandatoryDenylistMatchesPrivilegedMarker`, `TestWorkspaceRoleCannotShadowUserRole`, `TestResolve_EvalOrder` | |
```

> **The ID is `INV-AG-28`, not `INV-AG-8`.** `INV-AG-8` has been taken since 2026-07-30 by the message-loss invariant (`.mivia/invariants.md:58`), and IDs run contiguously through `INV-AG-24` (`:74`). `validate_invariants.py` hard-fails on duplicate IDs (`:47-86`), so an earlier revision's ID would have broken `make validate-invariants` at commit time.

> **The invariant changed shape with the medium.** In the hybrid design it said *config may only narrow*, because a reviewed markdown file was an upper bound that agent-writable TOML had to intersect against. With one medium there is no such pair, and the guarantee moves to the trust boundary between the two **files**: user config is authoritative, workspace config is gated and additive-only. `00` §3 invariant 3 must be restated to match.

> **Deliberately says name set, not privilege, and role not session.** `09` §2.2 establishes that tool-name inclusion is not a privilege ordering — `{run_command} ⊄ {read_file, write_file, grep}` yet is strictly more powerful. And per H10, this bounds a *role*, not a session: the unscoped `multi_step` handler stays selectable until `07`. Both caveats belong in `docs/security/overview.md` (`09` §1).

**`make invariants` will not run these tests, and this is a hard gate.** `Makefile:132` is a single hardcoded `-run` alternation with no `TestResolve`/`TestRole`/`TestScopedRegistry`/`TestWorkspace`/`TestGuardrails`/`TestValidateAgainstCatalogue` alternative. `validate_invariants.py` does three checks: duplicate IDs (`:47-86`), test existence (`:98-108`), **and that every referenced test is selected by the Makefile regex** (`:110-121`, exit 1 with "invariant test(s) are not selected by Makefile invariants regex"). The `Makefile` edit is **required for the commit to pass**.

## 11. Known gaps and required additions

**`--disable-tool` interaction, and how a typo differs from a disabled tool.** `--disable-tool run_command` (or `[tools].disable_tools`) removes the tool at registry construction (`disabledToolNames` + the `register` closure, `default_registry.go:87-100`). A role allowlisting `run_command` would then fail Layer B and the CLI refuses to start. But "intersect silently" cannot be the whole answer: there is no way today to enumerate "every name in any configuration", so a typo and a disabled tool are indistinguishable and M4 survives for any workspace using `disable_tools`.

Resolution — introduce `tools.AllToolNames() []string`, a compiled, sorted catalogue of every `Tool.Name()` the binary can register, asserted complete by `TestAllToolNamesMatchesFullRegistry` (constructs a registry with a workspace set, Tavily configured, `DisableTools` empty, and diffs). Then:

1. A name **not in the catalogue** is a typo ⇒ **fatal at Layer B**, with a nearest-name suggestion.
2. A name **in the catalogue but absent from the registry the role's position receives** is *disabled*, not misspelled ⇒ dropped at Layer C with **one stderr warning** naming the role, the tool, and the reason.
3. The same name in `disallowed_tools` or `tools_remove` is always a no-op, never an error.
4. A drop that empties the set is subject to `fail_on_empty_toolset`, and the error names the drop reason.

**`skills` entries are not validated here.** Deferred to `06` per P4. When `06` lands it takes the same shape as rule 2 above — an entry naming an absent skill is a **warning and a drop, never fatal**. The skill corpus is workspace content and legitimately differs per repo; a user-level role must not brick startup in an unrelated workspace.

**Rollback criterion:** if `tools_add`/`tools_remove` prove confusing beside `tools`, drop the deltas and require a role to state its full list — the deltas are pure authoring convenience and nothing else depends on them. If the gate proves to be friction with no benefit, the escalation is to state plainly in `docs/security/overview.md` that workspace roles are a fifth ungated prompt surface, not to leave the gate half-enforced.

## 12. What the TOML-only decision removed

Recorded so a future reader does not re-derive the hybrid design and re-introduce its failure modes.

| Removed | Why it existed | Why it is gone |
|---|---|---|
| `.mivia/agents/*.md` | Reference implementations use markdown agent files | One medium, one source of truth |
| **Rule A** (project markdown replaces user markdown) | Two files of the same medium could define one role | No second file |
| **Rule B** (TOML intersects markdown's allowlist, unions its denylist, min of `max_turns`) | `mivia.toml` is agent-writable, so it had to be unable to widen a reviewed file | No pair to merge. The guarantee moved to the user-vs-workspace **file** boundary (§10) |
| §6's two changes to `internal/skills/frontmatter.go` | Roles needed `key: []` to be non-nil and a bare `key:` to error | Roles no longer parse frontmatter. INV-AG-17 untouched; no `SKILL.md` breaks |
| **P2** as a precondition | The subset parser had to exist first | `go-toml/v2` already does this |
| **H2** empty intersection from two valid sources | Only reachable via Rule B | — |
| **H3** invisible shadowing | Only reachable via Rule B | — |
| **H4** key-name skew between YAML and TOML | Two key syntaxes | One syntax |
| **H6** split-brain review | Only reachable via Rule B | — |
| Symlink containment for role files | `.mivia/agents/leak.md` → `~/.ssh/id_ed25519` was a read-side escape | No role files to symlink. Config file reads are unchanged from today |
| Per-medium nil-vs-empty rules | Markdown and TOML disagreed about `[]` | `go-toml/v2` distinguishes missing from `= []` (verified); §6 |
