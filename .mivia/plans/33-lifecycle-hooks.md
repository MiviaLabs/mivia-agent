# 33 — Lifecycle hooks (deterministic control + observation layer)

**Status:** DESIGN — **blocked on §3b and §6b**; not implementable as written.
**Date:** 2026-08-02 · verified against HEAD and re-researched 2026-08-01
**Depends on:** `25-skill-triggers` (shipped), the `runtime.Dispatcher.Invoke` gate,
the `events.Bus` fan-out, **`05-role-model-core` §5 (`load_workspace_config`, NOT
implemented at HEAD — `grep -rn load_workspace_config internal/` is empty)**, and a
**multi-file config merge** that does not exist today (§3b). **Amends:** none.
**Blast radius:** HIGH, not medium. A PreToolUse hook can *block* a tool call, and a
project-supplied hook is arbitrary code execution triggered by `cd` into a repo. The
original MEDIUM rating was written before §3b was known: workspace config today
*replaces* user config rather than merging with it, so a hostile `.mivia/mivia.toml`
does not merely add hooks — it silently displaces the user's entire configuration.
Trust is the load-bearing part. Persisted state: one trust store (§9).

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

The research question — "do Grok / Claude / Codex put triggers *in* skills?" — needs
a more careful answer than the first draft of this plan gave. The first draft said
"no, nobody couples skills and hooks." **That is wrong, and re-research falsified it:**

- **Claude Code supports `hooks:` in `SKILL.md` frontmatter.** Those hooks are
  registered when the skill loads and torn down when it exits — "scoped to the
  skill's execution and cleaned up on exit." So a *model-selected* skill really does
  cause hook registration.
- **Grok skills "hook into Grok Build's lifecycle events"**, and plugins bundle
  skills + hooks as a distribution unit.

The distinction that *does* survive scrutiny, and that this plan rests on, is
narrower and worth stating precisely:

> A skill may be the **packaging and scoping unit** for a hook. A trigger phrase is
> never the **firing condition** of a hook. Once a skill is active, its hooks fire
> deterministically on lifecycle events — the model chose *whether the hook is
> installed*, never *whether it runs*.

That narrower claim is what makes §10's non-goal defensible: v1 does not read hooks
from `SKILL.md`, because skill-scoped hooks make the *installed hook set* depend on
model judgement, and that interacts with trust (§6) in a way v1 should not take on.
It is a deferral with a known shape, not a claim that the field doesn't do it —
and §10 now says so.

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
- **Two runner kinds — shell *and* HTTP.** Not command-only.
- **Context arrives via environment variables**, not only stdin JSON: every hook gets
  `GROK_HOOK_EVENT`, `GROK_HOOK_NAME`, `GROK_SESSION_ID`, `GROK_WORKSPACE_ROOT`;
  plugin hooks additionally get `GROK_PLUGIN_ROOT`, `GROK_PLUGIN_DATA`. Worth noting
  because §3a's `$MIVIA_FILE` substitution is solving the same problem the field
  solves with plain env vars — see §3c.

### 1b. Claude Code — `code.claude.com/docs/en/hooks`

- **Hooks** are the richest of the three. **~30 lifecycle events** (the first draft
  said 15; recount at the live docs): `SessionStart`, `Setup`, `SessionEnd`,
  `UserPromptSubmit`, `UserPromptExpansion`, `Stop`, `StopFailure`, `PreToolUse`,
  `PostToolUse`, `PostToolUseFailure`, `PermissionRequest`, `PermissionDenied`,
  `PostToolBatch`, `SubagentStart`, `SubagentStop`, `TaskCreated`, `TaskCompleted`,
  `TeammateIdle`, `FileChanged`, `CwdChanged`, `ConfigChange`, `WorktreeCreate`,
  `WorktreeRemove`, `InstructionsLoaded`, `PreCompact`, `PostCompact`,
  `Elicitation`, `ElicitationResult`, `MessageDisplay`, `Notification`.
- **Five handler types**, not three: `command`, **`http`** (POST to a URL),
  **`mcp_tool`** (call a tool on an MCP server), `prompt` (single-turn LLM
  evaluator), `agent` (subagent, experimental). Most production hooks are `command`.
- **PreToolUse is the security gate — but not with the shape the first draft
  assumed.** It returns
  `hookSpecificOutput: { hookEventName: "PreToolUse", permissionDecision:
  "allow"|"deny"|"ask"|"defer", permissionDecisionReason, updatedInput }`.
  The flat `{"decision":"block","reason":...}` shape is the **other** events'
  contract (`PostToolUse`, `Stop`, `UserPromptSubmit`, …), not PreToolUse's. §8 was
  written against the wrong one and is corrected there.
- **`updatedInput` lets a hook rewrite the tool's arguments** before execution. This
  is a capability, and a hazard we must explicitly decline — see §8a.
- **Only exit 0 parses stdout as JSON.** Exit 2 ignores JSON and reads stderr.
- **Default `command` timeout is 600s** (30s for `UserPromptSubmit`, 10s for
  `MessageDisplay`); `prompt` 30s, `agent` 60s; `SessionEnd` shares a 1.5s budget.
- **`disableAllHooks: true`** is the global kill switch; managed hooks survive it.
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
- **Config is BOTH TOML and JSON** — the first draft's "no separate hook-scripts
  file" is wrong. Codex reads inline `[hooks]` tables in `config.toml` **and**
  standalone `hooks.json` files, and *"if a single layer contains both `hooks.json`
  and inline `[hooks]`, Codex merges them and warns at startup."* Admin-managed hooks
  come from `requirements.toml`. This weakens §3's "Codex proves TOML-inline is
  sufficient" argument to "Codex proves TOML-inline *works*" — which is still enough
  for our purposes, but the plan should not overstate it, and Appendix A is corrected.
- **Trust is tracked by content HASH**, not by name or id: "the trust system tracks
  hook definitions by hash," and non-managed command hooks require explicit trust
  review before execution. Managed hooks (system/MDM/`requirements.toml`) are
  auto-trusted. This is the detail §6 should copy and the first draft relegated to a
  rollback fallback in §14 — hash-keying is the *primary* design, not the retreat.
- `--dangerously-bypass-hook-trust` exists precisely because untrusted hooks run
  arbitrary commands. The TUI has `/hooks` to browse, trust, disable.
  `PermissionRequest` hooks don't fire in `-p` mode → use `PreToolUse` for automated
  gates.
- **Timeouts: 600s default**, with `SessionEnd` at 1s default / 3s max.

### 1d. Meta-pattern (GSD `get-shit-done`, community frameworks)

The `gsd-build/get-shit-done` installer projects Claude/Grok skill+hook bundles
across runtimes. Its issue #3603 is the cleanest statement of the cross-harness
contract: skills become `/commands` + subagents; hooks project onto
`SessionStart`/`PreToolUse`/`PostToolUse`. The lesson: **skills and hooks are
distributed together but are different primitives.** A plugin packages both; it does
not merge them.

### 1e. What nobody does (corrected)

The first draft claimed "nobody couples skills and hooks." **Falsified** — Claude
Code's `SKILL.md` frontmatter takes a `hooks:` block scoped to the skill's execution,
and Grok skills hook into lifecycle events. What survives is the narrower and more
useful statement from §0:

- Nobody makes a **trigger phrase the firing condition** of a hook. Selection decides
  *installation*; the lifecycle event decides *execution*.
- Consequently, a skill-scoped hook is still deterministic **within its scope**: once
  installed, it fires on every matching event until the skill exits.

The reason the narrow version matters: a trigger is a hint to a probabilistic system
(the model), a hook is a guarantee from a deterministic system (the runtime). Letting
the model decide *whether a script runs at all on this tool call* removes the one
property that justifies hooks existing. Letting the model decide *which hook set is
loaded for this task* does not — but it does put model judgement upstream of a trust
decision, which is why v1 declines it (§10).

---

## 2. Where we stand vs. the field

| Capability | Grok | Claude | Codex | **mivia today** |
|---|---|---|---|---|
| Skill selection hints reach model | ✅ (desc) | ✅ (desc) | ✅ (desc) | ✅ (`triggers:`, plan 25) |
| PreToolUse block gate | ✅ | ✅ | ✅ | ❌ |
| PostToolUse reactive run | ✅ | ✅ | ✅ | ❌ |
| SessionStart context inject | ✅ | ✅ | ✅ | ❌ |
| Config format | TOML | JSON | TOML **+ JSON** | **TOML** (`mivia.toml`) |
| Config layering | merged | merged additively | merged per layer | ❌ **first-existing-wins** (§3b) |
| Trust model | (login-gated) | settings tiers | hash-keyed + `--bypass` | ❌ (no hooks to trust) |
| `/hooks` browser | via `/plugins` | ✅ | ✅ | ❌ |

We are at parity on selection and config-format; we have **zero** on the
deterministic lifecycle layer.

**Correction to the first draft's closing claim.** It said "the gap is authoring
surface and policy, not plumbing." Verification at HEAD shows that is false in three
places, each of which is real work this plan must own rather than assume:

1. **`KindSessionStart` is never published.** It is declared at
   `internal/events/event.go:22` and listed in `allKnownKinds`
   (`internal/events/metrics.go:48`), and that is the whole of its existence — no
   `Publish` call anywhere in `internal/`. §9's "where `KindSessionStart` is
   published" points at a seam that does not exist. `KindTurnEnd` *is* published
   (`internal/cli/tui_events.go:78`), so `Stop` has a real seam and `SessionStart`
   does not.
2. **Config does not merge across layers** — see §3b. There is no user+project
   layering to hang a trust tier on.
3. **`load_workspace_config` is not implemented.** Plan `05` §5 designs it;
   `grep -rn load_workspace_config internal/` returns nothing. The gate this plan
   needs to lean on is itself unshipped.

`Dispatcher.Invoke` is a genuine seam and is the one thing the first draft got right
about plumbing.

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
# ~/.mivia/mivia.toml  — user config only in v1. See §3b: project hooks are NOT
# read from ./.mivia/mivia.toml, because that file today REPLACES this one.

# A [[hooks]] table is one matcher group: event + matcher + one or more handlers.
# There is deliberately NO `trust` key here — see §6b. A file cannot declare its
# own trust tier; trust is derived from which file the hook came from, plus the
# content hash recorded in the trust store.

[[hooks]]
event   = "PreToolUse"           # one of the §4 events
matcher = "run_command"          # regex on tool name; "" or absent = match all

  [[hooks.handlers]]
  type       = "command"         # command only in v1 (§5)
  argv       = ["./hooks/block-no-verify.sh"]   # explicit argv; NOT a shell string
  timeout    = 10                # seconds; default per-event (§7)
  on_timeout = "block"           # block | allow — PreToolUse defaults to block (§7)

[[hooks]]
event   = "PostToolUse"
matcher = "write_file|search_replace"

  [[hooks.handlers]]
  type    = "command"
  argv    = ["./hooks/gofmt-changed.sh"]   # reads MIVIA_FILE from the environment
  timeout = 10
```

Note what changed from the first draft and why — each was a defect, not a style
preference:

- **`run = "gofmt -w \"$MIVIA_FILE\""` → `argv = [...]`.** A single string that must
  be "parsed with `shellwords`" is a shell-command-shaped field that only *pretends*
  not to be a shell. `argv` as a TOML array removes the parsing step and therefore
  the class of quoting bugs it would introduce. This also matches `run_command`'s own
  contract, which takes `argv` and explicitly refuses shell strings.
- **Substitution tokens are gone.** `$MIVIA_FILE` interpolation into an argv is
  string-splicing model-controlled data into a command line — the exact shape §11
  claims to have designed out. Context reaches the hook the way Grok does it: as
  **environment variables** (`MIVIA_HOOK_EVENT`, `MIVIA_TOOL`, `MIVIA_FILE`,
  `MIVIA_SESSION_ID`, `MIVIA_WORKSPACE_ROOT`) and as the stdin JSON of §8. A value
  passed through the environment is never re-parsed as syntax.
- **The `trust` key is gone** — §6b.
- **The `SessionStart` example is gone** — that event has no publish site (§2), so
  v1 cannot ship it. See the revised §4.

### 3b. BLOCKER — mivia config does not merge; a project file *replaces* the user file

The first draft's central assumption is stated in the old §3a comment: *"Project
config and user config merge additively — every matching hook runs, project does not
override user."* **This is false at HEAD, and it is the reason this plan is blocked.**

`config.DefaultConfigCandidates()` (`internal/config/paths.go:47-60`) returns
`$MIVIA_CONFIG`, then `<cwd>/.mivia/mivia.toml`, then `~/.mivia/mivia.toml` — and
`Load` takes `FirstExisting` of that list (`internal/config/load.go:308`). Exactly
**one** file is read. There is no merge layer of any kind.

Two consequences:

1. **The trust tiers in §6 have no substrate.** `managed` / `project` / `user` tiers
   presuppose that three files co-load and their hooks concatenate. Today the
   presence of `./.mivia/mivia.toml` means `~/.mivia/mivia.toml` is *never opened*.
   A "project hook that runs alongside the user's hooks" cannot be expressed.
2. **The pre-existing shadowing hazard becomes executable.** Today a cloned repo
   shipping `.mivia/mivia.toml` already silently replaces the user's provider,
   privacy, and tool configuration — bad, but inert. Adding `[[hooks]]` to that file
   turns a config-shadowing bug into arbitrary code execution on `cd`.

**Resolution — v1 reads hooks from user config only.** Hooks are loaded exclusively
from `config.UserConfigPath()` (`~/.mivia/mivia.toml`), read **at its fixed path**,
never via `config.Load`. This is precisely the mechanism plan `05` §5 already
established for `load_workspace_config` and for the same reason. If the resolved
config file is a workspace file, any `[[hooks]]` table in it is **stripped with a
warning**, exactly as `05` §5 strips workspace `[chat].system_prompt`.

Project-supplied hooks are **deferred to a follow-up plan** whose first requirement
is a real config merge layer. That is a larger, independently valuable change
(`[[providers]]`, `[tools]`, and `[agents.roles]` all want it too) and it does not
belong inside a hooks plan. Shipping user-only hooks now is a complete, useful
feature: it covers the whole "my machine, my policy" use case, which is what
`PreToolUse` gating and format-on-write are actually for.

### 3c. Why hooks cannot reuse `run_command`'s exec path

The first draft says `run` is "executed via the **existing allowlist exec path**
(`run_command`'s argv model)… This reuses the security boundary we already have."
Read against `internal/tools/run.go`, that path structurally rejects every hook the
plan wants to write:

| `run_command` behaviour | Where | Effect on hooks |
|---|---|---|
| `argv[0]` containing `/` or `\` is refused: *"program must be a bare name on the allowlist, not a path"* | `run.go:207-211` | `./hooks/block-no-verify.sh` — the plan's own example — cannot run |
| `argv[0]` must be on the run allowlist, then `exec.LookPath` | `run.go:212-220` | a hook script is not an allowlisted *program*; adding hook scripts to `run_allowlist` would also hand the **model** the ability to invoke them |
| `cmd.Dir = t.ws.Abs` | `run.go:110` | a hook can only ever run at workspace root |
| `secretPathInArgv` refuses secret-like paths | `run.go:86-92` | a hook reading `~/.mivia/…` is blocked |
| output is `redact.Text`'d and wrapped in a `command:/cwd:/exit=` header | `run.go:143-149` | the header would be parsed as the hook's JSON stdout |

So hooks need **their own exec path**, and the plan must say so plainly rather than
claim a reuse that does not typecheck. That path's rules, stated as a contract:

1. `argv[0]` is a **filesystem path**, resolved relative to the directory of the
   config file that declared the hook; absolute paths allowed. **No `PATH` lookup**
   — a hook must never resolve to a different binary because `PATH` changed.
2. **No shell, ever.** `exec.CommandContext(ctx, argv[0], argv[1:]...)`; no `sh -c`,
   no `shellwords`, no interpolation.
3. Working directory is the workspace root; environment is the filtered env
   (`filterEnv`) plus the fixed `MIVIA_*` set.
4. stdout/stderr are captured under an explicit byte bound (§9b) and are **not**
   redacted or reformatted — they are a machine protocol (§8), not model-visible
   tool output.
5. **Hooks never re-enter the dispatcher** (§11a). They are out-of-band process
   execution, invisible to the tool registry and to the model.

This is *more* security surface than the first draft admitted, not less. It is
justified only by the trust gate in §6 — which is why §15 forbids landing the
mechanism without it.

---

## 4. Event surface — v1 scope

Start small. Claude Code's 15 events are a decade of accretion; Codex shipped with
~6 and grew. **v1 implements the minimum that makes the layer worth having and that
maps to seams we already own.**

| Event | When | Our seam | Can block? | Handler contract |
|---|---|---|---|---|
| `PreToolUse` | after `reserve`, before `execute` | `dispatcher.go:251-254`, between the `!res.allowed` check and `d.execute` | **Yes** | exit 2 / `permissionDecision:"deny"` → tool denied, reason fed to model |
| `PostToolUse` | after `d.execute` returns | `dispatcher.go:254` return path | No (reactive) | stdout → bounded `additionalContext` (§9b) |
| `Stop` | root loop turn ends | `KindTurnEnd` publish site, `internal/cli/tui_events.go:78` | No | stdout → continuation prompt |

**`SessionStart` is cut from v1.** The first draft listed it as the low-risk, easy
one; it is in fact the only one of the four with **no seam at all**. `KindSessionStart`
is declared (`internal/events/event.go:22`) and enumerated in `allKnownKinds`
(`internal/events/metrics.go:48`) but is **never published** by any code in
`internal/`. Shipping `SessionStart` means first creating a session-start publish
point and deciding what "session start" means for resume, model-switch (which rebuilds
the dispatcher generation, INV-AG-28), and headless one-shots. That is its own change.
Introducing a context-injection surface *and* its first publisher in the same plan is
how an injection path ships untested. Add it in a follow-up, once something else needs
`KindSessionStart` published for its own reasons.

Cutting it also removes v1's only *prompt-injection* surface: `SessionStart` stdout is
concatenated into system context, whereas `PostToolUse`/`Stop` stdout enters as model-
visible turn content that is already bounded and attributed. That is a real reduction
in blast radius, not just scope.

**Explicitly deferred** (known, documented, not v1): `SessionStart`, `SessionEnd`,
`UserPromptSubmit`, `PermissionRequest`, `SubagentStart/Stop`, `PreCompact`,
`FileChanged`, `PostToolUseFailure`. The TOML loader must **reject unknown event
names** (hard error, like plan 25 §6's unknown-key rejection) so a typo doesn't
silently disable a hook — and it must reject the deferred names with a message that
says *deferred*, not *unknown*, so a config copied from Claude/Codex docs fails
legibly.

### 4a. Why these three

- `PreToolUse` is the **only** blocking event and the entire security value prop.
  Every harness agrees it is the one that matters.
- `PostToolUse` is the reactive value prop (format, lint, test). Pairs with
  PreToolUse into the self-correcting loop every guide describes.
- `Stop` is pure observation on a seam that already publishes (cost logging, cleanup).
- The rest are useful but none is load-bearing for a v1. Codex proved a ~6-event set
  is enough to be production-stable; three is enough to be *worth having*.

### 4b. Which invocation kinds does "ToolUse" cover?

`Dispatcher.Invoke` is not a tool-only path — it dispatches `Tool`, `Skill`, and
`Subagent` (`dispatcher.go:18-22`). The first draft never said which of these fire
`PreToolUse`, which would have made the answer an implementation accident.

**Decision: `PreToolUse`/`PostToolUse` fire only when `req.Kind == runtime.Tool`.**
An event named `PreToolUse` that also fires on subagent dispatch is a lie in a
security-relevant name, and a `matcher` regex written against tool names would match
subagent names by coincidence. Skill and subagent lifecycle get their own events when
the deferred `SubagentStart/Stop` land.

Two further behaviours to pin with tests, both consequences of `Invoke`'s existing
structure:

- **Deduplicated invocations do not fire hooks.** A repeat `req.ID` returns the
  cached `completed` result from `reserve` (`dispatcher.go:291-293`) and returns at
  `dispatcher.go:240-247`, before the hook point. Correct — the tool did not run — but
  it must be asserted, or a hook author will assume one fire per model tool call.
- **A block happens after `reserve` has already charged the budget**
  (`dispatcher.go:305`) and installed the active marker. The deferred cleanup at
  `dispatcher.go:234` still runs, and `failResult` still delivers to waiters
  (`dispatcher.go:462-467`), so a blocked call does not wedge a duplicate waiter.
  Pin it; it is not obvious from reading the hook code alone.

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
