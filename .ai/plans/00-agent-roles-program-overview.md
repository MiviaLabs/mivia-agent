# 00 — Agent Roles Program: Overview

**Status:** Program index. Supersedes `AGENT-ROLES-TEAM-REFACTOR-PLAN.md` (deleted; see git history).
**Date:** 2026-07-29
**Scope:** Declarative, named agent **roles** for mivia — each with its own system prompt, scoped tools, and skill access — configurable in **both** `mivia.toml` and markdown agent files.

---

## 1. Why this replaced a single plan

The predecessor was a single 454-line plan carrying a Step-0 challenge record (§11) that certified it correct. A second validation round (4 independent agents + direct verification) found that certification did not hold. The plan was **audited against a pre-`e069064` tree** and rested on premises that are false at HEAD.

**Falsified premises** (each verified directly, see §5):

| Plan claim | Reality |
|---|---|
| "`inspect_agent` typo is a live bug at `multi_step.go:211`" | Already `inspect_agents`; fixed in `be8b7e9`, guarded by `multi_step_test.go:180`. Cited as a live defect in 5 places. |
| "`runtime.Request.Permission` is dead code, never read" | Read and enforced at `internal/skills/skills.go:85` (`skill permission denied`). |
| "`tools.go` is 571 LOC, must not grow" | 377. `NewDefaultRegistry`/`DefaultAllowlist` moved to `internal/tools/default_registry.go`. |
| "`runChat` at `chat_repl.go:21-89`" | `internal/cli/chat_command.go:16-94`. Every `chat_repl.go` line anchor in the wiring section was invalid. |
| "`spawn_agent` at `orchestrate.go:261-308`" | That range is `inspectAgentTool`. `spawnAgentTool.Execute` is `orchestrate.go:131-209`. |
| "Adopts Claude Code's deny-by-default tool evaluation" | Claude Code is **inherit-by-default** (`tools` omitted ⇒ inherits all). The stance was a mivia invention presented as industry-validated. |

**Structurally vacuous machinery** (would have shipped as no-ops — the exact failure the plan claimed to be avoiding):

- **`can_spawn`**: the mandatory denylist removes `delegate`/`dispatch_tasks`/`spawn_agent` from every subagent registry, so no non-root role can ever spawn. The delegation graph had zero traversable edges. The per-edge monotonic-narrowing invariant, `INV-AG-7` as drafted, and two acceptance tests all validated an untraversable graph.
- **`max_depth`**: `subagents.Task.Depth` is never populated by any caller (`dispatch.go`, `orchestrate.go`, `delegate.go`), so the guards at `dispatcher.go:225` and `subagents.go:94` cannot trip across spawn hops.
- **The skill-tool gate**: `internal/skills/loader.go` never populates `Definition.Tools`, so `skills.Select`'s subset loop iterates an empty slice. Vacuously true for every skill.
- **Option (A) justification**: "composes with the existing `dispatcher.Allow` gate" — `Dispatcher.Register` unconditionally sets `Allow[k][name] = true` (`dispatcher.go:162-168`). Registration *is* allowance. There is no per-caller gate to compose with.

**And the finding that reordered the whole program:** tool scoping is currently **advertisement-only**. See §2.

---

## 2. The load-bearing discovery

`agent.Loop.Tools` does not gate execution. It feeds `OpenAITools()` specs and `reg.Capability()` only. Execution goes through `opts.Dispatcher.Invoke(Kind=Tool, Name=<model-supplied string>)` (`loop_tools.go:374`), and `toolHandler` closes over the **full** registry (`runtime/tools.go:11-16`). `MultiStepHandler` passes the **parent's** dispatcher (`multi_step.go:84`).

> **`restrictedRegistry()` has always been a prompt-shaping filter, not a control.** A subagent that emits `spawn_agent` executes it.

Every security guarantee the predecessor plan asserted — mandatory denylist, monotonic narrowing, deny-by-default allowlists — was enforced only against a compliant model. That is a prompt-level defense presented as a privilege boundary, in a design whose stated #1 risk is prompt injection.

**Consequence for sequencing:** plan `01` is not a phase of the roles feature. It is a prerequisite *and* a standalone security fix, and it ships first.

---

## 3. Program invariants

These hold across all plans. Any plan that violates one is wrong.

1. **Enforcement lives at the dispatch boundary, never at the registry.** A registry filter is an advertisement. The authorization boundary is the pairing of a restricted registry with a dispatcher built from that same registry (`01`).
2. **No field ships without an enforcement point.** A schema field that silently no-ops is a security lie. Fields with no runtime hook are *omitted*, not "reserved with a warning", unless their TOML shape is already public.
3. **Configuration may only narrow privilege, never widen it.** TOML over markdown intersects allowlists and unions denylists (`05`). A reviewed agent file is a privilege *upper bound*.
4. **Every guard carries a mutation proof.** Rule 20 requires it and the predecessor plan had none. Each plan lists the mutation, the file, and the test that must then fail.
5. **Compiled surface stays project- and language-generic** (rule 60, `generic_surface_test.go`, `prompt_generic_test.go`). Role prompts and descriptions are workspace user-config; the compiled base text of tool `Description()` and any built-in role prompt is not.
6. **Claims are verified against HEAD.** The predecessor's failure mode was a stale audit blessed by a challenge round. Every file:line in these plans was re-derived on 2026-07-29.

---

## 4. Plan set

Each plan is its own ADLC cycle with its own challenge round, verify gate, and commits.

| # | Plan | Ships alone? | Depends on |
|---|---|---|---|
| `01` | [Dispatch-boundary tool authorization](01-dispatch-boundary-tool-authorization.md) | **yes** — security fix on its own merits | — |
| `02` | [Run-handle ownership](02-run-handle-ownership.md) | **yes** — security fix on its own merits | — |
| `03` | [Agentkit embedded serving](03-agentkit-embedded-serving.md) | **yes** | — (owns the frontmatter parser — see cycle note) |
| `04` | [Workspace namespace `.mivia/`](04-workspace-namespace-mivia.md) | **yes** | `03` |
| `05` | [Role model core](05-role-model-core.md) | no | `01`, `04` |
| `06` | [Role–skill binding](06-role-skill-binding.md) | no | `03`, `05` |
| `07` | [Role routing](07-role-routing.md) | no | `02`, `05` |
| `08` | [Role CLI and observability](08-role-cli-and-observability.md) | no | `07` |
| `09` | [Role docs and examples](09-role-docs-and-examples.md) | no | `02`, `08` |

> **Cycle resolved.** `03` §4c needs the frontmatter parser that `05` §6 specifies, while `05` → `04` → `03`. That is a real cycle (`03 → 05 → 04 → 03`). **Resolution: the parser lands in `03`**, in a shared location `05` then consumes. `03` therefore depends on nothing and the cycle disappears. Do not take `03`'s alternative wording ("or sequence `05`'s parser work first") — it reinstates the cycle.

> **Rule-60 amendment moves to `05`.** `07` §3 schedules the `chore(ai)` amendment for runtime-injected role text, but `05` §4's schema already injects role `description` into `Description()` at runtime — `05` hits rule 60 first. The amendment ships with `05`.

> **Depth propagation is owned by `02`, not `01`.** `01` §5 defers it as a follow-up; `02` §4 delivers it via the `Caller` carrier. If `01` ships second, its §5 follow-up is already done.

**Ordering rationale.** `01` first because every guarantee in `05`–`08` is unenforceable without it. `02` next because roles create multiple principals in one session, which turns a latent cross-run exposure into a live one. `03` and `04` before `05` because a role loader cannot be specified until embedded-vs-workspace precedence and the namespace are settled.

`01` and `02` touch disjoint code and may ship in either order — but **`02`'s ADLC RED gate is only natural if `02` ships first.** `02` §1 concedes that after `01` lands, the exposure narrows to the root agent, so `TestRunHandleNotAccessibleToOtherOwner` can no longer be written against a subagent caller and must use a synthetic second principal that does not exist until `05`. Ship `02` first, or accept a synthetic RED gate and say so in the completion report. Both must precede `05`.

### Cut from the program entirely

`can_spawn`, `max_depth`, `inherits_pool`, per-role `permission_mode`, handoff (control transfer), per-role `provider`. Each is either vacuous today (§1) or has no runtime hook. They return only with a real spawn-permission gate and a per-role completer decision — not as reserved schema.

---

## 5. Verification record

Findings in §1–§2 were produced by four independent validators (codebase correctness, adversarial security, ADLC/rules compliance, design skepticism) and then re-verified directly. Load-bearing confirmations:

| Claim | Evidence |
|---|---|
| Scoping is advertisement-only | `multi_step.go:200-203` + `loop_tools.go:374` + `runtime/tools.go:11-16` + `dispatcher.go:162-168` |
| `can_spawn` vacuous | `multi_step.go:209-213` blocks all three spawn tools |
| Registration is allowance | `dispatcher.go:161-168` |
| Skill `Tools` never populated | `internal/skills/loader.go` — zero matches for `Tools` |
| Frontmatter parser drops lists | `loader.go:107-119` recognizes only `name`/`description`; `.ai/skills/concurrency-review/SKILL.md` `triggers:` is silently dropped today |
| Cross-subagent result leakage | `req.ID = task.call.ID` (model-supplied, `loop_tools.go:375`) keys dispatcher-global `d.completed`/`d.fingerprints` (`dispatcher.go:233-237`) |
| agentkit serving API is dead | `Rule`/`Doctrine`/`Skill`/`Resolve`/`AgentInstructions` — zero production callers; only `EnsureInstructions` is wired (`cmd/mivia/main.go:18`) |

### Industry research (2026-07-29)

- **`.agents/` is not a standard.** [dotagentsprotocol.com](https://dotagentsprotocol.com/) is a single-author draft ("DRAFT · 2026-02-24", author `aj47`) naming no production implementations. [bgreenwell/dotagents](https://github.com/bgreenwell/dotagents) self-describes as *proposed*.
- **`AGENTS.md` is the real standard** (Linux Foundation / Agentic AI Foundation, 60k+ repos, 30+ tools) — but it is an *instructions* file, not agent definitions. mivia already ships one.
- **Agent definitions converged on tool-namespaced dirs**, e.g. [`.claude/agents/*.md`](https://code.claude.com/docs/en/sub-agents) with YAML frontmatter. No neutral directory won. Hence `.mivia/` (`04`).
- **The reference frontmatter schema** (`name`, `description`, `tools`, `disallowedTools`, `model`, `permissionMode`, `maxTurns`, `skills`, `mcpServers`, `hooks`, `memory`, `effort`, `isolation`, `color`, `initialPrompt`) contains **nothing resembling a spawn graph** — independent corroboration for cutting `can_spawn`.
- `tools` **omitted ⇒ inherits all**. Adopted in `05`.

---

## 6. Program-level gates

Every plan runs: `make verify`, `make test`, `make race`, `make invariants`, `make validate-invariants`, `make structure-check`, `make secret-scan`, `make docs-check`.

Skills per ADLC: `engineering-working-contract` throughout, `verify-code-change` after each change, `bug-audit` on each diff, **plus `secure-change` on `01`/`05`/`07` and `concurrency-review` on `01`** (rule 50 skill coupling — the predecessor plan invoked only `bug-audit`).

Blast radius for `01`, `05`, `07` is **high** (security + concurrency) per `verify-code-change`; reports use `mivia-report/v1`.
