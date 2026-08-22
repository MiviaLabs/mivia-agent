# UI Replacement (4-10) + mivia-ai-sdk Simplification

Repo: `/home/mac/projects/mivialabs/mivia-agent`, branch `dev`. Phase 0-3 of
the UI replacement and two hardening commits are already shipped (7 commits
ahead of `origin/dev`). The binding plan for Phase 0-3 is
`docs/design/ui-replacement-phases.md` lines 1-283.

This file is the working plan for everything that comes next, under a
single rule: **simplify, refactor, do not over-engineer.**

## The split: SDK vs CLI

`mivia-ai-sdk` (`github.com/MiviaLabs/mivialabs-sdk`, sibling repo) and
`mivia-agent` (this CLI repo) have a clean line between them:

- **`mivia-ai-sdk` is the generic, reusable, product-agnostic core.** A
  block library for building agent runtimes in Go. Composable packages:
  `envelope`, `room`, `flow`, `machine`, `agentloop`, `tools`, `provider`,
  `contextplan`, `contextstate`, `contextsummary`, `longtermmemory`,
  `memory`, `mcp`, `ledger`, `durablefence`, `subagent`, `hooks`,
  `identity`, `discovery`, `dispatch`, `heartbeat`, `scheduler`,
  `trigger`, `trace`, `policy`, `workspace`, `secretpath`, `envfile`,
  `spool`, `providerregistry`, `agent`, `agentrun`, `events`,
  `runconfig`, `skills`, `schema`, `taskrun`, `e2e`, `channel`, `usage`,
  `runtime`, `a2a`, `a2aclient`, `a2aack`. Any primitive that any agent
  runtime needs lives here.

- **`mivia-agent` is the product.** The `mivia` binary, the
  `mivia-ui` binary, the chat REPL, the new bubbletea-free UI, the
  `mivia.toml` config layer, the operator-visible redaction, the
  chat-block bubble rendering, the slash command set, the workstation
  identity, the agent/skills/sessions/worktrees filesystem layout.
  CLI-specific features stay in CLI.

If a primitive has the same shape across products, it belongs in the
SDK. If a primitive is shaped by mivia's product (the `mivia.toml`
layout, the chat-block format, the operator redaction policy), it
stays in the CLI. The audit below classifies every local mirror
against this line.

Two threads are sequenced below:

- **Thread A** — UI Replacement Phases 4-10 (continuation of the binding
  plan).
- **Thread B** — `mivialabs-sdk` simplification: an audit of every
  local mirror in `mivialabs-agent/internal/` against the SDK
  primitive it duplicates, with the deletions, the SDK extensions to
  propose upstream, and the cli-family cleanup that follows when the
  inner mirrors die.

The two threads are interleaved; Thread B's audit is the source of
truth for what `cmd/mivia` and `cmd/mivia-ui` import. Phases 4-7 of
Thread A slot in where Thread B's drops free up internal dependencies.
Phase 8 of Thread A is the cutover and the merge point.

---

## Thread A — UI Replacement Phases 4-10 (continuation of binding plan)

These continue the binding plan from line 285 onward. The
plan-reviewer is invoked per phase per ADLC.

### Phase 4 — `Approver`: tool approval gating

**Goal.** `ports.Approver` (Pending channel + Resolve) over the real
approval surface.

**Scope.** `internal/uiadapter/approver.go`. Find the existing approval
gating in `internal/tools` / `internal/runtime` (read `internal/clichat`
for behaviour reference only — DO NOT import it). `TranslateEvent`
emits `KindToolPending` for tools awaiting approval. Wire the adapter
into `cmd/mivia-ui`.

**Tests.** Approve→tool runs; Deny→tool does not run; Resolve with
unknown id is a no-op; turn cancelled while approval pending does not
deadlock; offline smoke test drives a scripted approver stub through
one approval-and-run cycle.

**Commit subject:** `feat(agent): implement tool approval gating in uiadapter`.

### Phase 5 — `SessionStore`: list, load, save

**Goal.** `ports.SessionStore` (List, Load, Save) backed by the real
store.

**Scope.** `internal/uiadapter/sessionstore.go`. Wire into
`cmd/mivia-ui`.

**Tests.** List on empty store, save then list, load a saved session
and confirm history is restored, load a missing session returns a
clear error; offline smoke test.

**Commit subject:** `feat(agent): implement session store port in uiadapter`.

### Phase 6 — `CommandRunner`: slash commands

**Goal.** `ports.CommandRunner`. Decide deliberately which commands the
new UI supports; do NOT port the CLI's set mechanically.

**Scope.** `internal/uiadapter/commands.go`. Resolves a slash command
against `chat.Session` methods directly. The legacy CLI's command set
lives in `internal/clichat` (read for behaviour reference only — DO
NOT import it). Document the dropped set in the commit body.

**Tests.** One per supported command; unknown command returns a clear
outcome.

**Commit subject:** `feat(agent): implement command runner port in uiadapter`.

### Phase 7a — Settings ports (General, Providers, MCP)

**Goal.** `ports.GeneralSettings`, `ports.ProviderSettings`,
`ports.MCPSettings`.

**Scope.** `internal/uiadapter/settings_general.go`,
`settings_provider.go`, `settings_mcp.go`. Honour `ports.Scope`. Return
real `SaveHandle`s.

**Commit subject:** `feat(agent): implement general, provider, and MCP
settings ports`.

### Phase 7b — Settings ports (Agents, Automations)

**Goal.** `ports.AgentSettings`, `ports.AutomationSettings`.

**Scope.** `internal/uiadapter/settings_agent.go`,
`settings_automation.go`. `AgentSettings` maps onto `internal/agents`.
`AutomationSettings` is the largest surface (triggers, schedules,
runs, watch). Check whether a real backend for automations exists yet;
if not, document explicitly and either keep demoharness wired for
that port or defer the phase.

**Commit subject:** `feat(agent): implement agent and automation settings
ports`.

### Phase 8 — Cutover: `cmd/mivia` uses the new UI

**Goal.** The shipped binary launches the new UI.

**Scope.** In `cmd/mivia/main.go`, replace
`cli.SetTUILauncher(legacytui.RunTUI)` with a launcher that starts
the new UI through `uiadapter`. Keep `legacytui` reachable behind an
opt-in escape hatch for one release. Flip `cmd/mivia-ui`'s `--demo`
default to false, since real mode is now the primary path.

**Tests.** End-to-end real-mode smoke (manual acceptance on a
workstation with credentials; offline unit tests as far as they go).

**Commit subject:** `feat(cli): launch the new UI from cmd/mivia`.

### Phase 9 — Delete `legacytui`

**Goal.** Remove the old TUI.

**Precondition.** Phase 8 has been running without regression long
enough that the escape hatch can be dropped.

**Scope.** Remove the escape hatch from Phase 8. `git rm -r
internal/legacytui`. Remove `internal/legacytui` import and
`SetTUILauncher` call from `cmd/mivia/main.go`. Remove
`internal/cli/tui_launcher.go` if it has no other consumer. Remove
`internal/legacytui` row from `.mivia/policy/import-layers.json`.
Delete `internal/clichat/legacytui_test_exports.go` and
`internal/clichat/legacytui_split_helpers_test.go`. `grep -ri
legacytui --include='*.go' --include='*.json' .` returns nothing.

**Tests.** Whole-tree build, vet, focused tests.

**Commit subject:** `refactor(ui): delete legacytui`.

### Phase 10 — Collapse the orphaned cli-family packages

**Goal.** Reclaim the structure. With `legacytui` gone, large parts of
`internal/clichat` (terminal rendering, bubbles, dialogs, rails,
markdown) exist only to serve a front end that no longer ships.

**Scope.** Audit `internal/clichat` (170+ production files).
Classify each Dead / Live. Delete the dead set. Do the same audit for
`cliagents`, `cliworkflow`, `cliorchestrate`, `cliworktree`, and the
alias shims in `internal/cli`. Whatever genuinely remains, decide
deliberately. Re-baseline `import-layers.json`. **This is where Thread
B's cli-family simplification lands the heavy lift — the dead set
is smaller because Thread B's drops already removed the underlying
mirrors.**

**Tests.** Whole-tree build, vet, focused tests, then `make verify`.

**Commit subject:** `refactor(cli): collapse packages orphaned by the
UI replacement`.

---

## Thread B — `mivialabs-sdk` simplification (the audit)

The audit is a one-pass read of every local mirror in
`mivialabs-agent/internal/` against the SDK's primitives. The SDK is
**not** yet a dependency of this module; nothing under `internal/`
imports it. The audit's output is a deletion list and an
SDK-extension list. Each deletion is gated on (a) the SDK having the
primitive and (b) the SDK having any extensions the local mirror
needs.

### B.0 — bring the SDK into the module (one-line commit)

- Add `github.com/MiviaLabs/mivialabs-sdk` to `go.mod`. The SDK has no
  semver tag; B.0 uses a `replace` directive pointing at the local
  path `/home/mac/projects/mivialabs-sdk` with a comment in
  `go.mod` explaining the temporary state and the planned
  tag-pinning.
- `go mod tidy` resolves cleanly. No cycles (the SDK's
  `modernc.org/sqlite` does not collide with `mivia-agent`'s
  `ncruces/go-strftime`; verify in B.0).
- No code uses the SDK yet. Build, vet, focused tests must still
  pass.
- Pre-clear `mivialabs-sdk/policy/pending_wiring.json` for the
  packages Thread B will use (`agentloop`, `contextplan`,
  `contextstate`, `dispatch`, `envelope`, `events`, `hooks`, `mcp`,
  `memory`, `longtermmemory`, `provider`, `schema`, `secretpath`,
  `skills`, `subagent`, `tools`, `usage`). If any is missing from
  the SDK's pending_wiring, the B.0 commit fails; file a
  sibling-repo PR to register it first.

**Commit subject:** `build(deps): bring mivialabs-sdk into the module`.

### B.1 — SDK extensions to propose upstream (one PR per extension, in the SDK repo)

Before any local mirror can drop, the SDK must have the missing
primitives. Each extension lands as a PR in
`github.com/MiviaLabs/mivialabs-sdk`, is reviewed and merged there,
and then enables the corresponding CLI drop in B.2.

The user has set the rule: the SDK should be a *generic, reusable,
product-agnostic core* — primitives that any agent runtime needs
belong in the SDK. The CLI only keeps product-specific features.
That means every B.1 extension is justified by its general
applicability, not by mivia's specific use.

| Local mirror | SDK primitive | Extension needed before drop | Why this belongs in SDK |
|---|---|---|---|
| `internal/agent` | `mivialabs-sdk/agentloop` | `WithRetryAfterPromptTooLong`, `WithToolParallelism(n, keyLock)`, `WithToolScrubOnRegistryChange`, `WithHeartbeat(d, fn)`, `WithToolResultSpool(spool)` | The inner tool-call loop is the SDK's job. CLI's additions are general agent patterns any caller needs. |
| `internal/mcp` (rejection + cap) | `mivialabs-sdk/mcp` | `WithDescriptionRedaction(*Policy)`, `WithSchemaByteCap(n)`, `WithServerNameEncode(fn)` | Description redaction and schema size cap are general MCP client concerns. Any caller consuming remote tools needs them. |
| `internal/secretpath` | `mivialabs-sdk/secretpath` | `WithSubstringExceptions([]string)` | Substring vs glob is a *matcher* choice, not a product choice. Both patterns are general. |
| `internal/contextmgr` (calibration) | `mivialabs-sdk/contextplan` | `WithCalibration(*Calibration)`, `WithHystereticPrune(highPct, lowPct int)` | Calibration and hysteretic-prune are general planner behaviours. |
| `internal/ledger` (SQLite repo) | `mivialabs-sdk/ledger` | `Recover`, `SetTaskAttempt`, `CompareAndSetTaskStatus`, `DisplayName` on the `Store` interface; move `StorageLedgerRepository` to a new SDK `ledger/sqlitestore` package | Durable task admission is the SDK's job. The CLI's SQLite repo is one impl; the SDK's `MemStore` is the other. |
| `internal/hooks` (Claude-Code style) | `mivialabs-sdk/hooks` | External-program `Handler` variant (JSON I/O + `PermissionDecision` parsing) | The registry shape is generic; the external-program executor is a generic pattern any Claude-Code-style runtime needs. |
| `internal/events` (per-handler buffered subscription) | `mivialabs-sdk/events` | Per-handler buffered subscription, `Delivery.Unsubscribe / Flush / Close`, typed `CompactionEvent` payload | The in-process bus is a generic primitive; per-handler buffered subscription and typed payloads are general patterns any consumer wants. |
| `internal/jschema` (model-facing helpers) | `mivialabs-sdk/schema` | `CorrectiveMessage(err, redact)`, `PromptAppendix`, `Example(s)`, `Contract(s)` | Model-facing helpers are general tool-calling-agent needs. CLI's redaction hook is the only CLI-specific bit. |
| `internal/reasoning` (Level / Dialect / Setting) | `mivialabs-sdk/provider` | `FormatReasoningEfforts([]Effort) string`; absorb `Level`→`ReasoningEffort`, `Dialect`→`ReasoningDialect`, `Setting`→`ReasoningPolicy` | Reasoning vocabulary is provider-level, not product-level. |
| `internal/provider` *type definitions* (drop the types, keep the impls) | `mivialabs-sdk/provider` | (no extension needed — types already in SDK; just type-swap the call sites) | Provider interface is generic. |
| `internal/workspace` *sandbox core* (drop the sandbox, keep the namespace paths) | `mivialabs-sdk/workspace` | (no extension needed — sandbox is already in SDK; just delete the duplicate) | `os.Root`-based sandboxing is generic. |

Each extension PR's body cites the local mirror's file:line as the
evidence of the missing primitive. PRs are sequenced so that the
extensions for the smallest drops land first; the B.2 drops below
become possible once each extension lands.

### B.2 — local-mirror drops (CLI simplifications)

After B.0 and B.1's extensions land, the following local mirrors
are candidates for deletion. Each is one commit, gated by
`go build ./...` and `go test ./affected-pkg`. The order is
dependency-first: a package that another deletion depends on lands
first.

#### Wave 1 (no upstream extension needed; SDK already has the primitive)

| Order | Local mirror | Replaces with | Commit subject |
|---|---|---|---|
| 1 | `internal/durablefence` (test-only harness) | `mivialabs-sdk/durablefence` | `refactor(deps): remove internal/durablefence superseded by mivialabs-sdk` |
| 2 | `internal/envfile` | `mivialabs-sdk/envfile.Load` | `refactor(deps): remove internal/envfile superseded by mivialabs-sdk` |
| 3 | `internal/contentref` (adopt `sha256:<digest>` shape; drop the `ref:<kind>:<digest>` kind discriminator) | `mivialabs-sdk/contextstate.Mint` | `refactor(deps): remove internal/contentref superseded by mivialabs-sdk` |
| 4 | `internal/contextstate` (every type and validator) | `mivialabs-sdk/contextstate` | `refactor(deps): remove internal/contextstate superseded by mivialabs-sdk` |
| 5 | `internal/reasoning` (Level / Dialect / Setting) | `mivialabs-sdk/provider` reasoning types | `refactor(deps): remove internal/reasoning superseded by mivialabs-sdk` |
| 6 | `internal/usage` in-memory accumulation; keep `UsageRecord`/`UsageWriter` as the durable log schema | `mivialabs-sdk/usage.Accumulator` | `refactor(agent): swap in-memory usage accounting to mivialabs-sdk` |
| 7 | `internal/agentmsg.ContentRef` (call sites that mint a ref) | `mivialabs-sdk/contextstate.Mint` | (rolled into the contentref drop, #3) |

#### Wave 2 (after B.1 extensions land)

| Order | Local mirror | Replaces with | Commit subject |
|---|---|---|---|
| 8 | `internal/mcp` (after redaction+cap extensions) | `mivialabs-sdk/mcp` | `refactor(deps): remove internal/mcp superseded by mivialabs-sdk` |
| 9 | `internal/secretpath` (after `WithSubstringExceptions`) | `mivialabs-sdk/secretpath` | `refactor(deps): remove internal/secretpath superseded by mivialabs-sdk` |
| 10 | `internal/agent` (after agentloop options) | `mivialabs-sdk/agentloop` with the new options | `refactor(agent): swap internal/agent to mivialabs-sdk/agentloop` |
| 11 | `internal/ledger` (after ledger extension; CLI's `StorageLedgerRepository` migrates to a new SDK `ledger/sqlitestore`) | `mivialabs-sdk/ledger` with new options; production-grade repo at `ledger/sqlitestore` | `refactor(deps): remove internal/ledger superseded by mivialabs-sdk` |
| 12 | `internal/events` (after events extensions) | `mivialabs-sdk/events` with per-handler buffered subscription + typed `CompactionEvent` | `refactor(deps): remove internal/events superseded by mivialabs-sdk` |
| 13 | `internal/jschema` (after schema extensions; keep the model-facing helpers) | `mivialabs-sdk/schema` with the model-facing helpers | `refactor(deps): remove internal/jschema superseded by mivialabs-sdk` |

#### Partial drops (CLI keeps the product-specific part, drops the generic part)

| Order | Local mirror | What moves to SDK | What stays in CLI | Commit subject |
|---|---|---|---|---|
| 14 | `internal/hooks` (Claude-Code style) | Registry shape (`mivialabs-sdk/hooks` grows an external-program `Handler` variant) | The JSON-I/O executor (`internal/hooks/exec.go` parses `{hookSpecificOutput:{permissionDecision:"allow\|deny\|ask"}}`) becomes one such handler | `refactor(agent): move hooks registry to mivialabs-sdk, keep executor local` |
| 15 | `internal/tools` (capped buffer, glob match, file observation) | The `Tool` interface and registry (already in `mivialabs-sdk/tools`) | `cappedBuffer` (stream-output capture), `glob_match.go` (Go file globbing), `file_observation.go` (file read) — CLI-specific tool implementations | `refactor(deps): remove internal/tools type definitions superseded by mivialabs-sdk` |
| 16 | `internal/workspace` (sandbox core) | Sandbox (`os.Root`-based, already in `mivialabs-sdk/workspace`) | The `mivia`-specific namespacing (`AgentsPath`, `SkillsDir`, `SessionsDir`, `WorktreesDir`, `ContextStorePath`, `MemoryDBPath`) | `refactor(deps): remove internal/workspace sandbox superseded by mivialabs-sdk` |
| 17 | `internal/provider` *type definitions* | The interface and request/response types (already in `mivialabs-sdk/provider`) | The concrete impls (`OpenAICompat`, `DeepSeek`, `LLMGateway`, `OllamaLoopback`) and the request-shaping helpers (`RepairReasoningLessToolExchanges`, `RepairToolPairing`, `deriveCacheUsage`) | `refactor(deps): remove internal/provider type definitions superseded by mivialabs-sdk` |
| 18 | `internal/skills` (file-based loader) | The in-memory registry and `Skill` type (already in `mivialabs-sdk/skills`) | The file loader (`loader.go`, `skill_markdown.go`) and the resource-snapshot subsystem (`resources.go`) | `refactor(deps): remove internal/skills registry superseded by mivialabs-sdk` |

After every drop, re-baseline
`.mivia/policy/import-layers.json`. The edge count goes down with
each deletion (the deleted package's out-degree leaves the count).
The cap should be re-baselined downward or kept at its current value
with documented headroom.

The 12 + 6 + 5 = ~23 commits above are the entire mivialabs-sdk
simplification work. Each commit is one local-mirror deletion or
partial drop. None of them require a new package in `mivia-agent`;
they are pure deletions. There is no `internal/sdkcompat` bridge
package, no SDK-backed `Conversation` adapter, no SDK-backed chat
REPL — the audit's findings ruled those out as over-engineering.

### B.3 — cli-family simplification

After B.2's drops, the cli-family is smaller because the
cli-family's inner dependencies are smaller. The cli-family itself
is **not** in the drop list above (none of it duplicates an SDK
primitive), but it *composes* the dropped mirrors; dead code paths
in the cli-family become visible only after the mirrors die. Phase
10 of Thread A combines the cli-family collapse with the remaining
dead-set audit. The two threads merge at the Phase 10 commit.

The cli-family is kept as the CLI command surface (`cliworktree`,
`clichat`, `cliagents`, `cliworkflow`, `cliorchestrate`); it just
becomes thinner because the mirrors it composed are gone.

### B.4 — out-of-scope for this audit (intentional non-mirrors)

These packages have no SDK analogue and are kept as-is:
`internal/agents` (CLI's own agent-resolution layer),
`internal/composition`, `internal/diff`, `internal/faultinject`,
`internal/legacytui`, `internal/miviaauth`,
`internal/providerregistry`, `internal/ui`, `internal/uiadapter`,
`internal/workflows/*`.

The cli-family (`cliworktree`, `clichat`, `cliagents`,
`cliworkflow`, `cliorchestrate`) is also out of scope as a
*drop target* — they are CLI command surfaces, not SDK mirrors.
They become thinner in B.3 / Phase 10.

### B.5 — what is NOT in this plan (per the "do not over-engineer" rule)

- **No `internal/sdkcompat` bridge package.** If a type-mismatch
  surface is found at the call site during a drop, fix the call
  site with a one-line conversion rather than build a bridge. The
  SDK's and the local's `Message`/`Request`/`Response` shapes are
  1:1; if a divergence is found in practice, fix the SDK or the
  call site, do not paper over it with a bridge.
- **No SDK-backed chat REPL.** The REPL is the cli-family's job
  (`internal/clichat/chat_repl.go`). It uses `internal/agent.Loop`.
  When B.2 drop #10 swaps `internal/agent.Loop` to
  `mivialabs-sdk/agentloop.Loop`, the REPL gets the SDK-backed loop
  for free — no separate "wire cmd/mivia to the SDK" step.
- **No SDK-backed `Conversation` adapter.** The Phase 2 `Conversation`
  wraps `*chat.Session`. It does NOT need a sibling `SDKConversation`
  wrapping `mivialabs-sdk/agentloop.Runner`; the Phase 8 cutover
  delegates the entire chat path to the SDK-backed REPL the
  cli-family owns, and the new UI consumes that REPL's output
  through the same uievent stream.
- **No full REPL rewrite.** B.2 drop #10 transitively replaces
  `internal/agent.Loop` with the SDK's, but the REPL's structure
  (slash commands, bubble rendering, dispatcher) stays. The diff
  for drop #10 is the Loop swap plus the call-site updates; the
  surrounding REPL is unchanged.

---

## Sequencing (merged view)

| Order | Phase | Thread | Subject |
|-------|-------|--------|---------|
| 1 | B.0 | SDK | bring mivialabs-sdk into the module |
| 2 | B.1 | SDK | (multiple sibling-repo PRs) extensions land upstream |
| 3 | B.2 #1-#7 | SDK | Wave 1 drops (no upstream extension needed) |
| 4 | Phase 4 | UI | tool approval gating in uiadapter |
| 5 | Phase 5 | UI | session store port in uiadapter |
| 6 | B.2 #8-#9 | SDK | Wave 2 drops 8-9 (mcp, secretpath) |
| 7 | Phase 6 | UI | command runner port in uiadapter |
| 8 | Phase 7a | UI | general, provider, MCP settings ports |
| 9 | B.2 #10-#13 | SDK | Wave 2 drops 10-13 (agent, ledger, events, jschema) |
| 10 | Phase 7b | UI | agent and automation settings ports |
| 11 | B.2 #14-#18 | SDK | partial drops (hooks, tools, workspace, provider types, skills registry) |
| 12 | Phase 8 | UI | launch the new UI from cmd/mivia |
| 13 | B.3 | UI | cli-family simplification (post-mirror-collapse) |
| 14 | Phase 9 | UI | delete legacytui |
| 15 | Phase 10 | UI | collapse packages orphaned by the UI replacement |

The order is suggested, not fixed. Any phase whose preconditions are
met may run. The edge-cap re-baseline at each phase is the
rate-limiter; if a phase's planned cap exceeds the measured count,
re-baseline before proceeding.

---

## What this plan does NOT cover

- Phase 0-3 (already shipped on `dev`).
- The `mivialabs-agent-desktop` sibling (out of scope).
- Provider-side changes (Anthropic, OpenAI, Gemini): we adopt the
  SDK's `provider.Completer` interface; provider impls in
  `internal/provider/openai_compat`, `deepseek`, `llmgateway`,
  `ollama_loopback` stay where they are.
- Migration of users off the legacy CLI. Phase 9 deletes
  `legacytui`; the opt-in env var stays until the next major
  release.
- New SDK packages beyond what B.1 lists.

---

## How this plan was produced

This plan is the result of two operations:

1. The UI Replacement Phases 4-10 carry over from the binding
   plan `docs/design/ui-replacement-phases.md` (lines 285+).
2. The SDK simplification thread is the output of a one-pass audit
   of every `mivialabs-agent/internal/` package against the SDK
   primitive it overlaps with. The audit cites file:line evidence
   for both the local mirror and the SDK primitive.

This is a working document: the ADLC plan/planner +
planner/reviewer + builder/reviewer loop applies to each phase above
individually. Each phase is a separate ADLC delivery, not a single
mega-delivery.

The `mivialabs/mivia-agent` repo's `AGENTS.md` and the
`mivialabs/mivialabs-sdk` repo's `AGENTS.md` are the canonical
sources for the rules each phase must follow. This file is a
planning artefact, not policy.