# Plan: Declarative Agent Roles / Teams Refactor

**Status:** Draft v2 (post challenge + validation — see §11 for dispositions)
**Date:** 2026-07-29
**Author:** ZCode (codebase audit + cross-framework research synthesis)
**Scope:** Add declarative, named agent **roles** to mivia-agent — each with its own system prompt, scoped tools (allow/deny), allowed skills, and delegation rules — so a user can define a **team of agents** and assign roles to tasks/subagents.

> **v1 scope boundary (set by the challenge phase).** v1 delivers: per-role **system prompt + tool exposure scoping + skill allowlist + can_spawn graph + max_depth/max_turns**. v1 explicitly does **NOT** deliver (deferred — see §10): per-role `run_command` **argv** scoping, per-role `permission_mode`, per-role `provider`/`model`, and handoff. These were in Draft v1's schema but are **unimplementable against the current runtime** (the run_command allowlist is a struct field baked at registry-build; `runtime.Request.Permission` is dead code; per-role completers need credential-scope decisions). Keeping them out of v1 prevents shipping a schema whose fields silently no-op. They are listed in §3 as **reserved/ignored** so the TOML shape is forward-compatible.

> **Tool-inheritance stance: ALLOW-BY-DEFAULT (convenience).** Per owner direction, a role with no `tool_allowlist` **inherits the full resolved pool** (minus mandatory denylist + its own denylist). This prioritizes ergonomics — define a role with just a `system_prompt` and it works — over the stricter deny-by-default stance. The security compensations that make this safe are kept: the **non-overridable mandatory denylist**, **monotonic tool narrowing per `can_spawn` edge** (a child can never exceed its spawner), **`can_spawn` deny-by-default**, and **`fail_on_empty_toolset`**. The one residual risk — a denylist-only role silently gaining a *new* tool added globally in a future release — is documented in §10.9 as an accepted tradeoff; users who want strictness can still set `tool_allowlist` per role.

> **Note on the plan-as-file model.** The ADLC rule (`.ai/rules/05-adlc-agentic-development-lifecycle.md`) states a "zero files" storage model. This `.ai/plans/*.md` is a deliberate persistent-record exception, consistent with the established `.ai/plans/` directory (`ALLOWLIST-REFACTOR-PLAN.md`, `ZAI-GLM-PROVIDER-ADAPTER-PLAN.md`, etc.). The in-context Step 0 challenge still runs via orchestrated challenger agents; this file is the durable record only.

---

## 1. Summary & goal

Today mivia-agent has **exactly one** agent shape: the root agent's behavior is hardcoded (`defaultAgentPrompt`/`buildAgentPrompt` in `internal/cli/prompt.go`), tools are scoped **globally** at registry build time (`tools.NewDefaultRegistry`), and **every subagent shares one** `config.SubagentConfig` (one prompt, one tool policy, set once at dispatcher construction in `internal/cli/dispatcher.go`). There is no concept of "roles," "personas," or "profiles" anywhere (verified by grep).

**Goal:** let a user declare named roles like `researcher` (read-only), `engineer` (full edit), `reviewer` (read-only + audit), and assign a role to a task — so the spawned agent runs with that role's prompt + scoped tools + skills, not the shared default.

### Design thesis (grounded in cross-framework research)

Surveyed OpenAI Agents SDK, Claude Code subagents, Roo Code modes, Google ADK, CrewAI, AutoGen. The convergent model for an agent role is: **identity + system prompt + tool set + skill set + model + routing hint + delegation rules**. The two most battle-tested decisions to adopt:

1. **Claude Code's tool evaluation order:** `mandatory_denylist` (non-overridable) → `tool_denylist` (pre-filter over inherited pool) → `tool_allowlist` (post-filter, intersect). Deny-by-default when an allowlist is set; inherit-pool when unset.
2. **Monotonic-narrowing invariant (security-critical):** a spawned role's *effective* tool set ⊆ intersection of (its declared set, spawning role's effective set). A child **must never exceed** its parent's effective tools. This prevents the #1 risk: privilege escalation via subagent inheritance.

**Delegation primitive:** *call-as-tool* (parent stays in control, child gets scoped prompt, returns a value) — matches the existing `[subagents]` fan-out model. *Handoff* (control transfer) is explicitly out of scope for v1 (§10).

**Selection model:** LLM-routed (the parent reads each role's `description` and decides which to spawn) + explicit escape hatch (`--agent <name>` and `@"name"` task syntax).

---

## 2. Current architecture (recap, from codebase audit)

| Concern | Location | Current behavior |
|---------|----------|------------------|
| Agent loop | `internal/agent/loop.go:69-73` | `Loop{Completer, Tools *Registry, Messages}` — tiny, stateless. System prompt seeded via `Messages[0]` by caller. **Cleanly separable — good.** |
| Per-run knobs | `internal/agent/loop.go:41-67` (`Options`) | Model, limits, dispatcher, event bus. **Where per-role overrides land** (or via a higher `Role` type that produces `Options`+system msg). |
| Tool registry | `internal/tools/tools.go:51-54` | `Registry{order, by}`. Built **once globally** via `NewDefaultRegistry` (`tools.go:382-475`). |
| Tool allow/block | `tools.go:404-421` | `DefaultAllowlist` + `RunAllowlist`/`RunAllowlistOnly`/`RunBlocklist` — but these scope **`run_command` argv**, *not* which agent tools are exposed. `DisableTools` is global. **No per-agent scoping exists.** |
| Subagent spawn | `internal/subagents/multi_step.go:187-220` | `setupAgentLoop` builds a **new `agent.Loop`** per invocation; `restrictedRegistry()` copies the parent pool **minus a hardcoded blocklist** (`delegate`, `dispatch_tasks`, `spawn_agent`, …). System prompt from `h.SystemPrompt` with a hardcoded fallback. **This is the primary refactor site.** |
| Dispatcher allow gate | `internal/runtime/dispatcher.go:56-61` | `Policy.Allow map[Kind]map[string]bool` — per-Kind+name allowlist for Tool/Skill/Subagent invocation. Set globally at construction. **An existing gate a per-role system can layer on.** |
| System prompt | `internal/cli/prompt.go:85-176` | `buildAgentPrompt` → `loadAgentPrompt` (`.ai/agent-prompt.md` or fallback). One root prompt; subagents share `cfg.SystemPrompt`. |
| Skills | `internal/skills/skills.go:13-66` | `Definition{Name,Tools,...}`. `Select` enforces a skill's declared `Tools` ⊆ available — closest existing thing to a tool gate, keyed on skill. Loaded globally (`loader.go`), all registered for any caller (`dispatcher.go:146-157`). **Not scoped per role.** |
| Config | `internal/config/types.go:5-13` | `File{Provider, Providers, Chat, Subagents, Tools, Privacy, Integrations}`. **No `[[agents]]` / `[agents.*]` section.** |
| Task→handler binding | `internal/cli/dispatch.go:199-233` (`buildTasks`) | A task's `Name` = handler name (default `"multi_step"`) or a skill name. **This is the extension point** for "task → role." |
| Consumers of `Loop` | `internal/chat/session.go:249`, `subagents/multi_step.go:200` | Root session + subagents. |

**File-size baselines** (`.ai/policy/go-structure.json`, soft 500 / hard 800): `internal/agent/*` all under 400 (headroom). `internal/tools/tools.go` is **571** (over soft — must NOT grow; new tool-scoping logic → new file). `internal/cli/orchestrate.go` ~521 (near soft — careful). `internal/cli/prompt.go` 203, `config/types.go` 149, `config/load.go` 279 (room).

---

## 3. Proposed config schema (TOML)

Extends the existing `[provider]`/`[providers.*]`/`[tools]`/`[subagents]`/`[chat]` sections. New: a `[agents]` block with a `default` base profile + `[[agents.roles]]` array + `[agents.guardrails]`.

```toml
# --- Declarative agent roles (a "team") ---
[agents.default]                 # base profile every role inherits unless overridden
# provider = "deepseek"          # optional: references a [providers.*] table
# model    = "glm-5.2"
system_prompt    = "You are mivia, a local CLI coding agent."
tool_denylist    = []           # remove from inherited pool (pre-filter)
# tool_allowlist = []           # keep only these (post-filter); unset => inherit pool
skills           = []           # skill names preloaded into this role's context
permission_mode  = "default"    # default | accept_edits | plan | auto | dont_ask
max_depth        = 3            # how deep this role may spawn sub-roles

[[agents.roles]]
name        = "researcher"      # slug; required; unique
title       = "Research Agent"  # display name (optional)
description = "Use for codebase exploration, locating code, answering 'where is X'."  # routing hint
inherits    = "default"         # single-inheritance composition (optional)
system_prompt = '''You are a read-only research subagent. Search, read, summarize. Never edit.'''
# provider/model: RESERVED in v1 (ignored on spawned roles — see v1 scope boundary).
provider    = "zai"
model       = "glm-4.5-air"
tool_allowlist = ["read_file", "grep", "glob", "list_dir"]   # explicit, minimal
skills         = []
can_spawn      = []             # DEFAULT deny: len==0 (unset or []) ⇒ cannot spawn
max_depth      = 0

[[agents.roles]]
name        = "engineer"
title       = "Implementation Engineer"
description = "Use to make code changes, run builds and tests."
inherits    = "default"
# No tool_allowlist ⇒ inherits the full default pool (allow-by-default stance).
can_spawn   = ["researcher", "test-runner"]   # bounded delegation graph
max_depth   = 2

[[agents.roles]]
name        = "reviewer"
title       = "Code Reviewer"
description = "Use to review a diff for quality, security, best practices."
inherits    = "default"
tool_allowlist = ["read_file", "grep", "glob"]   # monotonic narrowing of default
tool_denylist  = ["run_command", "write_file", "search_replace"]  # belt-and-suspenders
can_spawn      = []

[[agents.roles]]
name        = "test-runner"
title       = "Test Runner"
description = "Use to run the build/test/lint suite and report results."
inherits    = "default"
tool_allowlist = ["run_command"]
# NOTE: per-role run_program_allowlist is RESERVED in v1 (unimplementable today —
# runCommandTool.allowlist is a struct field baked at registry-build; ScopedRegistry
# only filters tool exposure, NOT argv). The global [tools] run_allowlist governs
# run_command for ALL roles in v1. See §10.7 for the deferred implementation.
can_spawn      = []
max_depth      = 0
max_turns      = 8

[agents.guardrails]
# Names MUST match real Tool.Name() (validated against the global registry at CLI
# layer — see §5). NOTE: the real inspect tool is "inspect_agents" (PLURAL).
mandatory_tool_denylist  = ["delegate", "dispatch_tasks", "spawn_agent",
                            "inspect_agents", "join_run", "cancel_run"]
fail_on_empty_toolset    = true                 # refuse to load a role resolving to no tools
enforce_monotonic_narrowing = true              # per-edge: child tools ⊆ spawner's effective tools (load-time hard error)
# NOTE: no require_allowlist_for_non_root — v1 is ALLOW-BY-DEFAULT (convenience).
# A role without tool_allowlist inherits the full resolved pool. See §7.5/§10.9.
```

> **`permission_mode` is NOT in the v1 schema.** It maps to nothing in the runtime today (`runtime.Request.Permission` at `internal/runtime/dispatcher.go:32` is dead code — never read). It is omitted from v1 rather than shipped as a silent no-op. It returns when runtime support exists (§10.3).

### Field semantics

| Field | Type | Default | Meaning |
|-------|------|---------|---------|
| `name` | string | required | Slug; unique; referenced by `--agent`, task `role`/`Name`, `can_spawn`. Must not collide with a handler or skill name (load-time error). |
| `title` | string | = name | Display name for TUI. |
| `description` | string | "" | **Routing hint** the parent LLM reads to pick a role (distinct from system prompt). Runtime user-config (rule-60-exempt); may be project-specific. |
| `inherits` | string | "default" | Single-inheritance base profile; resolved transitively at load time (cycle = error). |
| `system_prompt` | string | inherited | The role's system instruction. Runtime user-config (rule-60-exempt). (Later: `system_prompt_file`.) |
| `provider` / `model` | string | — | **RESERVED in v1** — ignored on spawned roles (run on spawner's completer). May be honored for the root `--agent` session only. See §10.8. |
| `tool_allowlist` | []string | unset | Post-filter; intersect with spawner's effective pool. **Unset ⇒ inherit full resolved pool** (allow-by-default). Set this to restrict a role (e.g. read-only `researcher`). |
| `tool_denylist` | []string | [] | Pre-filter over inherited pool. |
| `skills` | []string | [] | Skill names to preload into the role's context (capability surface). Each skill's declared `Tools` must be ⊆ role's effective tools (load-time gate, reusing `skills.Select`). |
| `can_spawn` | []string | unset | Allowed child role names. **DEFAULT DENY:** `len(can_spawn)==0` (whether unset or `[]`) ⇒ cannot spawn. Implementation MUST test `len()==0`, never `!= nil`. |
| `max_depth` / `max_turns` | int | inherited / 0(unlimited) | Spawn depth / turn cap. |
| `run_program_allowlist/blocklist` | — | — | **RESERVED in v1** (not in schema). Unimplementable today; returns with per-role registries in §10.7. |
| `permission_mode` | — | — | **NOT in v1** (no runtime hook). |

### Tool evaluation order (per role, at resolution time)
```
effective = spawnerEffectivePool or GlobalRegistry           # global only for root
effective = effective - mandatory_tool_denylist              # 1) non-overridable floor
effective = effective - role.tool_denylist                   # 2) pre-filter
if role.tool_allowlist != nil:
    effective = effective ∩ role.tool_allowlist              # 3) post-filter (intersect)
# monotonic-narrowing check (guardrails.enforce_monotonic_narrowing):
#   checked PER can_spawn EDGE against THAT EDGE'S spawner's resolved EffectiveTools
#   (not against agents.default). Direct-edge checks SUFFICE (subset is transitive),
#   so no transitive closure is needed. Runs on RESOLVED sets (post-inheritance,
#   post-mandatory-denylist). Violation ⇒ load-time error.
```

---

## 4. New Go types & packages

### `internal/roles/` (NEW package) — `role.go`, `resolve.go`, `registry.go`

Keeps role logic out of `config/` (parsing) and `tools/` (registries). ~3 small files.

```go
// role.go
package roles

type Role struct {
    Name, Title, Description      string
    Inherits                      string
    SystemPrompt                  string
    Provider, Model               string
    ToolAllowlist, ToolDenylist   []string
    Skills                        []string
    CanSpawn                      []string
    PermissionMode                string
    MaxDepth, MaxTurns            int
    RunProgramAllowlist           []string
    RunProgramBlocklist           []string
}

type Guardrails struct {
    MandatoryToolDenylist         []string
    FailOnEmptyToolset            bool
    EnforceMonotonicNarrowing     bool
}

type Spec struct {          // parsed from TOML, pre-resolution
    Default Role
    Roles   []Role
    Guardrails Guardrails
}
```

```go
// resolve.go — resolves inheritance + tool sets, validates guardrails.
// Returns a map[name]ResolvedRole (effective tools already computed).
// IMPORTANT: Resolve validates only STRUCTURAL invariants at config-load time
// (inheritance cycles, monotonic-narrowing per-edge, non-empty toolset, can_spawn
// target existence). It CANNOT validate that allow/deny names are real tools —
// the tools.Registry is not built until the CLI layer. Name-reality validation
// happens in the CLI layer via roles.ValidateAgainstRegistry (see §5).
// Decomposed into sub-functions to stay under the 80-LOC function soft limit:
//   resolveInheritance, applyMandatory, applyDeny, applyAllow,
//   checkMonotonicNarrowing (per-edge), checkEmptyToolset, checkCanSpawnTargets.
type ResolvedRole struct {
    Role
    EffectiveTools []string  // after full evaluation order; strings only (no *Registry)
}
func Resolve(spec Spec) (map[string]*ResolvedRole, error)   // NOTE: no globalToolNames arg
func ValidateAgainstRegistry(reg *tools.Registry, roles map[string]*ResolvedRole) error
```

```go
// registry.go — name→ResolvedRole lookup; used by dispatcher + CLI.
type Registry struct{ roles map[string]*ResolvedRole }
func NewRegistry(resolved map[string]*ResolvedRole) *Registry
func (r *Registry) Get(name string) (*ResolvedRole, bool)
func (r *Registry) Names() []string
```

### `internal/tools/scope.go` (NEW file — `tools.go` is at 571 LOC, must not grow)

```go
// ScopedRegistry builds a Registry from a base registry by re-exposing ONLY the
// Tool instances whose names are in `keep`. It filters TOOL EXPOSURE only.
// It does NOT and CANNOT reconfigure runCommandTool.allowlist (a struct field
// baked at registry-build — see §10.7). keep = the resolved EffectiveTools set.
func ScopedRegistry(base *Registry, keep []string) (*Registry, error)
```
`keep` is the already-resolved `EffectiveTools` set from `roles.Resolve`. **Scope note:** this replaces `restrictedRegistry()` in `subagents/multi_step.go` exactly (filter by `Tool.Name()`), and — like the current code — does **not** touch `run_command` argv scoping. Per-role argv scoping is out of v1 scope (§10.7).

---

## 5. Integration points (the wiring)

| Site | File | Change |
|------|------|--------|
| Config parsing | `internal/config/types.go` (+ new `roles.go`) | Add `Agents AgentsSection` to `File`. `type AgentsSection struct { Default roles.Role; Roles []roles.Role; Guardrails roles.Guardrails }` with TOML tags. |
| Config resolution | `internal/config/load.go` (`Load`) | After loading, call `roles.Resolve(spec)` (structural checks only) → build `roles.Registry`; attach to `config.Resolved` (new field `Roles *roles.Registry`). **Cannot validate tool names here — registry not built yet.** |
| **Tool-name reality check (CLI layer)** | `internal/cli/chat_repl.go` after `configureChatWorkspace` (line ~132) | **NEW step:** call `roles.ValidateAgainstRegistry(reg, res.Roles.All())` immediately after `tools.NewDefaultRegistry`. This is the ONLY place every `tool_allowlist`/`tool_denylist`/`mandatory_tool_denylist` entry can be checked against real `Tool.Name()` values. (Findings: config-load validation is impossible; this closes the gap.) |
| Subagent spawn | `internal/subagents/multi_step.go:187-220` | `restrictedRegistry()` → `tools.ScopedRegistry(h.FullRegistry, role.EffectiveTools)`. `h.SystemPrompt` → `role.SystemPrompt` when a role is bound. **The hardcoded blocklist moves to `[agents.guardrails].mandatory_tool_denylist` — and the live `inspect_agent` typo is FIXED to `inspect_agents` (plural).** |
| Handler construction | `internal/cli/dispatcher.go:120-144` (`registerMultiStepHandler`) | Loop over `roles.Registry.Names()` and register one `MultiStepHandler` per role under the role name (Kind=Subagent). Load-time: reject name collisions with existing handler/skill names. (See §6.) |
| Task→role binding (`dispatch_tasks`) | `internal/cli/dispatch.go:199-233` (`buildTasks`) | Task `handler` (defaults `"multi_step"`) resolves role OR handler/skill. **Add explicit `role` JSON field** as the primary binding (cleaner than `Name` overloading — sidesteps §10.1 namespace collision). Precedence: `role` > `handler` > default. Back-compat: no `role`/`handler` ⇒ `multi_step`. |
| Task→role binding (`spawn_agent`) | `internal/cli/orchestrate.go:261-308` | `spawn_agent` task field is `name` (NOT `handler`) with **no default** (`orchestrate.go:300`). Reconcile: add the same `role` field; if `name` is empty and `role` set, use `role`. Document this asymmetry. |
| Root session role | `internal/cli/chat_repl.go:21-89` (`runChat`) | New `--agent <name>` flag (parsed via `flagValue`, like `--provider` at `chat_repl.go:23` — NOT the `chatFlags` switch). Sequence: parse flag → resolve role → override system prompt at `chat_repl.go:41-47` → apply `ScopedRegistry` to `sess.Tools` **between** `configureChatWorkspace` (line 58) and `attachSessionDispatcher` (line 63), because `attachSessionDispatcher` captures `reg` into `MultiStepHandler.FullRegistry`. |
| System prompt | `internal/cli/prompt.go` | `loadAgentPrompt` gains a role parameter; role's `system_prompt` short-circuits the `.ai/agent-prompt.md`/fallback chain for subagents. |
| CLI help | `internal/cli/root.go` | Document `--agent`. |
| Parent tool descriptions | `internal/cli/dispatch.go`, `orchestrate.go` | Inject available role list + `description` into `dispatch_tasks`/`spawn_agent` tool `Description()` at **runtime** (user-config, rule-60-exempt). The **compiled base text** of these `Description()` methods MUST stay project/language-generic (enforced by `internal/tools/generic_surface_test.go`). |

---

## 6. Key design decision: per-role handler instance vs. lookup-at-invoke

Two options for how a role binds to a `MultiStepHandler`:

- **(A) One handler instance per role**, registered under the role name (Kind=Subagent). `roles.Registry` drives a registration loop at dispatcher build. **Pro:** clean, each handler pre-scoped; composes with the existing `dispatcher.Allow[Subagent][name]` gate; the `restrictedRegistry()` replacement is a 1:1 mapping. **Con:** N handler instances; role/handler/skill names share the dispatcher namespace (load-time collision rejection mitigates). For N≤50 roles, memory is trivial (a `Registry` is `{order,by}`, a `MultiStepHandler` is a few pointers).
- **(B) Single `multi_step` handler**, role resolved at `Invoke` time from `runtime.Request` metadata (a new `Role` field). **Pro:** one instance, sidesteps namespace collision entirely. **Con:** every invocation re-resolves + re-scopes; `Request` needs a new field.

**Recommendation: (A) for v1** (small N, clean 1:1 map to the existing `dispatcher.Allow` gate). The `role` task field (§5) is the primary binding, which also sidesteps the namespace collision more cleanly than relying on task `Name` overloading. **Migration trigger to (B):** >50 roles, OR when per-role `provider.Completer` construction lands (§10.8) — then lazy/just-in-time materialization is preferable.

---

## 7. Security & safety (non-negotiables for this refactor)

This refactor introduces a **privilege surface**. The following are hard requirements, not nice-to-haves. *(Substantially strengthened after the security challenge phase — see §11.)*

1. **Monotonic tool narrowing — load-time, per `can_spawn` edge.** Checked **per edge** against **that edge's spawner's** resolved `EffectiveTools` (NOT against `agents.default`). Direct-edge checks **suffice** (subset is transitive/hereditary — if A⊇B and B⊇C on each edge, A⊇C holds automatically; no transitive closure needed). Runs on **resolved** sets (post-inheritance, post-mandatory-denylist). Violation ⇒ load error. Prevents a restricted spawner reaching an over-privileged child.
2. **Mandatory denylist is non-overridable and applied first.** `mandatory_tool_denylist` always runs before any role allowlist, so an allowlist of `["delegate"]` cannot resurrect a blocked tool. Carries the current hardcoded delegation-tool blocklist — with the **`inspect_agent`→`inspect_agents` typo fixed** (current code at `multi_step.go:211` has a live bug; the inspect tool already leaks into subagent registries today). Every `mandatory_tool_denylist` entry is validated against real `Tool.Name()` at the CLI layer (`roles.ValidateAgainstRegistry`).
3. **`can_spawn` is deny-by-default.** `len(can_spawn)==0` (whether unset or `[]`) ⇒ no spawn privilege. Implementation MUST test `len()==0`, never `!= nil` (the classic nil-vs-empty misconfig→privesc). Every entry must name a real role (load-time check).
4. **Empty-toolset refusal.** If `fail_on_empty_toolset=true` and a role resolves to zero tools, fail at load (Claude-Code behavior) — never silently degrade to a tool-less agent.
5. **Tool inheritance is ALLOW-BY-DEFAULT (convenience stance, per owner direction).** A role with no `tool_allowlist` inherits the full resolved pool (minus mandatory denylist + its own denylist) — so a role can be defined with just a `system_prompt` and work. This is a deliberate ergonomics-over-strictness choice. It is safe *within v1* because: the **mandatory denylist** (§7.2) removes delegation tools from every role; **monotonic narrowing** (§7.1) bounds every spawned child to ⊆ its spawner's effective tools; **`can_spawn` deny-by-default** (§7.3) bounds the delegation graph; and **v1 has no other authority axis** (§7.9) — so a role cannot escalate beyond its spawner regardless of how broad its own inherited pool is. Users who want strict per-role tooling still set `tool_allowlist` (e.g. `researcher`, `reviewer`). **Accepted residual:** a denylist-only (or no-list) role silently gains a *new* tool added globally in a future release — see §10.9.
6. **Skills are a capability, gated at load time.** `skills` inject instructions into context. Each preloaded skill's declared `Tools` must be ⊆ the role's effective tools — enforced **at load time (Phase 1)** by reusing `skills.Select` (`internal/skills/skills.go:52-66`), NOT deferred. A skill instructing use of a tool the role lacks is harmless at the tool layer (the tool isn't registered) but the load-time gate fails loud rather than relying on soft prompt-level defense.
7. **No secret/PII in committed role prompts.** Role `system_prompt` is user-authored workspace config (parallel to `.ai/agent-prompt.md`); `make secret-scan` must pass over any committed examples. Rule-60 allows workspace prompts to be project-specific.
8. **Validation is fail-fast, split across two layers.** Structural invariants (cycles, monotonic narrowing, non-empty, can_spawn targets, skill-tool gate) fail in `roles.Resolve` at config-load. **Tool-name reality** (allow/deny/mandatory entries are real `Tool.Name()` values) fails in `roles.ValidateAgainstRegistry` at the CLI layer after the registry is built. Both before any agent runs.
9. **Prompt-injection scope statement (LLM-routed spawning is a confused-deputy surface).** Spawn selection is LLM-routed (parent reads `description`). `can_spawn` + monotonic tool-narrowing mitigate the **tool** axis. They do NOT mitigate prompt-injection-driven spawn selection on any **other** authority axis. **This is why v1 removes the other axes from per-role control** (no per-role `permission_mode`, no per-role argv scoping, no per-role provider/credentials on spawned roles) — so the tool axis is the *only* axis, and narrowing it is sufficient. Documented as a deliberate v1 security boundary.
10. **Generic-surface boundary.** Role `description`/`system_prompt` injected into tool `Description()` at runtime are **user workspace config** (rule-60-exempt, like `.ai/agent-prompt.md`). The **compiled base text** of `spawnAgentTool.Description()`/`dispatchTasksTool.Description()` and any **built-in default role prompts** MUST stay project/language-generic — enforced by the existing `internal/tools/generic_surface_test.go` and `internal/cli/prompt_generic_test.go`. No `cmd/mivia`/`go test`/`github.com/MiviaLabs` strings in compiled defaults.

> **Removed from v1 (moved to §10 as deferred, with reasons):** per-role `run_command` argv scoping (§7.6 in v1 — unimplementable, see §10.7), per-role `permission_mode` narrowing (§7.2b in v1 — no runtime hook, §10.3), per-role provider/credential scope (§10.8). The tool-exposure axis is the sole authority axis in v1, which makes §7.1's narrowing invariant *complete* for v1.

---

## 8. Phased delivery (incremental, each phase ships + verifies)

The full refactor is large; split into independently-shippable phases. **Each phase is its own ADLC cycle + commit(s).** Commit scopes (no new scope; `setup` is prohibited per AGENTS.md):

| Phase | Scope(s) | Rationale |
|-------|----------|-----------|
| 1 | `agent` (roles+config code) + `docs` (toml example + config.md) | `internal/roles/` is agent-role machinery → `agent`. Docs split into a `docs` commit. |
| 2 | `agent` | touches `internal/tools/`, `internal/subagents/` → `agent`. |
| 3 | `cli` (dispatch.go, orchestrate.go) + `agent` (dispatcher.go, runtime) | spans two scopes → split or use dominant `cli`. |
| 4 | `cli` | `internal/cli/`, `internal/chat/`. |
| 5 | `docs` | `docs/`, `mivia.toml.example`. |

### Phase 1 — Config + types + resolution (no runtime change)
- `internal/roles/` package (`role.go`, `resolve.go`, `registry.go`) + tests (decompose `Resolve` into sub-functions to stay <80 LOC each).
- `internal/config/types.go` + `internal/config/roles.go` parsing.
- `config.Resolved.Roles *roles.Registry` populated.
- `roles.ValidateAgainstRegistry` defined (but called in the CLI layer in later phases).
- `mivia.toml.example` + `docs/product/config.md` get the `[agents]` section.
- **Behavior change: NONE.** If no `[agents]` block, `Roles` is empty and all existing paths untouched. `go-toml/v2` ignores unknown keys, and no existing `internal/config/*_test.go` asserts the exact top-level section set.
- Verify: `roles.Resolve` unit tests (cycles, per-edge monotonic-narrowing, empty-toolset refusal, eval order, can_spawn deny-by-default, skill-tool gate); `config` load tests; **NEW invariant** added to `.ai/invariants.md` (see §9).

### Phase 2 — Per-role tool scoping for subagents
- `internal/tools/scope.go` (`ScopedRegistry` — tool-exposure filter only).
- Replace `subagents/multi_step.go:restrictedRegistry()` with `tools.ScopedRegistry`. **Fix the `inspect_agent`→`inspect_agents` typo at `multi_step.go:211` in the same change.**
- `MultiStepHandler` gains an optional `Role *roles.ResolvedRole`; when set, uses role prompt + scoped registry. When nil, current behavior.
- CLI layer calls `roles.ValidateAgainstRegistry(reg, res.Roles.All())` after `configureChatWorkspace` — the authoritative tool-name reality check.
- Verify: subagent tests with a role-bound handler prove scoped tools + the inspect tool is now actually removed; existing subagent tests unchanged (role nil).

### Phase 3 — Task→role binding + dispatcher registration
- `internal/cli/dispatcher.go`: register one `MultiStepHandler` per role (option A); load-time collision rejection.
- `internal/cli/dispatch.go:buildTasks`: add explicit `role` JSON field (primary binding); back-compat `handler`/default preserved.
- `internal/cli/orchestrate.go` (`spawn_agent`): add the same `role` field; reconcile with the `name` (no-default) asymmetry.
- Inject role list + descriptions into tool `Description()` **at runtime** (user-config); keep compiled base text generic (covered by `generic_surface_test.go`).
- Verify: dispatch test spawning a `researcher` role proves scoped tools + role prompt reach the child loop; back-compat test (no role) unchanged; namespace-collision test (role name == skill name ⇒ load error).

### Phase 4 — Root session role + CLI flag
- `internal/cli/chat_repl.go`: `--agent <name>` via `flagValue`; sequence prompt override + `ScopedRegistry` between `configureChatWorkspace` and `attachSessionDispatcher` (§5).
- `internal/cli/prompt.go`: role prompt short-circuits for the root session.
- `internal/cli/root.go`: document `--agent`.
- Verify: end-to-end `mivia chat --agent researcher` runs with read-only tools; `--agent engineer` with full tools.

### Phase 5 — Docs + TOML examples + smoke (still a full ADLC cycle)
- `docs/product/agent.md` (roles/team section, in-place — OWNERS-safe), `docs/product/config.md`, `mivia.toml.example`.
- Manual smoke: define a 3-role team, run a task that fans out, confirm scoping + narrowing holds.
- **Still runs a ≥1-round bug-audit** on the docs/TOML diff (a wrong `mivia.toml.example` is a shipped bug) — NOT "just docs."

**Each phase gated by:** `make verify` + `make test` + `make race` + `make invariants` + `make secret-scan` + `make docs-check`. Bug-audit skill run on each phase diff.

---

## 9. Verification plan (cumulative, per phase)

```text
go build ./...
go test ./internal/roles/...     -race    # NEW package, heaviest tests
go test ./internal/config/...    -race
go test ./internal/tools/...     -race
go test ./internal/subagents/... -race
go test ./internal/cli/...       -race
go test ./internal/agent/...     -race
go vet ./...
make verify          # bundles docs-check, secret-scan, structure-check, semgrep, go-check
make race            # concurrency packages (subagents pool, dispatcher)
make invariants      # ADLC requires when touching internal/config/
```

**Critical acceptance tests (Phase 1–4):**
1. `TestResolve_InheritanceCycle` — error on cyclic `inherits`.
2. `TestResolve_MonotonicNarrowingViolation` — error when a `can_spawn` edge's child EffectiveTools ⊄ spawner's EffectiveTools. **Test is per-edge** (two spawners of different privilege reaching the same child).
3. `TestResolve_EmptyToolsetRefused` — error when `fail_on_empty_toolset` and a role resolves to ∅.
4. `TestResolve_EvalOrder` — mandatory→deny→allow applied in correct order (golden tables); an allowlist entry equal to a mandatory-denylist entry is denied.
5. `TestResolve_CanSpawnDenyByDefault` — unset AND `[]` both ⇒ no spawn privilege.
6. `TestResolve_AllowByDefaultInheritsPool` — a non-root role with no `tool_allowlist` resolves to the full inherited pool (minus mandatory denylist) and loads successfully (the allow-by-default stance).
7. `TestValidateAgainstRegistry_UnknownToolName` — a `tool_allowlist` entry not matching any real `Tool.Name()` ⇒ error (the `readfile` vs `read_file` typo case). **This closes the gap that config-load validation cannot catch.**
8. `TestValidateAgainstRegistry_MandatoryDenylistReal` — every `mandatory_tool_denylist` entry is a real tool name (guards the `inspect_agent`/`inspect_agents` class of typo).
9. `TestScopedRegistry` — produces exactly the resolved tool set; `inspect_agents` is actually removed.
10. `TestMultiStepHandler_RoleScoped` — spawned loop's `Tools` matches role.EffectiveTools; prompt = role prompt.
11. `TestDispatch_RoleBinding` — task with `role:"researcher"` reaches a scoped handler; back-compat task (no role) reaches `multi_step`.
12. `TestRootSession_AgentFlag` — `--agent researcher` yields read-only tool registry in the session.
13. `TestNamespaceCollision` — a role name equal to a skill/handler name ⇒ load error.

**NEW invariant (Phase 1, per ADLC requirement when touching `internal/config/`):** add `INV-AG-7` to `.ai/invariants.md`: *"Agent roles: a spawned role's effective tools are a subset of its spawner's effective tools; violation is a load-time error. Tool inheritance is allow-by-default (a role with no tool_allowlist inherits the full resolved pool)."* Add the test reference; `make validate-invariants` confirms it resolves. The existing invariant suite has no config-layer/role invariant — this fills the gap for the new privilege surface.

**Manual smoke (Phase 5, real keys, not committed):**
```text
# define [agents.default] + [[agents.roles]] researcher/engineer/reviewer in mivia.toml
./mivia chat --agent researcher "edit README.md"   # expect: write_file unavailable / refused
./mivia chat --agent engineer  "edit README.md"    # expect: succeeds
```

---

## 10. Residual risk / open questions (and deferred-to-later scope)

### 10.1 — MEDIUM: namespace collision (role vs handler vs skill names)
Task `role`/`Name` resolves against three namespaces. **Mitigated** by (a) the explicit `role` task field as primary binding (§5), (b) load-time collision rejection (Phase 3), (c) documented precedence `role > skill > built-in handler`. Residual: an LLM emitting a bare `handler` that coincidentally matches a role name routes to the role — acceptable since roles are explicit user config.

### 10.2 — Informational: idempotency/replay keys include the role name
`coordinator.requestFingerprint` (`internal/coordinator/coordinator.go:335-342`) marshals the whole `Task` including `Name`/role. **Renaming a role invalidates prior idempotency keys and breaks resume of in-flight runs** (`ResumeInterruptedRun` re-runs the original name → `unknown subagent`). Not blocking; document as an operational note. The coordinator/Pool themselves are handler-agnostic and per-role handlers are safe for the ledger.

### 10.3 — DEFERRED: `permission_mode` (no runtime hook)
`runtime.Request.Permission` (`internal/runtime/dispatcher.go:32`) is **dead code** — never read; `permission_mode` maps to nothing. v1 **omits** it entirely (not even reserved) rather than ship a silent no-op. Returns when runtime support exists, with a narrowing rule (`rank(child) ≤ rank(spawner)` across `plan ≤ default ≤ accept_edits ≤ auto ≤ dont_ask`).

### 10.4 — DEFERRED: handoff (control transfer)
v1 is call-as-tool only (matches the existing fan-out). Handoff (OpenAI SDK / ADK transfer) would touch the agent loop's control flow — explicitly deferred.

### 10.5 — RESOLVED: skill `Tools` gate timing
Moved to **load time, Phase 1** (§7.6) — not deferred. A role preloading a skill whose declared tools ⊄ role effective tools is a load error.

### 10.6 — Informational: file-size discipline
`internal/cli/orchestrate.go` (521) and `tools.go` (571) are over/at the soft 500 limit with **no maxLines grandfather cap** (verified — not in `.ai/policy/go-structure.json` baseline list). New logic MUST go in new files (`roles/*`, `tools/scope.go`, `config/roles.go`); growing the near-limit files trips the structure gate. Phase 3's runtime description-injection into `orchestrate.go` must stay minimal (a few lines) or move to a helper.

### 10.7 — DEFERRED: per-role `run_command` argv scoping (the big one)
**Why deferred:** `runCommandTool.allowlist` is a `[]string` struct field set **once** at `NewDefaultRegistry` (`internal/tools/tools.go:445-453`) and consumed at `Execute` (`internal/tools/run.go:192-209`). `ScopedRegistry` only filters tool *exposure* — it re-exposes the **same** `*runCommandTool` instance and cannot reconfigure its allowlist. So a v1 `test-runner` role with `run_command` in its `tool_allowlist` runs `run_command` under the **global** `[tools]` program policy, not a per-role one. Shipping a per-role `run_program_allowlist` field that silently no-ops would be a security lie, so it is **out of v1**. **Implementation when it returns:** either (a) build one `*Registry` per role via `NewDefaultRegistry(roleRunOpts)`, or (b) inject a per-request program policy into `runCommandTool.Execute` keyed by the active role, AND-intersected with the global allowlist. Add `TestRunCommand_PerRoleAllowlist` proving `test-runner` cannot run `rm` even when global allows it.

### 10.8 — DEFERRED: per-role `provider`/`model` (credential scope)
**Why deferred:** a spawned role with `provider="zai"` needs its own `provider.Completer` constructed with `ZAI_API_KEY` — a credential the spawning context may have no business exposing, and an axis the tool-narrowing invariant does not cover. **v1 decision: spawned roles IGNORE `provider`/`model` and run on the spawner's completer** (prompt + tools differ; model does not). The fields are **reserved** (accepted, warned) so the TOML shape is forward-compatible. Root `--agent` MAY honor them (single completer, no spawn). When per-role completers return: construct per distinct `(provider,model)` pair, cached, with credentials bounded by the spawning role's privilege — and add a narrowing rule for credential scope.

### 10.9 — ACCEPTED (allow-by-default stance): denylist-only roles silently gain future tools
With the owner-directed allow-by-default stance (§7.5), a role that sets neither `tool_allowlist` (e.g. `engineer`) inherits the full resolved pool. If a future mivia release registers a new powerful tool globally, every such role **silently gains it** with no config change. This is the explicit tradeoff for convenience. Mitigations in place: (a) the **mandatory denylist** still removes delegation tools from every role; (b) **monotonic narrowing** still bounds spawned children ⊆ spawner; (c) the new tool cannot grant *spawn* privilege (`can_spawn` is separate and deny-by-default). Net: a no-list role can do more *itself*, but cannot escalate *delegation*. Users wanting strictness set `tool_allowlist` per role. Flag for review on any release that adds a high-privilege tool (consider extending `mandatory_tool_denylist` then).

---

## 11. Challenge & validation dispositions (ADLC Step 0 record)

Three challenger agents reviewed Draft v1 (codebase correctness, security/safety, ADLC + design soundness). Findings and how each was handled:

### From the codebase-correctness challenger

| Finding | Disposition |
|---------|-------------|
| `New()`/`MultiStepHandler`/`dispatcher` handler map / dependency graph / file sizes — all VERIFIED. | No change (architecture sound). |
| **`inspect_agent` typo (live bug): real tool is `inspect_agents` (plural).** Draft v1 would cement the bug in `mandatory_tool_denylist`. | **FIXED** — corrected to `inspect_agents`; flagged for fix at `multi_step.go:211`; added `ValidateAgainstRegistry` test (#8) to catch this class. |
| **Per-role `run_program_allowlist` is unimplementable** — `ScopedRegistry` only filters tool exposure; `runCommandTool.allowlist` is baked at registry-build. | **FIXED** — removed from v1 schema (reserved); documented fully in §10.7 with the real implementation path. |
| **`permission_mode` maps to nothing** — `runtime.Request.Permission` is dead code. | **FIXED** — removed from v1 schema; documented in §10.3. |
| **Load-time tool-name validation is impossible in `config.Load`** — registry built later in CLI layer. | **FIXED** — `Resolve` does structural checks only; `ValidateAgainstRegistry` runs in the CLI layer after `configureChatWorkspace`; documented in §5 + §7.8. |
| **`spawn_agent` uses `name` (no default), not `handler`** — Draft v1 only addressed `dispatch_tasks`. | **FIXED** — added to §5; reconciled via the explicit `role` field. |
| Idempotency keys include role `Name` — renaming breaks resume. | **FIXED** — documented in §10.2 (informational; not blocking). |
| Root-session wiring sequencing + `--agent` flag parsing underspecified. | **FIXED** — §5 now sequences `ScopedRegistry` between `configureChatWorkspace` and `attachSessionDispatcher`; `--agent` via `flagValue`. |

### From the security/safety challenger

| Finding | Disposition |
|---------|-------------|
| Monotonic-narrowing direction (child ⊆ spawner) is correct. can_spawn deny-by-default intent correct. | Confirmed. |
| **Monotonic narrowing must be per-edge against the spawner (not `agents.default`), on resolved sets; direct edges suffice.** | **FIXED** — §7.1 rewritten precisely; eval-order comment updated. |
| **`permission_mode` widening unguarded.** | **FIXED** — `permission_mode` removed from v1 entirely (§7.9: tool axis is the sole axis in v1, so narrowing is complete). |
| **Denylist-only roles silently inherit future tools.** | **OVERRIDDEN by owner direction** — owner chose allow-by-default convenience. Stance flipped in §7.5; the residual risk is documented as accepted in §10.9 with the mitigations that keep it bounded (mandatory denylist + monotonic narrowing + `can_spawn` deny-by-default). |
| **Prompt-injection spawn selection unmitigated on non-tool axes.** | **FIXED** — §7.9 scope statement: v1 removes all non-tool axes precisely so tool-narrowing is sufficient. |
| **Per-role provider/credential scope ambiguity.** | **FIXED** — §10.8: spawned roles ignore provider/model in v1; explicit decision, not "deferred TBD." |
| `can_spawn` empty-vs-unset nil footgun. | **FIXED** — §3: `len()==0` check mandated. |
| Skill-content-vs-role-tools: load-time gate, not deferred. | **FIXED** — moved to Phase 1 (§7.6). |

### From the ADLC + design-soundness challenger

| Finding | Disposition |
|---------|-------------|
| VERDICT: ADLC COMPLIANT, design SOUND. Phasing is sound decomposition (not a violation). | Confirmed. |
| **Generic-surface boundary unstated** — risk of baking project-specific text into compiled defaults, failing `generic_surface_test.go`. | **FIXED** — §7.10 explicit boundary; §5 notes runtime user-config vs compiled generic text. |
| **No config/roles invariant exists** — `make invariants` runs the suite but `.ai/invariants.md` has no role/narrowing invariant. | **FIXED** — INV-AG-7 added to Phase 1 (§9). |
| Per-phase commit scopes unstated. | **FIXED** — §8 scope table. |
| Phase 5 "just docs + smoke" undersells ADLC Step 6. | **FIXED** — §8 Phase 5 now runs a ≥1-round bug-audit. |
| Single-inheritance right for v1; intersect-on-allowlist defended; one-handler-per-role acceptable for small N; description-injection acceptable for v1. | Confirmed (no change); option-A→B migration trigger documented in §6. |
| Optional: named tool-group presets; pre-decompose `Resolve`. | **Adopted:** `Resolve` decomposition noted in §4. Tool-group presets deferred (YAGNI for v1). |
