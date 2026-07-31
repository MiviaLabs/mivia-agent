# 33 — Lifecycle hooks (deterministic control + observation layer)

**Status:** DESIGN — **blocked on §3b and §6a**; not implementable as written.
**Date:** 2026-08-02 · verified against HEAD and re-researched 2026-08-01
**Depends on:** `25-skill-triggers` (shipped), the `runtime.Dispatcher.Invoke` gate,
the `events.Bus` fan-out, **`05-agent-model-core` §01 (`load_workspace_config`, NOT
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
# There is deliberately NO `trust` key here — see §6a. A file cannot declare its
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
- **The `trust` key is gone** — §6a.
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
(`[[providers]]`, `[tools]`, and agent definitions all want it too) and it does not
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

### 6a. BLOCKER (resolved) — a file may not declare its own trust tier

The first draft put `trust = "managed" | "project" | "user"` **inside the `[[hooks]]`
table**, i.e. inside the very file whose trustworthiness is in question. A hostile
`.mivia/mivia.toml` would simply write `trust = "managed"` and grant itself the tier
that "always runs; user cannot disable."

This is not a novel observation — plan `05` §5 already litigated and closed exactly
this, and its wording should be read as binding here:

> **The gate cannot live in `mivia.toml`.** … A hostile repo would ship `mivia.toml`
> containing `load_workspace_config = true` and authorize itself. Same reasoning `04`
> §5 applies to the namespace directory: *a floor the agent can lower is not a floor.*

**Resolution:** the `trust` key does not exist. Trust is never *declared*; it is
*derived*, from two things the config file cannot influence:

1. **Which file the hook came from**, resolved at a fixed path by the loader — not a
   path the file can name. In v1 that is only `config.UserConfigPath()` (§3b).
2. **The content hash of the hook definition**, matched against the trust store.
   This is Codex's model ("the trust system tracks hook definitions by hash") and it
   gives the property a name-keyed store cannot: **editing a trusted hook revokes its
   trust automatically.** A name-keyed store lets `hooks/fmt.sh` be confirmed once and
   rewritten forever after.

**Trust tiers (derived, not declared):**

| Tier | Source | Default behaviour | Override |
|---|---|---|---|
| `managed` | `~/.mivia/managed.toml`, a separate operator/admin file, absent by default | Always runs; user cannot disable from TUI | — |
| `user` | `[[hooks]]` in `~/.mivia/mivia.toml` at its fixed path | Runs only if `(source, definition-hash)` is in the trust store; otherwise prompts once, interactively | `--bypass-hook-trust` |
| `workspace` | `[[hooks]]` in a workspace `mivia.toml` | **Stripped at load with a warning** (§3b). Not "untrusted" — not loaded at all in v1. | none |
| `untrusted` | user-config hook whose hash is unconfirmed or has changed | **Does not run.** Listed in `/hooks` as pending. | `/hooks trust <id>` |

**Defaults are fail-closed.** A fresh install runs **zero** hooks until confirmed.
This is the opposite of "parse and silently skip." A `PreToolUse` hook the user didn't
know about is a privilege escalation; the default must be that it doesn't run.

**Headless.** With no TTY there is no one to confirm, so `-p`/headless runs **zero**
non-managed hooks unless `--bypass-hook-trust` is passed. The first draft proposed
`--trust-project-hooks` / `--trust-user-hooks`; those are dropped. A flag whose name
reads as a feature ("trust my hooks") gets pasted into CI configs unexamined, which is
the failure mode the flag exists to prevent. One flag, and it says `bypass`.

This composes with our existing `.mivia/policy/agent-hook-bypass.json`, which already
blocks `--no-verify` and hook-bypass env vars at the *agent* layer. That policy is
about the agent not circumventing the **user's** git hooks; this layer is about the
**user's** mivia hooks running at all. Different direction, same principle: hooks are
trust-gated, not auto-run.

### 6b. The bypass flag

Add `--bypass-hook-trust` (mirrors Codex's `--dangerously-bypass-hook-trust`). Intent
is **automation that has already vetted hook sources** (CI). It must never be the
default and must be documented as dangerous. The flag name carries "bypass" so it is
greppable and never reads as a feature. It is the **only** trust-relaxing flag (§6a).

---

## 7. Timeouts

Per-handler `timeout` in seconds, with per-event defaults matching the field's
character:

| Event | Default timeout | `on_timeout` default | Rationale |
|---|---|---|---|
| `PreToolUse` | 10s | **`block`** | gate; long blocks stall the loop |
| `PostToolUse` | 10s | `allow` | reactive; Claude/Codex default 600s, our v1 keeps it tight |
| `Stop` | 5s | `allow` | continuation prompt; must not delay next turn |

A timed-out hook is **killed and reported**, never silently dropped.

**Correction — a timed-out `PreToolUse` hook must not fail open.** The first draft
said a timeout "feeds back as a non-blocking warning (not a block)" for *all* events.
Applied to `PreToolUse`, that means **hanging the gate disables the gate**: a hook
written to enforce `.mivia/policy/agent-hook-bypass.json` stops enforcing it the
moment the script is slow, wedged on a lock, or waiting on stdin — and the tool call
proceeds unchecked. An attacker
who can make a hook hang has defeated it, and so has an ordinary flaky script.

So `on_timeout` is an explicit per-handler key, and **`PreToolUse` defaults to
`block`**. Reactive events keep fail-open, because a slow formatter must not stop
work. The default is written down in config and surfaced by `/hooks`, so an operator
who wants a fail-open gate chooses it knowingly.

**Context lineage.** The hook's timeout context is derived from the dispatcher's
incoming `ctx`, **not** from `execute`'s `callCtx` — that one does not exist yet at
the `PreToolUse` point (`dispatcher.go:347-351`), and by the `PostToolUse` point its
`cancel()` has been deferred and may already have fired. A `PostToolUse` hook run on a
canceled context would silently never execute; pin that with a test.

Hook timeouts are **not** charged against `req.Timeout`. A tool granted 300s by
`run_command`'s `Capability` (`run.go:37-46`) must not lose 20s of it to hooks.

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

Note: **only exit 0 parses stdout as JSON**, matching Claude and Codex. On exit 2 the
JSON is ignored and stderr is the reason. This avoids a hook that blocks *and* returns
a contradictory JSON body.

**structured output (optional, preferred for PreToolUse):**

The first draft used `{"decision":"block","reason":"…"}` and claimed it was
"Claude/Codex's `hookSpecificOutput` shape, trimmed." **It is the wrong event's
shape.** Claude Code's flat `decision`/`reason` belongs to `PostToolUse`, `Stop`,
`UserPromptSubmit` et al.; `PreToolUse` uses a nested, differently-named field. A hook
script written to the first draft's contract would be **silently non-portable in the
one direction the plan cares about** — and worse, a Claude-authored `PreToolUse` hook
pasted into mivia would emit `hookSpecificOutput` that our parser ignored, and its
*deny* would read as *allow*. Fail-open by schema drift.

`PreToolUse` — mirror Claude's nested shape:
```json
{ "hookSpecificOutput": {
    "hookEventName": "PreToolUse",
    "permissionDecision": "deny",
    "permissionDecisionReason": "commit uses a hook-bypass flag forbidden by policy" } }
```
v1 accepts `permissionDecision` values `allow` and `deny`. `ask` and `defer` are
**rejected at parse time with a named error**, not treated as `allow` — mivia has no
interactive permission prompt at the dispatcher layer for them to escalate to, and
mapping an unknown decision onto the permissive branch is exactly the drift above.

`PostToolUse` / `Stop` — flat shape, matching those events in Claude/Codex:
```json
{ "decision": "block", "reason": "…", "additionalContext": "…" }
```
(`decision: "block"` on `PostToolUse` does not undo the action; it feeds the reason to
the model, as in the field.)

**An unparseable stdout on exit 0 falls back to exit-code semantics** and emits a
warning — it must never be interpreted as a decision.

### 8a. `updatedInput` is an explicit non-goal, and the reason is an invariant

Claude Code's `PreToolUse` may return `updatedInput` to **rewrite the tool's
arguments** before execution. v1 rejects the field with a named error rather than
ignoring it.

The reason is not conservatism, it is a concrete break: `Invoke` computes
`meta.InputHash` from `req.Input` at `dispatcher.go:219`, and `reserve` records that
hash as the invocation fingerprint at `dispatcher.go:288/318` — both **before** the
hook point. A hook that mutated `req.Input` afterwards would produce an audit record
and a dedup fingerprint describing input that **was never executed**, and the
`"invocation id reused with different input"` guard (`dispatcher.go:288-290`) would
start firing on legitimate retries. Supporting `updatedInput` means moving hash
computation after the hook and re-reasoning about `reserve`'s critical section — a
real change to the dispatcher's identity model, not a field addition.

---

## 9. Integration points (concrete, against HEAD)

| Concern | Location | Change |
|---|---|---|
| TOML parse of `[hooks]` | new `internal/hooks/config.go` | parse + validate; reject unknown events, deferred events, handler types, `trust`, `run`, `updatedInput` (§4, §5, §6a, §8a) |
| User-only load + workspace strip | `internal/config/load.go`, at the workspace-file branch | read `[[hooks]]` only from `config.UserConfigPath()`; strip + warn for a workspace file (§3b), mirroring `05` §5 |
| PreToolUse gate | `dispatcher.go:251-254`, between `!res.allowed` and `return d.execute(...)` | if `req.Kind == Tool` and the hook field is non-nil, call it; on deny, return a **blocked** result (§9c) without executing |
| PostToolUse reactive | `dispatcher.go:254` return path | call the post hook; attach bounded context per §9b |
| Stop | `internal/cli/tui_events.go:78`, the `KindTurnEnd` publish | run hooks, append continuation prompt |
| `/hooks` browser | `internal/cli/` slash command | list events, matchers, derived trust tier, pending/active; promote by hash |
| Trust store | `~/.mivia/hook-trust.json` via `workspace.NamespacePath` (the **one** JSON file, because it is state not config) | records confirmed `(source, definition-hash)` pairs (§6a); not in `mivia.toml` |

### 9a. Dispatcher coupling — keep it optional

`internal/runtime` must not import `internal/hooks` (import cycle, plus every test
binary would link the exec path). Instead `Policy` gains optional
`PreInvokeHook`/`PostInvokeHook` **func fields**; the CLI wiring sets them when hooks
are configured and trusted, leaves them nil otherwise. Nil = no hooks = one nil
compare = today's behaviour. This matches how `Policy.Sink` is already an optional
func field — see §9d for why the placement on `Policy` rather than `Dispatcher` is
load-bearing rather than incidental.

### 9b. BLOCKER — `PostToolUse` context must not breach the output ceiling

The first draft says: *"append `additionalContext` to result."* Appending hook stdout
to `result.Output` after `execute` has returned **bypasses the per-tool output ceiling
entirely**, and that ceiling is INV-AG-25/26/27 — three of the most heavily pinned
invariants in the repo.

The check is `if len(out) > res.ceiling` at `dispatcher.go:383`, inside `execute`. A
hook appending at `dispatcher.go:254` writes *after* it. The result: a tool that
declared `ResultBudgetBytes()` and passed its own bound can hand the model an
arbitrarily large payload, and `meta.OutputHash` (`dispatcher.go:387`) would describe
the pre-append bytes — an audit hash that does not match what the model received. That
is INV-AG-10's shape of defect one layer over.

**Resolution:** hook context is **never spliced into `Result.Output`.** It travels in
a new, separately bounded field — `Result.HookContext` — with its own fixed byte
budget (proposed 8 KiB total across all `PostToolUse` handlers for one invocation,
declared as a constant, not configurable in v1). The agent loop renders it as an
attributed block distinct from tool output. Over-budget hook stdout is **truncated
with a notice**, not refused: unlike tool output (which the dispatcher destroys,
INV-AG-25), hook stdout is advisory, and destroying a tool's result because its
formatter was chatty is a worse failure than truncating the advice.

This also keeps hook stdout out of `meta.OutputHash` and `meta.OutputPreview`, so the
audit record continues to describe the *tool's* bytes, which is what it is for.

### 9c. Blocked is not failed

The first draft says "return `fail(blockReason)`." `fail` routes to `failResult`,
which stamps `meta.Status = "failed"` (`dispatcher.go:410`). A policy block and a
broken tool would then be indistinguishable in the audit sink, in the TUI, and in
`internal/cli`'s status classification.

Add a distinct `"blocked"` status flowing through the same `failResult` machinery
(so waiters are still released, `dispatcher.go:462-467`) with:

- `meta.Status = "blocked"`;
- a payload of `{"status":"blocked","error":"<hook reason>"}` — the reason must reach
  the model, that is the entire point of a block;
- a message that, per INV-AG-27's existing note, **avoids the substrings
  `internal/cli`'s `statusFromErr` matches** for canceled/timed-out, so a blocked call
  is not misreported as a cancellation.

### 9d. Hook wiring lives on `Policy`, and subagents inherit deliberately

§9a's justification is factually wrong: `Sink` is a field on **`Policy`**
(`dispatcher.go:71`), not on `Dispatcher`. That is not pedantry — it decides
propagation. `Policy` is copied to derived dispatchers by `Dispatcher.Policy()`
(`dispatcher.go:170-176`), which deliberately clears only `Allow`.

**Decision: hook funcs go on `Policy`, next to `Sink`, and therefore propagate to
scoped subagent dispatchers.** A `PreToolUse` gate that a subagent silently escapes is
not a gate — subagents run the same tools against the same workspace. This must be
asserted by a test, because it is a security property that currently falls out of a
struct copy.

The trust store is the single justifiable JSON file: it is *runtime state* (which
hooks the user confirmed), not *configuration*. Config is TOML; state may be JSON.
This keeps the "all config is TOML" rule intact while not forcing a state file into
the config format.

---

## 10. What this does NOT do (explicit non-goals)

- **Does not couple hooks to skill `triggers:`.** A hook fires on a lifecycle event;
  a trigger influences model selection. They are independent.
- **Does not read hooks from `SKILL.md` frontmatter** — *deferred, not impossible.*
  Claude Code does exactly this (§0, §1e), and it is a reasonable future surface. It
  is out of v1 because skill-scoped hooks make the installed hook set depend on model
  selection, which must be reconciled with §6a's hash-keyed trust store first: what is
  the confirmation UX when a skill the model just picked wants to install a hook
  mid-turn? That question deserves its own plan.
- **Does not add `http`, `mcp_tool`, `prompt`, or `agent` handler types.** v1 is
  `command` only (§5). Note the field has five, not the three the first draft listed.
- **Does not support `updatedInput` / tool-argument rewriting** (§8a).
- **Does not read hooks from workspace config** (§3b) — deferred with the config
  merge layer it requires.
- **Does not implement `SessionStart`** — no publish site exists (§2, §4).
- **Does not make hooks run in `-p`/headless without `--bypass-hook-trust`** (§6a).
- **Does not change `triggers:` or plan 25.** That work is shipped and stays.
- **Does not introduce a plugin/marketplace system.**

---

## 11. Threat model (required because hooks run arbitrary code)

| Threat | Mitigation |
|---|---|
| Malicious `.mivia/mivia.toml` in a cloned repo runs code on `cd` | Workspace `[[hooks]]` are **not loaded at all** in v1 (§3b), stripped with a warning like `05` §5 does for workspace prompts |
| Hostile config grants itself the always-run tier | No `trust` key exists; tier is derived from the fixed load path + trust store (§6a) — *a floor the agent can lower is not a floor* |
| A trusted hook script is silently rewritten after confirmation | Trust is keyed on the **content hash** of the definition (§6a); an edit revokes trust and re-prompts |
| Hook command injected via tool argument (e.g. filename `"; rm -rf /`) | `argv` is a TOML array; **no `shellwords`, no interpolation, no `sh -c`** (§3c). Tool-derived values reach the hook only as env vars and stdin JSON, never as command syntax |
| Hook resolves to an unexpected binary | No `PATH` lookup; `argv[0]` is a path resolved against the declaring config file's directory (§3c) |
| Hook stdout is treated as a decision it did not make | Only exit 0 parses JSON; unknown `permissionDecision` values are a **parse error**, never coerced to `allow` (§8) |
| A slow/hung `PreToolUse` hook silently disables the gate | `on_timeout` defaults to `block` for `PreToolUse` (§7) |
| Hook output smuggles unbounded content past the tool ceiling | Hook context is a separately bounded field, never appended to `Result.Output` (§9b) — INV-AG-25/26/27 preserved |
| Hook stdout injects instructions into the system prompt | `SessionStart` (the only system-context surface) is cut from v1 (§4); `PostToolUse`/`Stop` output is model-visible, bounded, and attributed |
| Subagent escapes the gate | Hook funcs ride on `Policy`, which propagates to derived dispatchers (§9d), pinned by test |
| Hook re-enters the agent and loops | §11a |
| Hook blocks everything (DoS via PreToolUse) | Per-handler timeout (§7); a blocked tool reports the reason and the model adapts |
| Untrusted hook promoted silently | `/hooks trust` is an explicit user action; managed hooks are operator-set only |

### 11a. Re-entrancy — hooks must never re-enter the dispatcher

Not addressed in the first draft, and load-bearing given §3c. If a hook's execution
went through the tool path (`run_command`) — as the first draft's "reuse the existing
allowlist exec path" implied — then a `PreToolUse` hook matching `run_command` would
dispatch `run_command`, which would fire `PreToolUse`, which would dispatch
`run_command`. Unbounded recursion on the *first* hook anyone writes, and `MaxDepth`
(`dispatcher.go:267`) would not catch it because hook execution carries no depth.

Two structural guarantees, both testable:

1. Hooks execute via `internal/hooks`' own `exec.CommandContext` (§3c). They never
   construct a `runtime.Request` and never call `Dispatcher.Invoke`.
2. `internal/hooks` does not import `internal/tools` or `internal/runtime`. Pin with
   an import-graph assertion, the same way the package boundary is enforced elsewhere
   — a comment saying "hooks are out-of-band" does not survive a refactor.

A third, belt-and-braces guard: a process-wide re-entrancy flag so that if a hook ever
does reach `Invoke` (via a future handler type, or a bug), the nested `PreToolUse`
is skipped rather than recursing.

---

## 12. Verification

`go test ./internal/hooks/...` — config and execution:

- accepted v1 events; unknown events rejected; **deferred** events rejected with a
  *deferred*, not *unknown*, message (§4)
- `trust`, `run`, `updatedInput`, and `type = "prompt"|"agent"|"http"|"mcp_tool"` each
  rejected with a named error (§5, §6a, §8a)
- `argv[0]` is resolved against the config file's directory; **no `PATH` lookup** (§3c)
- timeout kills the process; `on_timeout = "block"` denies, `"allow"` warns (§7)
- exit 2 reads stderr and ignores stdout JSON; unparseable stdout on exit 0 falls back
  to exit code with a warning; unknown `permissionDecision` is a parse error (§8)
- trust store is keyed by `(source, definition-hash)`; **editing a confirmed hook
  revokes its trust** (§6a)

`go test ./internal/config/...`:

- `[[hooks]]` in a **workspace** `mivia.toml` is stripped with a warning; the same
  table in user config at `UserConfigPath()` loads (§3b) — the direct analogue of
  `05`'s `TestGate_IgnoredInWorkspaceConfig`

`go test ./internal/runtime/...`:

- nil hook fields = today's behaviour, byte-identical results
- deny path returns **status `"blocked"`**, not `"failed"`, carries the reason to the
  model, and does not call the handler (§9c)
- a blocked call releases its waiter and clears its active marker (§4b)
- hooks fire for `Kind == Tool` only — **not** for `Skill` or `Subagent` (§4b)
- a **deduplicated** invocation fires no hook (§4b)
- **a scoped subagent dispatcher inherits the hook funcs via `Policy()`** (§9d)
- `PostToolUse` context lands in `Result.HookContext`, is bounded, and **`meta.
  OutputHash`/`OutputPreview` still describe the tool's bytes only** (§9b)
- oversized hook stdout is truncated with a notice and does **not** destroy the tool
  result (§9b)
- a `PostToolUse` hook still runs after `execute`'s `callCtx` has been canceled (§7)

Regression / boundary:

- import-graph assertion: `internal/hooks` imports neither `internal/runtime` nor
  `internal/tools` (§11a)
- a `PreToolUse` hook matching `run_command` does not recurse (§11a)
- `go test -race ./...`
- `make verify` — new gate: every `[[hooks]]` event is a known v1 event; unknown ⇒ fail

Manual:

- a `PreToolUse` hook on `run_command` blocks a commit the agent-hook-bypass policy
  forbids, and the reason reaches the model
- a fresh install runs zero hooks; `-p` runs zero non-managed hooks without
  `--bypass-hook-trust`
- a repo containing `.mivia/mivia.toml` with `[[hooks]]` runs nothing and warns

## 13. Invariant to register

A new row (`INV-AG-29` — `invariants.md` runs contiguously to `INV-AG-28`; plans 35,
36, 37, 40 have reserved 30–33, so confirm 29 is still free at implementation time).
Broken into independently testable clauses, since the first draft's single sentence
mixed four unrelated guarantees:

> *Lifecycle hooks are loaded only from user config read at its fixed path — a
> workspace `mivia.toml` never supplies a hook and never declares its own trust; trust
> is derived, keyed on the hook definition's content hash, so editing a confirmed hook
> revokes it. A fresh install and any headless run without `--bypass-hook-trust`
> execute zero non-managed hooks. Hooks execute as an explicit `argv` with no shell,
> no `PATH` lookup, and no interpolation of tool-derived values, and never re-enter
> `Dispatcher.Invoke`. `PreToolUse` is the only blocking event, fires only for
> `Kind == Tool`, propagates to derived subagent dispatchers, defaults to blocking on
> timeout, and returns status `blocked` with a reason that reaches the model. Hook
> output is carried in its own bounded field and is never appended to `Result.Output`,
> so per-tool output ceilings (INV-AG-25/26/27) and the audit hashes remain exact.*

## 14. Rollback criterion

If the Dispatcher hook fields introduce measurable latency on the no-hook path (they
must not — nil check is the fast path), make the fields a single optional
`Hooks` interface with a no-op implementation, so the check is one nil-compare.
If trust UX proves too heavy for interactive use, narrow to "project hooks prompt
once per file hash, not per session" before considering removal. Do not ship hooks
without trust — that is the one decision this plan will not walk back.

## 15. Sequencing — the slice plans

This document is the spine: research (§1), decisions (§3–§9), threat model (§11), and
the invariant (§13). The implementation is split into eight slices, each with its own
plan in this directory. **Read this file first; a slice plan assumes its decisions.**

| # | Slice | Owns | Depends on |
|---|---|---|---|
| [`01`](01-config-scope-and-workspace-strip.md) | Config scope + workspace strip | §3b blocker: hooks load from user config only; workspace `[[hooks]]` stripped with a warning | — |
| [`02`](02-hook-config-parse.md) | `internal/hooks/config.go` | TOML parse, the rejection table, per-event defaults, the definition hash | `01` |
| [`03`](03-hook-exec-and-protocol.md) | `internal/hooks/exec.go` | own exec path (§3c), argv-only, env injection, timeout/`on_timeout`, the §8 wire protocol | `02` |
| [`04`](04-trust-store-and-hooks-command.md) | Trust store + `/hooks` | hash-keyed `~/.mivia/hook-trust.json`, tiers, confirmation UX | `02`, `03` |
| [`05`](05-headless-trust-gate.md) | Headless gate | zero non-managed hooks without `--bypass-hook-trust` | `04` |
| [`06`](06-dispatcher-integration.md) | Dispatcher | `Policy` hook fields, `PreToolUse`/`PostToolUse`, `blocked` status, bounded `HookContext`, subagent propagation, re-entrancy | `03`, `04`, `05` |
| [`07`](07-stop-event-wiring.md) | `Stop` | root-turn-end wiring at `internal/cli/tui_events.go:78` | `03`–`05` |
| [`08`](08-docs-and-invariant.md) | Docs + `INV-AG-29` | `docs/development/lifecycle-hooks.md`, `mivia.toml.example`, invariant, `make verify` gate, INDEX row | `01`–`07` |

**Step 0 is a gate, not a slice.** Confirm the §3b scope cut — hooks load from user
config only. If project hooks are wanted in v1, this plan stops and a config-merge plan
goes first. Every slice below assumes the cut.

**Order changed from the first draft, deliberately.** It sequenced mechanism before
trust and then warned "do not land the mechanism without the gate." A sequence whose
own note says its first three steps are unlandable is the wrong sequence — the warning
would be load-bearing on reviewer memory across three commits. Here the trust store
(`04`) and the headless gate (`05`) land **before** the dispatcher is ever wired to
call a hook (`06`), so at no commit does the tree contain a reachable hook-execution
path without its gate. Slices `01`–`03` are independently testable and reachable only
from tests.

**One correction originates in a slice rather than here:** `08` §1 records that this
document's original docs target, `docs/development/hooks.md`, already exists, is about
*Git* hooks, and is a required path with a unique-H1 gate. Lifecycle hooks are
documented at `docs/development/lifecycle-hooks.md` instead.

---

## Appendix A — why not `hooks.json` (the rejected alternative)

| Argument for JSON | Verdict |
|---|---|
| Claude Code uses `.claude/settings.json` | Claude is JSON-configured end-to-end; we are TOML end-to-end. Not transferable. |
| **Codex supports `hooks.json` too** | True, and the first draft denied it (§1c is corrected). But Codex supports *both* and merges them with a startup warning — i.e. it pays for the second format with an ambiguity it has to warn about. That is an argument against adding the second format, not for it. |
| Codex uses JSON for the stdin/stdout protocol | That is a *wire protocol*, not config. We reuse the JSON wire protocol (§8) while keeping TOML config. The two are unrelated. |
| Hooks feel different from provider config | They are still config: declared, static, version-controlled. They belong in the config format. |
| JSON arrays map to matcher+handlers | TOML `[[hooks.handlers]]` maps identically and is already the idiom in `mivia.toml` (`[[providers.X.models]]`). |

The only JSON file this plan introduces is `~/.mivia/hook-trust.json` — runtime
*state*, not config — and that is the distinction that keeps "all config is TOML"
true. (See §9.)

---

## Appendix B — corrections log (verification pass, 2026-08-01)

Every row below was a claim in the first draft that verification against HEAD or
re-research falsified. Recorded so a reader of the original can see what moved.

| # | First draft said | Actually | Fixed in |
|---|---|---|---|
| 1 | Project and user config "merge additively" | `FirstExisting` reads exactly one file (`paths.go:47-60`, `load.go:308`); a project file *replaces* user config | §3b — **blocker**, scope cut to user-only |
| 2 | `trust` is a field in the `[[hooks]]` table | A file declaring its own trust tier is self-authorization; `05` §5 already closed this class | §6a — **blocker**, key removed |
| 3 | `SessionStart` fires "where `KindSessionStart` is published" | Never published anywhere in `internal/` | §2, §4 — event cut from v1 |
| 4 | Hooks reuse `run_command`'s allowlist exec path | That path rejects path-shaped `argv[0]` (`run.go:207-211`), requires an allowlisted program, pins cwd, and reformats output — it rejects the plan's own examples | §3c — own exec path, stated explicitly |
| 5 | `run` is a string parsed with `shellwords` + `$MIVIA_FILE` substitution | Splices tool-derived data into a command line — the injection the plan claims to prevent | §3a — `argv` array + env vars |
| 6 | Blast radius MEDIUM | Arbitrary code execution on `cd` into a repo, compounded by #1 | header — HIGH |
| 7 | PreToolUse returns `{"decision":"block"}` | Claude uses `hookSpecificOutput.permissionDecision: allow\|deny\|ask\|defer`; the flat shape is other events' | §8 — and the mismatch failed *open* |
| 8 | Timeout is always a non-blocking warning | Makes a hung `PreToolUse` hook a disabled gate | §7 — `on_timeout`, defaults to `block` |
| 9 | PostToolUse "appends `additionalContext` to result" | Bypasses the ceiling check at `dispatcher.go:383`, breaking INV-AG-25/26/27 and desyncing `meta.OutputHash` | §9b — **blocker**, separate bounded field |
| 10 | Block returns `fail(blockReason)` | `failResult` stamps `"failed"`; a policy block would be indistinguishable from a broken tool | §9c — `"blocked"` status |
| 11 | "`Policy.Sink` is a func field on `Dispatcher`" | It is on `Policy` (`dispatcher.go:71`) — which decides whether subagents inherit hooks | §9d — on `Policy`, inheritance made deliberate |
| 12 | Silent on re-entrancy | Combined with #4, the first hook anyone writes recurses unboundedly | §11a |
| 13 | Silent on which `Kind` fires "ToolUse" | `Invoke` dispatches `Tool`, `Skill`, `Subagent` alike | §4b — `Tool` only |
| 14 | "Nobody couples skills and hooks" | Claude Code has `hooks:` in `SKILL.md` frontmatter, skill-scoped; Grok skills hook lifecycle events | §0, §1e, §10 — narrowed claim, explicit deferral |
| 15 | Claude has ~15 events and 3 handler types | ~30 events, 5 handler types (`command`, `http`, `mcp_tool`, `prompt`, `agent`) | §1b |
| 16 | Codex has "no separate hook-scripts file" | Codex reads `hooks.json` *and* inline `[hooks]`, merging with a warning | §1c, Appendix A |
| 17 | Trust by "hook id"; hash-keying only as a §14 fallback | Codex tracks trust *by hash*; name-keying lets a confirmed script be rewritten freely | §6a — hash-keying is primary |
| 18 | Silent on `updatedInput` | It exists in Claude; adopting it would desync `meta.InputHash`/fingerprint (`dispatcher.go:219`, `:288`) | §8a — rejected with a reason |
| 19 | Sequence landed mechanism first, with a note not to | A sequence that warns its own first three steps are unlandable is the wrong sequence | §15 — trust lands before the call site |
| 20 | "The gap is authoring surface and policy, not plumbing" | Three missing pieces of plumbing (#1, #3, and `load_workspace_config` unimplemented) | §2 |
