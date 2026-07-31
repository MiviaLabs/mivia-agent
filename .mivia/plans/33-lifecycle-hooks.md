# 33 — Lifecycle hooks (deterministic control + observation layer)

**Status:** DESIGN — not yet implemented.
**Date:** 2026-08-02
**Depends on:** `25-skill-triggers` (shipped), the `runtime.Dispatcher.Invoke` gate,
and the `events.Bus` fan-out. **Amends:** none.
**Blast radius:** MEDIUM — a PreToolUse hook can *block* a tool call and change
agent behaviour. Trust is the load-bearing part. No persisted state.

---

## 0. The one correction this plan exists to make

"Triggers" and "hooks" are **not the same thing**, and the question that prompted
this plan — *"do they have triggers in skills like we plan to do?"* — conflates two
layers every major harness keeps deliberately separate. Stating the distinction up
front, because every later decision depends on it:

| | **Triggers** (plan 25, shipped) | **Hooks** (this plan) |
|---|---|---|
| **What it is** | Phrases in `SKILL.md` frontmatter (`triggers:`) | Scripts/config that run **at lifecycle events** |
| **Who reads it** | The **model**, via the tool-surface prompt | The **runtime**, deterministically |
| **Effect** | Influences which skill the model *picks* | Runs code / blocks / injects context *deterministically* |
| **Determinism** | Probabilistic (model may ignore) | Deterministic (runs every time) |
| **Our status** | ✅ Shipped (`INV-AG-17`) | ❌ Does not exist |

Plan 25 made `triggers:` reach the model-facing surface. That is a *selection*
mechanism. It cannot run code, cannot block a tool, cannot react after a write.
That is what hooks are. **This plan does not touch `triggers:`.** The two compose.

The research question — "do Grok / Claude / Codex put triggers *in* skills?" — has
a clean answer: **no.** They put *selection hints* (description, when-to-use) in
skills, and they put *hooks* in a separate lifecycle layer. We already match the
first half. We have no second half. This plan is the second half.

---

## 1. What the field actually does (research summary)

Surveyed three first-party harnesses plus one open-source meta-harness. The shape
converges hard; the differences are in config format and trust.

### 1a. Grok Build (xAI) — `github.com/xai-org/grok-build`, `docs.x.ai/build`

- **Skills** are `SKILL.md` markdown (description + when-to-use + instructions),
  discovered from `.grok/skills/`, `~/.grok/skills/`, plugin dirs, and
  `config.toml` paths. User-invocable skills surface as `/commands`. **No
  `triggers:` key** — selection is by description + the model's judgement.
- **Hooks** are a **separate layer** keyed on lifecycle events. `grok inspect`
  lists discovered "config sources, instructions, skills, plugins, hooks, and MCP
  servers" — five distinct things, listed separately. Hooks fire on SessionStart,
  PreToolUse, PostToolUse, etc. They can run scripts on file edits and tool calls.
- **Plugins** *bundle* skills + hooks + MCP servers + LSP servers as a unit. This
  is the composition model: a plugin is a distribution wrapper, not a new primitive.
- **Config is TOML** (`~/.grok/config.toml`). Skills/hooks/plugins/MCP all live there.
- **ACP headless** (`grok agent stdio`) and `-p` one-shots both run the hook layer.

### 1b. Claude Code — `code.claude.com/docs/en/hooks`

- **Hooks** are the richest of the three. ~15 lifecycle events spanning the full
  agent loop: `SessionStart`, `Setup`, `UserPromptSubmit`, `UserPromptExpansion`,
  `PreToolUse`, `PermissionRequest`, `PermissionDenied`, `PostToolUse`,
  `PostToolUseFailure`, `PostToolBatch`, `SubagentStart`, `SubagentStop`, `Stop`,
  `StopFailure`, `PreCompact`, `PostCompact`, `FileChanged`, `ConfigChange`, plus
  notification/display/elicit/worktree events.
- **Three handler types**: `command` (shell, JSON on stdin), `prompt` (single-turn
  LLM evaluator), `agent` (full subagent with tools). Most production hooks are
  `command`.
- **PreToolUse is the security gate**: can return `decision: approve|block` with a
  `reason`. `block` stops the tool and feeds the reason back to the model.
- **PostToolUse is reactive**: format-on-save, lint, run tests, feed errors back.
  Cannot undo the action.
- **Config is JSON** (`.claude/settings.json` + `.settings.local.json` + enterprise
  managed). Hooks merge *additively* — higher priority does not replace lower; every
  matching hook runs.
- **Matcher**: each event takes a `matcher` (regex on tool name, e.g. `Bash`,
  `Edit|Write`, `mcp__.*`).

### 1c. Codex CLI (OpenAI) — `learn.chatgpt.com/docs/hooks`

- **Hooks** are a direct port of Claude Code's model: same JSON-on-stdin protocol,
  same exit codes, same `hookSpecificOutput` / `additionalContext` shape. Started
  experimental in v0.99.0, **stable as of v0.124.0**.
- **Smaller event set**: `SessionStart`, `SessionEnd`, `UserPromptSubmit`,
  `PreToolUse`, `PostToolUse`, `Stop`, `PreCompact`, `SubagentStart`, `SubagentStop`.
  No prompt/agent handler types — **command only**; `prompt`/`agent` are parsed and
  silently skipped.
- **Config is TOML**, inline in `~/.codex/config.toml` under a `[hooks]` table (and
  `requirements.toml` for admin-managed). No separate hook-scripts file.
- **Trust model is explicit**: `--dangerously-bypass-hook-trust` flag exists precisely
  because untrusted hooks can run arbitrary commands. The TUI has `/hooks` to browse,
  trust, disable. Managed hooks can't be disabled by the user. `PermissionRequest`
  hooks don't fire in `-p` mode → use `PreToolUse` for automated gates.

### 1d. Meta-pattern (GSD `get-shit-done`, community frameworks)

The `gsd-build/get-shit-done` installer projects Claude/Grok skill+hook bundles
across runtimes. Its issue #3603 is the cleanest statement of the cross-harness
contract: skills become `/commands` + subagents; hooks project onto
`SessionStart`/`PreToolUse`/`PostToolUse`. The lesson: **skills and hooks are
distributed together but are different primitives.** A plugin packages both; it does
not merge them.

### 1e. What nobody does

Nobody couples `triggers:` (model selection phrases) to hook execution. The reason
is determinism: a trigger is a *hint to a probabilistic system* (the model), while a
hook is a *guarantee from a deterministic system* (the runtime). Coupling them means
a skill activation depends on model judgement to fire a script — which removes the
one property (determinism) that justifies hooks existing at all.

---

## 2. Where we stand vs. the field

| Capability | Grok | Claude | Codex | **mivia today** |
|---|---|---|---|---|
| Skill selection hints reach model | ✅ (desc) | ✅ (desc) | ✅ (desc) | ✅ (`triggers:`, plan 25) |
| PreToolUse block gate | ✅ | ✅ | ✅ | ❌ |
| PostToolUse reactive run | ✅ | ✅ | ✅ | ❌ |
| SessionStart context inject | ✅ | ✅ | ✅ | ❌ |
| Config format | TOML | JSON | **TOML** | **TOML** (`mivia.toml`) |
| Trust model | (login-gated) | settings tiers | `--bypass-hook-trust` | ❌ (no hooks to trust) |
| `/hooks` browser | via `/plugins` | ✅ | ✅ | ❌ |

We are at parity on selection and config-format; we have **zero** on the
deterministic lifecycle layer. The `events.Bus` and `Dispatcher.Invoke` exist and
are the exact seams this hangs off — so the gap is authoring surface and policy,
not plumbing.

---

## 3. Decision: TOML, not JSON

**Config format: TOML, in `mivia.toml` under a `[hooks]` table.**

Reasoning — this is a deliberate consistency decision, not a copy of any one harness:

1. **Codex already does exactly this.** Codex configs hooks inline in
   `~/.codex/config.toml` under `[hooks]` and it works. There is a shipped
   precedent for the TOML-inline approach in a harness closest to ours.
2. **mivia is a TOML-only product.** `mivia.toml` is the single config source. Every
   other surface (provider, chat, tools, privacy) is TOML. A separate JSON hooks file
   would be the one inconsistency, and it would force a second parser, a second
   discovery path, and a second trust boundary.
3. **The existing frontmatter subset parser (plan 25 §6) is YAML-subset, not TOML.**
   It stays YAML-subset because it parses *skill frontmatter inside markdown*, which
   is a different file kind. Hooks are first-class config, not embedded — they belong
   in the TOML that already governs everything else.
4. **TOML's array-of-tables maps cleanly** to the matcher+handlers shape every
   harness uses. JSON's only argument is "Claude did it that way"; Claude also uses
   JSON for settings everywhere, which we do not.

Rejected alternative — **separate `hooks.json`**: matches Claude Code's shape
literally but breaks the single-config invariant and adds a file-discovery surface
for no benefit. The JSON-in-`.claude` choice is downstream of Claude Code being
JSON-configured end to end; we are not.

### 3a. Proposed TOML shape

```toml
# ~/.mivia/mivia.toml  or  ./.mivia/mivia.toml

# A [[hooks]] table is one matcher group: event + matcher + one or more handlers.
# Project config (.mivia/mivia.toml) and user config (~/.mivia/mivia.toml) merge
# additively — every matching hook runs, project does not override user.

[[hooks]]
event   = "PreToolUse"          # one of the §4 events
matcher = "run_command"          # regex on tool name; "" or absent = match all
trust   = "managed"             # managed | trusted | untrusted (§6)

  [[hooks.handlers]]
  type    = "command"           # command only in v1 (§5)
  run     = "scripts/block-no-verify.sh"   # argv[0]; NOT a shell string
  timeout = 10                  # seconds; default per-event (§7)

[[hooks]]
event   = "PostToolUse"
matcher = "write_file|search_replace"
trust   = "project"

  [[hooks.handlers]]
  type = "command"
  run  = "gofmt -w \"$MIVIA_FILE\""
  timeout = 10

[[hooks]]
event = "SessionStart"
matcher = ""            # all sources

  [[hooks.handlers]]
  type = "command"
  run  = "cat .mivia/context.md"
  timeout = 3
```

`run` is parsed with `shellwords` into an argv and executed via the **existing
allowlist exec path** (`run_command`'s argv model), never `sh -c`. This reuses the
security boundary we already have rather than opening a shell. Substitution tokens
(`$MIVIA_FILE`, `$MIVIA_TOOL`, etc.) are a fixed, documented set — never arbitrary
`$VAR` expansion against the input, to avoid injection via tool arguments.

---

## 4. Event surface — v1 scope

Start small. Claude Code's 15 events are a decade of accretion; Codex shipped with
~6 and grew. **v1 implements the minimum that makes the layer worth having and that
maps to seams we already own.**

| Event | When | Our seam | Can block? | Handler contract |
|---|---|---|---|---|
| `SessionStart` | session begin/resume | `events.KindSessionStart` publish site | No | stdout → injected as session context |
| `PreToolUse` | after reserve, before `execute` | `runtime.Dispatcher.Invoke` (line ~241, post-`allowed` check) | **Yes** | exit 2 / `decision:block` → tool denied, reason fed to model |
| `PostToolUse` | after `execute` returns, before result stored | `Dispatcher.Invoke` tail (after `d.execute`) | No (reactive) | stdout → `additionalContext` fed back to model |
| `Stop` | root loop turn ends | `events.KindTurnEnd` publish site | No | stdout → continuation prompt (model may keep going) |

**Explicitly deferred** (known, documented, not v1): `UserPromptSubmit`,
`PermissionRequest`, `SubagentStart/Stop`, `PreCompact`, `FileChanged`,
`PostToolUseFailure`. Each has a seam but each adds a trust/timeout decision; ship
the four above, then add on demand. The TOML loader must **reject unknown event
names** (hard error, like plan 25 §6's unknown-key rejection) so a typo doesn't
silently disable a hook.

### 4a. Why these four and not Claude's fifteen

- `SessionStart` + `Stop` bracket a session and are pure observation — low risk,
  high value (inject project context, cleanup, cost logging).
- `PreToolUse` is the **only** blocking event and the entire security value prop.
  Every harness agrees it is the one that matters.
- `PostToolUse` is the reactive value prop (format, lint, test). Pairs with
  PreToolUse into the self-correcting loop every guide describes.
- The other eleven are useful but none is load-bearing for a v1. Codex proved a
  ~6-event set is enough to be production-stable.

---

## 5. Handler types — command only in v1

Claude Code has three (command / prompt / agent). **v1 ships `command` only.**

- **`command`**: executes an argv via the allowlist exec path; receives a JSON
  payload on stdin (tool name, input, session id); returns via exit code + stdout.
  Mirrors Claude/Codex's protocol exactly so future skill portability is cheap.
- **`prompt`** (LLM evaluator) and **`agent`** (subagent verifier): **rejected for
  v1.** They are powerful but each is a nested model call with its own cost, timeout,
  and prompt-injection surface. They are the classic speculative-generality trap —
  build them when a concrete hook needs them, not because Claude has them.

The loader **parses but rejects** `type = "prompt"|"agent"` with an explicit error
naming v1's limitation, so a config copied from Claude docs fails loudly rather than
silently no-op'ing (Codex's "silently skip" behaviour is a footgun we should not copy).

---

## 6. Trust model — the load-bearing part

Hooks execute arbitrary user/workspace commands. Every harness treats this as the
primary risk. Codex is the clearest: `--dangerously-bypass-hook-trust` exists
*because* the default is to require trust, and managed hooks can't be user-disabled.

**Proposed trust tiers (TOML `trust` field):**

| Tier | Source | Default behaviour | Override |
|---|---|---|---|
| `managed` | `~/.mivia/managed.toml` (operator/admin) | Always runs; user cannot disable from TUI | — |
| `project` | `./.mivia/mivia.toml` | Runs after **first-confirmation** prompt in interactive mode; in `-p`/headless, runs only if `--trust-project-hooks` passed | `--bypass-hook-trust` skips all |
| `user` | `~/.mivia/mivia.toml` | Runs after first-confirmation; headless requires `--trust-user-hooks` | same |
| `untrusted` | discovered but not yet confirmed | **Does not run.** Listed in `/hooks` as pending. | user promotes via `/hooks trust <id>` |

**Defaults are fail-closed.** An unconfigured or fresh checkout runs **zero** hooks
until the user confirms. This is the opposite of "parse and silently skip." A
`PreToolUse` hook that the user didn't know about is a privilege escalation; the
default must be that it doesn't run.

This composes with our existing `.mivia/policy/agent-hook-bypass.json`, which already
blocks `--no-verify` and hook-bypass env vars at the *agent* layer. That policy is
about the agent not circumventing the **user's** git hooks; this layer is about the
**user's** mivia hooks running at all. Different direction, same principle: hooks are
trust-gated, not auto-run.

### 6a. The bypass flag

Add `--bypass-hook-trust` (mirrors Codex's `--dangerously-bypass-hook-trust`). Intent
is **automation that has already vetted hook sources** (CI). It must never be the
default and must be documented as dangerous. The flag name carries "bypass" so it is
greppable and never reads as a feature.

---

## 7. Timeouts

Per-handler `timeout` in seconds, with per-event defaults matching the field's
character:

| Event | Default timeout | Rationale |
|---|---|---|
| `SessionStart` | 5s | must not block chat startup long |
| `PreToolUse` | 10s | gate; long blocks stall the loop |
| `PostToolUse` | 10s | reactive; Claude/Codex use 10min but our v1 keeps it tight |
| `Stop` | 5s | continuation prompt; must not delay next turn |

A timed-out hook is **killed and reported**, never silently dropped. Its exit feeds
back as a non-blocking warning (not a block). This mirrors our existing
`ToolTimeout` handling in `agent.Options`.

---

## 8. Protocol — the stdin/stdout contract

Reuse the Claude/Codex JSON-on-stdin protocol verbatim where possible, because it is
the de-facto standard and keeps hook scripts portable across harnesses.

**stdin (one JSON object):**
```json
{
  "event": "PreToolUse",
  "tool": "run_command",
  "input": { "argv": ["git", "commit", "-m", "x"] },
  "session_id": "...",
  "turn_id": "...",
  "tool_call_id": "..."
}
```

**control via exit code + stdout:**
- exit `0` → allow / success. stdout (if any) → `additionalContext` for the model.
- exit `2` → **block** (PreToolUse only). stderr → fed to model as the block reason.
- other non-zero → non-blocking warning; stderr shown to user, execution continues.

**structured output (optional, preferred for PreToolUse):**
```json
{ "decision": "block", "reason": "commit uses a hook-bypass flag forbidden by policy" }
```
Parsed from stdout when present; falls back to exit-code semantics otherwise. This is
Claude/Codex's `hookSpecificOutput` shape, trimmed to our v1 fields.

---

## 9. Integration points (concrete, against HEAD)

| Concern | Location | Change |
|---|---|---|
| TOML parse of `[hooks]` | new `internal/hooks/config.go` | parse + validate; reject unknown events/handler-types (§4, §5) |
| PreToolUse gate | `internal/runtime/dispatcher.go:Invoke`, after `if !res.allowed` (line ~247), before `d.execute` | call `hooks.PreInvoke(ctx, req)`; on block, return `fail(blockReason)` without executing |
| PostToolUse reactive | `internal/runtime/dispatcher.go:Invoke`, after `d.execute` returns | call `hooks.PostInvoke(ctx, req, result)`; append `additionalContext` to result |
| SessionStart inject | session startup, where `KindSessionStart` is published | run hooks, concat stdout as system context |
| Stop | where `KindTurnEnd` is published for the root loop | run hooks, append continuation prompt |
| `/hooks` browser | `internal/cli/` slash command (model: `/hooks`) | list events, matchers, trust tier, pending/active; promote untrusted |
| Trust store | `~/.mivia/hook-trust.json` (the **one** JSON file, because it is state not config) | records confirmed hook ids; not in `mivia.toml` |

The trust store is the single justifiable JSON file: it is *runtime state* (which
hooks the user confirmed), not *configuration*. Config is TOML; state may be JSON.
This keeps the "all config is TOML" rule intact while not forcing a state file into
the config format.

### 9a. Dispatcher coupling — keep it optional

`Dispatcher` must not import `internal/hooks` (would create a cycle and force every
test binary to link hooks). Instead, `Dispatcher` gains an optional
`PreInvokeHook`/`PostInvokeHook` **field** (func type); the CLI wiring sets it if
hooks are configured, leaves it nil otherwise. Nil = no hooks = zero overhead,
exactly today's behaviour. This matches how `Policy.Sink` is already an optional
func field on `Dispatcher`.

---

## 10. What this does NOT do (explicit non-goals)

- **Does not couple hooks to skill `triggers:`.** A hook fires on a lifecycle event;
  a trigger influences model selection. They are independent. A future "plugin" may
  ship both in one dir, but the runtime treats them as two things.
- **Does not add prompt/agent handler types.** v1 is command only (§5).
- **Does not implement all 15 Claude events.** v1 is four (§4).
- **Does not make hooks run in `-p`/headless without an explicit trust flag.**
  Fail-closed default (§6).
- **Does not change `triggers:` or plan 25.** That work is shipped and stays.
- **Does not introduce a plugin/marketplace system.** Grok/Claude have plugins; we
  do not need them to ship hooks. Plugins are a later packaging layer.

---

## 11. Threat model (required because hooks run arbitrary code)

| Threat | Mitigation |
|---|---|
| Malicious `.mivia/mvidia.toml` in a cloned repo runs code on `cd` | Project hooks are `untrusted` until confirmed (§6); fresh checkout runs zero hooks |
| Hook script injected via tool argument (e.g. filename `"; rm -rf /`) | `run` is argv-parsed, not `sh -c`; substitution tokens are a fixed set, not `$VAR` expansion |
| Hook disables the agent's own safety (bypass allowlist) | Hooks run **outside** the agent's tool registry; they cannot call agent tools or weaken the dispatcher. The agent's `.mivia/policy/agent-hook-bypass.json` still governs the agent. |
| Hook blocks everything (DoS via PreToolUse) | Per-handler timeout (§7); a blocked tool reports the reason and the model adapts |
| Untrusted hook promoted silently | `/hooks trust` is an explicit user action; managed hooks are operator-set only |

---

## 12. Verification

- `go test ./internal/hooks/...` — config parse (accepted/rejected events, handler
  types, TOML shape), trust resolution, timeout kill
- `go test ./internal/runtime/...` — Dispatcher with PreInvokeHook set: block path
  returns denial without executing; PostInvokeHook appends context; nil hook = no-op
- `go test -race ./...`
- `make verify` — new gate: every `[[hooks]]` event is a known v1 event; unknown ⇒ fail
- Manual: confirm a `PreToolUse` hook on `run_command` blocks a commit that the
  agent-hook-bypass policy forbids, and feeds the reason to the model; confirm an
  unconfigured checkout runs no hooks

## 13. Invariant to register

A new row (next free `INV-AG-29`): *Project/user lifecycle hooks do not execute
until trust is confirmed; the default for a fresh checkout is zero hooks running.
`PreToolUse` is the only blocking event; a block returns a reason that reaches the
model. Hook `run` is argv-executed, never `sh -c`.*

## 14. Rollback criterion

If the Dispatcher hook fields introduce measurable latency on the no-hook path (they
must not — nil check is the fast path), make the fields a single optional
`Hooks` interface with a no-op implementation, so the check is one nil-compare.
If trust UX proves too heavy for interactive use, narrow to "project hooks prompt
once per file hash, not per session" before considering removal. Do not ship hooks
without trust — that is the one decision this plan will not walk back.

## 15. Sequencing

1. `internal/hooks/config.go` + tests — TOML parse, validation, unknown-key reject
2. `internal/hooks/runtime.go` + tests — exec (argv, allowlist path), timeout, stdin/stdout
3. `Dispatcher` optional hook fields + tests — PreInvoke block path, PostInvoke context
4. SessionStart / Stop wiring at the publish sites
5. Trust store (`~/.mivia/hook-trust.json`) + `/hooks` slash command
6. `--bypass-hook-trust` flag + headless trust gating
7. Docs: `docs/development/hooks.md` + `mivia.toml.example` `[hooks]` section
8. Invariant `INV-AG-29` + `make verify` gate

Each step is independently testable. Steps 1–3 are the mechanism; 4–6 are the UX;
7–8 close the loop. Do not land 1–3 without 5–6 — a hook layer with no trust gate is
the defect this plan exists to prevent elsewhere.

---

## Appendix A — why not `hooks.json` (the rejected alternative)

| Argument for JSON | Verdict |
|---|---|
| Claude Code uses `.claude/settings.json` | Claude is JSON-configured end-to-end; we are TOML end-to-end. Not transferable. |
| Codex uses JSON for the stdin/stdout protocol | That is a *wire protocol*, not config. We reuse the JSON wire protocol (§8) while keeping TOML config. The two are unrelated. |
| Hooks feel different from provider config | They are still config: declared, static, version-controlled. They belong in the config format. |
| JSON arrays map to matcher+handlers | TOML `[[hooks.handlers]]` maps identically and is already the idiom in `mivia.toml` (`[[providers.X.models]]`). |

The only JSON file this plan introduces is `~/.mivia/hook-trust.json` — runtime
*state*, not config — and that is the distinction that keeps "all config is TOML"
true. (See §9.)
