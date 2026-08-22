# UI Replacement (4-10) + mivia-ai-sdk Simplification

Repo: `/home/mac/projects/mivialabs/mivia-agent`, branch `dev`. Phase 0-3 of
the UI replacement and two hardening commits are already shipped (8 commits
ahead of `origin/dev`). The binding plan for Phase 0-3 is
`docs/design/ui-replacement-phases.md` lines 1-283.

This file is the working plan for everything that comes next, under a
single rule: **simplify, refactor, do not over-engineer.**

## The split: SDK vs CLI

`mivia-ai-sdk` (`github.com/MiviaLabs/mivia-ai-sdk`, sibling repo at
`/home/mac/projects/mivialabs/mivia-ai-sdk`) and `mivia-agent` (this CLI
repo) have a clean line between them:

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
  runtime needs lives here. The SDK has deliberate design choices
  (e.g. `schema.MaxSchemaBytes` is a `const`, not config; `secretpath`
  is glob-only; `tools.Tool` has a minimal 2-method interface); the
  plan does not propose to expand those surfaces.

- **`mivia-agent` is the product.** The `mivia` binary, the
  `mivia-ui` binary, the chat REPL, the new bubbletea-free UI, the
  `mivia.toml` config layer, the operator-visible redaction, the
  chat-block bubble rendering, the slash command set, the workstation
  identity, the agent/skills/sessions/worktrees filesystem layout.
  CLI-specific features stay in CLI. The CLI is the mivia product; it
  is the only call site that has a complete UX story, and the SDK
  exists to serve that story's primitives.

If a primitive has the same shape across products, it belongs in the
SDK. If a primitive is shaped by mivia's product (the `mivia.toml`
layout, the chat-block format, the operator redaction policy), it
stays in the CLI. The audit below classifies every local mirror
against this line.

Two threads are sequenced below:

- **Thread A** — UI Replacement Phases 4-10 (continuation of the binding
  plan).
- **Thread B** — `mivia-ai-sdk` simplification: an audit of every
  local mirror in `mivia-agent/internal/` against the SDK
  primitive it duplicates, with the deletions, the SDK extensions
  to propose upstream, and the cli-family cleanup that follows when
  the inner mirrors die.

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

**Precondition.** `cmd/mivia`'s launcher must have a documented entry
point in `internal/uiadapter`. The current `internal/uiadapter.New`
from Phase 3 is the entry. The launcher wires `--workspace` /
`MIVIA_*` env / config load to `uiadapter.New`'s `Input`.

**Scope.** In `cmd/mivia/main.go`, replace
`cli.SetTUILauncher(legacytui.RunTUI)` with a launcher that calls
`uiadapter.New` and hands the result to the new UI's conversation
screen. Keep `legacytui` reachable behind an opt-in escape hatch for
one release. Flip `cmd/mivia-ui`'s `--demo` default to false, since
real mode is now the primary path.

**Tests.** End-to-end real-mode smoke (manual acceptance on a
workstation with credentials; offline unit tests as far as they go).

**Edge-count projection.** Phase 8 adds at minimum:
- `cmd/mivia → internal/uiadapter` (1 edge).
- `cmd/mivia → internal/ui/*` for the new launch wiring (3-5 edges
  for app, screen/conversation, jsonout, stream).
- `cmd/mivia → internal/config` and `cmd/mivia → internal/provider`
  for the config-load + provider-build wiring (2 edges, mirrored
  from Phase 3's `cmd/mivia-ui`).
- Total projection: +6 to +8 edges. New count: ~422 to ~424 of 420.
  Re-baseline `edgeCap` to 425 with documented headroom.

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

## Thread B — `mivia-ai-sdk` simplification (the audit)

The audit is a one-pass read of every local mirror in
`mivia-agent/internal/` against the SDK's primitives. The SDK is
**not** yet a dependency of this module; nothing under `internal/`
imports it. The audit's output is a deletion list and an
SDK-extension list. Each deletion is gated on (a) the SDK having the
primitive and (b) the SDK having any extensions the local mirror
needs.

**Two corrections from the plan-reviewer's first round:**

1. The SDK module path is `github.com/MiviaLabs/mivia-ai-sdk` (not
   `mivialabs-sdk`); the sibling path is
   `/home/mac/projects/mivialabs/mivia-ai-sdk` (not `mivialabs-sdk`).
   Every reference in the original plan was wrong; the corrected
   references are in this rewrite.
2. The plan's "what is NOT in the plan" list (B.5) excluded a
   `sdkcompat`-style bridge. After the plan-reviewer's findings, a
   small `internal/sdkadapter` is reintroduced — but only where the
   type-shape mismatch is real, and the package is named for what it
   does (not "compat", which is too vague).

### B.0 — bring the SDK into the module (one-line commit, with sibling-repo pre-clear)

- Add `github.com/MiviaLabs/mivia-ai-sdk` to `go.mod`. The SDK has
  no semver tag; B.0 uses a `replace` directive pointing at the local
  path `/home/mac/projects/mivialabs/mivia-ai-sdk` with a comment
  in `go.mod` explaining the temporary state and the planned
  tag-pinning.
- **Sibling-repo pre-clear before B.0 lands.** The SDK has
  `policy/pending_wiring.json` (no-caller rule) and `api/<pkg>.txt`
  API locks checked by `scripts/check_orphan_packages.py` and
  `scripts/check_api.py`. B.0 is gated on:
  - Every package Thread B will use is `pending_wiring.json`-registered
    (target `mivia-agent` as the first caller).
  - `make api-update` runs in the SDK repo first if any new symbols
    the CLI will import are not yet locked. The PR lands the API
    diff alongside the B.0 commit (or, if no new symbols are needed,
    no PR is needed — most B.0 imports are of already-locked
    symbols).
- `go mod tidy` resolves cleanly. No cycles (the SDK's
  `modernc.org/sqlite` does not collide with `mivia-agent`'s
  `ncruces/go-strftime`; verify in B.0).
- No code uses the SDK yet. Build, vet, focused tests must still
  pass.

**Commit subject:** `build(deps): bring mivia-ai-sdk into the module`.

### B.0.5 — `internal/sdkadapter`: small bridge for type-shape mismatches

**Sequencing.** B.0.5 lands before B.1 because B.0.5's
`provider.go` and `tool.go` import SDK primitives whose surface
may be extended by B.1; if B.1 lands first, B.0.5 will define a
bridge that compiles against a narrower API and then has to be
edited when B.1's extensions merge. The order is right but the
rationale is undocumented; one sentence here is enough. The
same rationale applies to B.2 #8 (the inner-loop swap), which
must follow both B.0.5 and the B.1 extensions it consumes.

**Why this exists.** Three local mirrors (CLI's `tools.Tool`,
`skills.Definition`, `provider.Message`) have richer shapes than
their SDK counterparts. The CLI's `tools.Tool` has 8 methods; the
SDK's has 2. The CLI's `skills.Definition` has 17 fields; the SDK's
`Skill` has 4. The CLI's `provider.Message` and the SDK's
`provider.Message` are wire-compatible but distinct Go types
because they live in different modules.

A single small package `internal/sdkadapter` carries the bridges
(50-200 LOC per file). It is **not** a generic compat layer; it is
named for what it does and lives in one directory with a clear
shape. Its responsibilities:

- `internal/sdkadapter/tool.go` — wrap an SDK `tools.Tool` as a CLI
  `Tool`. The CLI's `Tool` interface has 8 methods across the base
  interface (`Name`, `Description`, `Parameters`, `Execute`) and
  four optional mix-ins (`EphemeralResultTool`,
  `EphemeralResultMarker`; `PrivilegedTool`, `Privileged`;
  `CapableTool`, `Capability`; `ResultBudgetTool`,
  `ResultBudgetBytes`) per `internal/tools/tools.go:21-80`. The
  SDK's `tools.Tool` has 2 methods (`Name`, `Run`); the SDK
  declares `SchemaTool` (4 methods), `ProfiledTool`,
  `ExecutionProfile`, `ResultBudgetTool`, `PrivilegedTool` per
  `api/tools.txt:32-58`. The bridge must forward all eight CLI
  methods and adapt the SDK's narrower surface:
  - `Name()` forwards directly (both sides have it).
  - `Description()` returns the CLI-config description registered
    alongside the bridge; the SDK has no per-tool description on
    `tools.Tool` (only `ParameterSchema() []byte` for tools that
    implement `SchemaTool`). The bridge's CLI-config table is
    the only place a description comes from.
  - `Parameters()` returns `SchemaOf(t)` (verified at
    `tools/schema.go:18`, returns `([]byte, bool)`) decoded from
    the SDK's parameter-schema bytes, or the CLI-config
    `Parameters` if the SDK side does not implement `SchemaTool`.
  - `Execute(ctx, args)` calls `SchemaTool.DecodeArguments`
    (`tools/schema.go:11`) when the SDK tool implements it,
    otherwise it wraps the raw args into `tools.InOut{Value: ...}`
    (`tools/tool.go:7-9`) and calls `Tool.Run` directly; the
    bridge unwraps `Out.Value` back into the CLI's
    `(string, error)` return.
  - `EphemeralResultMarker(args)` forwards to a CLI-side
    table-backed function registered on the bridge (no SDK
    analogue; the SDK's `ResultBudgetTool` covers a different
    concern).
  - `Privileged()` forwards to `tools.IsPrivileged(t)`
    (`api/tools.txt:15`).
  - `Capability(args)` forwards to `tools.ExecutionProfileOf(t)`
    (`api/tools.txt:14`) and translates the SDK's
    `ExecutionProfile` (`Class`, `ResourceKey`, `Timeout`) into
    the CLI's `Capability` (`Class`, `ResourceKey`, `Timeout`,
    `MaxResultBytes`).
  - `ResultBudgetBytes()` forwards to
    `tools.ResultBudgetOf(t)` (`api/tools.txt:18`).
  The file is expected to land near ~250-300 LOC because the
  eight method bodies and the two translation tables
  (`ExecutionProfile` → `Capability`, `Description` synthesis)
  exceed the 200-LOC cap. The plan accepts the overshoot; if
  the file grows past 300 LOC, split the `Capability` translator
  into `internal/sdkadapter/tool_capability.go`. Tests: round-trip
  a tool that implements the SDK's `SchemaTool` +
  `ProfiledTool` + `ResultBudgetTool` mix-ins and assert each
  of the eight CLI-side methods returns the expected value;
  round-trip a tool that does *not* implement `SchemaTool` and
  assert `Parameters()` returns the CLI-config fallback.
- `internal/sdkadapter/skill.go` — wrap an SDK `skills.Skill` as a
  CLI `skills.Definition` (forward `Name`, `Instructions`,
  `Triggers`; fill the rest from the CLI's product layer at the
  loader boundary).
- `internal/sdkadapter/provider.go` — type-conversion helpers
  between SDK `provider.Message` and CLI `provider.Message` (and
  equivalents for `Request`, `Response`, `Chunk`, `Usage`, `ToolCall`).
  Tests: round-trip a request with one user message, one assistant
  message, one tool call; assert the converted form round-trips.

Each bridge file has a single import-direction (SDK → CLI or CLI →
SDK) and a single responsibility. The package is the
"translate at the boundary, then forget it lives in two modules"
rule: every Thread B drop that lands at the SDK boundary uses one
of these bridges once, then the local mirror is gone.

**Why not the bigger "sdkcompat" package.** The plan-reviewer
flagged the original B.5 exclusion as wrong; I agree the exclusion
was over-broad. The user's "do not over-engineer" rule kills
package-wide bridges, not per-type bridges that exist at the seam.
This package is the per-type shape, three small files, no
compatibility shim, no version negotiation. If it grows past 200
LOC, split it.

**Tests.** Per bridge file, one round-trip test (build a value in
module A's type, convert to module B's type, assert equality or
field-by-field equivalence).

**Import-row addition.** `.mivia/policy/import-layers.json` gets a
new row: `internal/sdkadapter` → `internal/provider`, `internal/tools`,
`internal/uikit/ports` (or whatever the CLI's surface needs), plus
`github.com/MiviaLabs/mivia-ai-sdk/provider`, `.../tools`, `.../skills`.

**Edge-cap impact.** B.0.5 adds 5-7 edges (3 to CLI, 3-4 to SDK).
The cap is re-baselined upward; the per-drop drops in B.2 then
offset the increase.

**Commit subject:** `feat(agent): add sdkadapter bridges for type-shape mismatches`.

### B.1 — SDK extensions to propose upstream (one PR per extension, in the SDK repo)

Before any local mirror can drop, the SDK must have the missing
primitives. Each extension lands as a PR in
`github.com/MiviaLabs/mivia-ai-sdk`, is reviewed and merged there,
and then enables the corresponding CLI drop in B.2.

**The rule.** Every extension is justified by *general applicability*
(more than one non-mivia consumer) and *alignment with the SDK's
existing design choices* (the SDK's deliberate constant-vs-config
decisions, its minimal-interface choices, and its docstring-encoded
non-goals are NOT expanded). If a primitive is mivia-product-specific,
it stays in the CLI and gets a bridge in `internal/sdkadapter`.

| # | Local mirror | SDK primitive | Extension needed | Why this belongs in SDK (cited evidence) |
|---|---|---|---|---|
| B.1.1 | *(deleted — none of the proposed extensions are actually missing from the SDK)* | n/a | **No extension.** The SDK's `agentloop.Options` already exposes `HeartbeatInterval` (`agentloop/options.go:259-263`), `DedupWithinTurn` (`:254-258`), `TurnResultBudget` (`:264-278`), `ConcludeMargin`/`ConcludeNotice` (`:234-253`), and `Audit AuditFunc` (`:218-220`). The CLI's `internal/agent/options.go` carries analogues (`MaxToolResultChars`, `MaxToolCallsPerBatch`, `MaxConcurrentTools`, etc.) that the inner-loop swap at B.2 #8 already maps per-field onto the SDK options; no `With*` setter is needed because the SDK uses struct fields, not option-setter methods. The CLI's per-step scrub-on-registry-change and the spool-aware tool-result shaping remain CLI-only concerns (B.4). **Do NOT propose `WithRetryAfterPromptTooLong` (it is mivia's prompt-budget shape), `WithToolParallelism` (it is mivia's work-limit shape; the SDK's loop runs tools sequentially by design), or `WithHystereticPrune` (no evidence in either codebase).** | Verified at `agentloop/options.go:99-106` (sentinel errors for `ErrTurnResultBudget`, `ErrConcludeMargin`, `ErrHeartbeatRequiresBus`), `:218-220` (`Audit AuditFunc`), `:234-253` (`ConcludeMargin`, `ConcludeNotice`), `:254-258` (`DedupWithinTurn`), `:259-263` (`HeartbeatInterval`), `:264-278` (`TurnResultBudget`). The plan's previous "With*" setters were fabricated; the SDK uses struct fields and `Options.Validate()` directly. |
| B.1.2 | `internal/ledger` *the production-grade SQLite repo* | `mivia-ai-sdk/ledger` | **No new SDK extension; no new SDK subpackage.** The SDK already exposes `ledger.SQLiteStore` (`ledger/sqlite_store.go:54-90`, gated by the `ledger_sqlite` build tag) and `ledger.NewSQLiteStore(path)` (`:64`). The CLI's `StorageLedgerRepository` is replaced by `ledger.SQLiteStore` directly; the CLI no longer owns the SQLite repo. The four CLI-specific concerns named in the previous revision (`Recover`, `SetTaskAttempt`, `CompareAndSetTaskStatus`, `DisplayName`) are not on the SDK's `Store` interface (`api/ledger.txt:57-61` only declares `Load`, `CompareAndSwap`, `Range`) and are **not** proposed as extensions: they are CLI-side adapter logic in `internal/sdkadapter/ledger.go` and belong to the mivia product, not the SDK's contract. | Verified at `ledger/sqlite_store.go:53-90` (the `SQLiteStore` struct and `NewSQLiteStore`), `api/ledger.txt:38` (`func NewSQLiteStore(path string) (*SQLiteStore, error)`) and `:51-61` (the `SQLiteStore` and `Store` interface). Promoting the CLI's repo to a separate `ledger/sqlitestore` subpackage would duplicate a package the SDK already exposes under the `ledger_sqlite` build tag. |
| B.1.3 | `internal/hooks` *the registry surface* | `mivia-ai-sdk/hooks` | Add an external-program `Handler` variant: `HandlerFunc func(ctx, payload) (veto bool, err error)` already exists; add `ExternalHandler{ Path, Args, StdinJSON, ParsePermissionDecision(veto bool, err error) (Veto, error) }` so any Claude-Code-style runtime (not just mivia) can register external programs as lifecycle hooks. The CLI's `internal/hooks/exec.go` becomes one such handler, with the mivia-specific JSON schema parsing as a separate file. | Two or more non-mivia agent runtimes in the wild use external-program hooks (Claude Code, Cline, Continue). The SDK's `hooks/doc.go:1-10` advertises the lifecycle registry; an external-program handler is a generic pattern. **Bridge file** for B.2 #10: the CLI's `Runner.Run` (struct method on `internal/hooks.Runner`) becomes an SDK `Handler` (Go func signature `func(ctx context.Context, payload any) (bool, error)` at `hooks/registry.go:32`) via `internal/sdkadapter/hooks.go`. Shape (~30-40 LOC): a `handlerAdapter` struct holding `runner *hooks.Runner` and a `Handler()` method that calls `runner.Run`, returning `(allow, err)` in the SDK's shape. The Event/Point name mapping lives next to the bridge (see B.1.3-followup and finding #9 below); the handler func itself is point-agnostic. |
| B.1.4 | `internal/reasoning` *type definitions* (Level / Dialect / Setting) | `mivia-ai-sdk/provider` | Add `ReasoningBudget` (a 0..100 token-budget knob the provider can pass through) **only**. **Do NOT propose `FormatReasoningEfforts` — that is a UI-side presentational helper that belongs in the CLI's `internal/reasoning/reasoning.go` (the file survives the drop; the 7-value `Level` survives; the request-encoder does the Level → ReasoningEffort mapping).** **Do NOT propose to absorb the 7-level `Level` enum into `ReasoningEffort`'s 4 values — the SDK's policy is deliberately coarser; expanding it would change the user-visible config and any caller who mapped CLI's `XHigh` to SDK's `High` would silently lose fidelity. Keep CLI's `Level`; bridge at the request-encoder boundary.** | `ReasoningBudget` is a generic provider capability any model that supports thinking can consume. `FormatReasoningEfforts` is a presentational helper every model-facing surface needs, but it is mivia's model-specific surface (today it lives alongside `internal/reasoning`'s 7-value `Level`); making it SDK-side would couple the SDK to a 7-value vocabulary the SDK deliberately rejects (`provider/reasoning.go:9-22` documents the 4-value closed set). The 7-level / 4-level mismatch is product-specific. |
| B.1.5 | `internal/events` *the per-handler buffered subscription* | `mivia-ai-sdk/events` | Add per-handler buffered subscription: `Subscribe(name, opts SubscribeOptions) (Sub, error)` with `opts.BufferSize`, `opts.CloseOnPanic`. The current `Bus.Subscribe` is broadcast-only. **Do NOT propose `Delivery` (re-entrant subscription handle from inside a handler) or `Flush / Close / Unsubscribe` on a `Bus` — these are product patterns. The per-handler buffer is generic; the rest is mivia's REPL.** | The SDK's `events/bus.go:42-88` is broadcast-only (`Subscribe(name, h Handler) error`; `Emit` calls handlers inline under the mutex, with no per-subscriber buffer). A buffered subscription lets any caller consume events without blocking the emitter. Justified by *exclusion of broadcast-only `Subscribe`* in `events/bus.go:55-66` and by the SDK's own docstring commitment that "handlers for one event run in order" — a slow consumer in a synchronous broadcast can back-pressure the emitter. The previous "two non-mivia callers (recorder, sidecar, hub)" rationale cited mivia's own subsystems and is dropped; the extension is justified on leaf-package design grounds, not on call-site enumeration. |

**Extensions explicitly NOT proposed** (after the plan-reviewer's
review):

- `internal/mcp` redaction / schema-cap / tool-name-encoding
  extensions — the SDK's `mcp/client.go:21-30` has only
  `ClientOptions{Info, OnProgress}`; `schema.MaxSchemaBytes` is a
  hard `const` (deliberate, not config); tool-name encoding is
  mivia's product decision. These three become CLI-side
  `internal/sdkadapter/mcp.go` helpers, not SDK extensions.
- `internal/secretpath` substring-exceptions — the SDK chose glob by
  design (`secretpath/matcher.go:51-84` uses `path.Match`).
  Expanding it for one caller violates the SDK's closed-API
  principle. CLI keeps its own substring matcher.
- `internal/contextmgr` `WithHystereticPrune` — no evidence in either
  codebase. The CLI's calibration is already covered by SDK
  `contextplan.Calibrated`; no `WithCalibration` wrapper is needed
  (the field already exists in `agentloop.Options`).
- `internal/jschema` model-facing helpers (corrective messages,
  example generation) — these are model-specific and currently
  consume CLI's `internal/redact` policy. They stay in CLI as
  model-specific helpers.

Each B.1.x PR's body cites the local mirror's file:line as the
evidence of the missing primitive, and the SDK's `doc.go` as the
evidence of where the design choice is documented.

### B.2 — local-mirror drops (CLI simplifications)

After B.0 and B.0.5 and B.1's extensions land, the following local
mirrors are candidates for deletion. Each is one commit, gated by
`go build ./...` and `go test ./affected-pkg`. The order is
dependency-first: a package that another deletion depends on lands
first.

**Per-phase ADLC shape.** Each row below has the four-part shape the
next ADLC planner will need: a Goal, a Scope (which files move and
which stay), the API surface (what the migration touches), and the
Tests (which contract must hold after the drop).

#### Wave 1 (no upstream extension needed; SDK already has the primitive)

| Order | Local mirror | Replaces with | ADLC shape |
|---|---|---|---|
| 1 | `internal/durablefence` (test-only harness) | `mivia-ai-sdk/durablefence` | **Goal**: drop the local test-only conformance kit. **Scope**: delete `internal/durablefence/`; the SDK's `durablefence.Run` and `Check*` are the canonical four checks. **API**: same exported symbol set; the SDK's call sites replace the local one. **Tests**: the SDK's `durablefence_test/` is run in place of the local. |
| 2 | `internal/envfile` | `mivia-ai-sdk/envfile.Load` | **Goal**: drop the local env-file loader. **Scope**: delete `internal/envfile/`; the SDK's `envfile.Load` is wire-compatible. **API**: same; the SDK's `Lookup` helper (if it exists) covers the local's preference order; if not, the local's `Lookup` stays in CLI as a small wrapper around the SDK's `Load`. **Tests**: the SDK's `envfile_test/` is run in place of the local. |
| 3 | `internal/contentref` (adopt `sha256:<digest>`; **dual-format parsing during transition**) | `mivia-ai-sdk/contextstate.Mint` | **Goal**: drop the local content-ref minter; adopt the SDK's `sha256:<digest>` shape as canonical. **Scope**: delete `internal/contentref/`; the minter becomes `contextstate.Mint` (verified at `contextstate/ref.go:31-33`). The dual-format parser `Parse(ref string)` **does not move to the SDK** because `contextstate.Parse` does not exist (`api/contextstate.txt:26-28` only locks `Mint`, `Digest`, `IsRef`); the parser lives in the CLI as `internal/sdkadapter/ref.go` and accepts both `ref:kind:digest` and `sha256:digest` during the transition window. A follow-up commit removes the dual-format support once the `usage_events.content_ref` table is migrated. **API**: `Mint(data []byte) string` returns `sha256:<digest>` (the SDK's); `Parse(ref) (kind, digest string, err error)` lives in `internal/sdkadapter/ref.go` and is the single CLI-side migration helper. **Tests**: round-trip a byte slice through the SDK's `Mint`; the dual-format parser returns the same `digest` for both `ref:foo:abc...` and `sha256:abc...`; persisted-ref migration test (a `usage_events` row with `ref:output:abc` parses to the same content as a `sha256:abc` row). |
| 4 | `internal/contextstate` (every type and validator) | `mivia-ai-sdk/contextstate` | **Goal**: drop the local mirror of the SDK's contract types. **Scope**: delete `internal/contextstate/`; import the SDK's `contextstate` package directly. The CLI's `contracts.go` and `commit_validation.go` are byte-identical with the SDK's per `contextstate/doc.go:6-9`. **API**: every `internal/contextstate.X` becomes `mivia-ai-sdk/contextstate.X`. **Tests**: any local test that imported `internal/contextstate` switches to the SDK's import; the SDK's tests cover the contract. |
| 5 | `internal/reasoning` (Level / Dialect / Setting) | `mivia-ai-sdk/provider` reasoning types (the 4-value `ReasoningEffort`, the open `ReasoningDialect` typed string) | **Goal**: drop the local mirror of the SDK's reasoning types where the vocabulary overlaps. **Scope**: keep CLI's 7-level `Level` enum (it is the product's user-visible config vocabulary); the SDK's `ReasoningEffort` (4 values) is used at the request-encoder boundary only. `internal/reasoning.FormatReasoningEfforts` stays as a CLI-side helper in `internal/reasoning/reasoning.go` (per B.1.4's revision — `FormatReasoningEfforts` is mivia's presentational layer, not an SDK-side function). **API**: CLI's `Level` is the source-of-truth; the request encoder translates `Level → ReasoningEffort` at the call site (the mapping is product-specific, defined in `internal/sdkadapter/provider.go`). **Tests**: the existing `internal/reasoning` tests keep working; the bridge has its own tests. |
| 6 | `internal/usage` in-memory accumulation; keep `UsageRecord`/`UsageWriter` as the durable log schema | `mivia-ai-sdk/usage.Accumulator` | **Goal**: drop the local in-memory accumulation; keep the durable log schema. **Scope**: delete `internal/usage/accumulator.go`; `internal/usage/record.go` (the durable schema) stays. The SDK's `usage.Accumulator` (`usage/accumulator.go:1-30`) is the in-memory total; CLI's `UsageRecord` (the row written to SQLite) is the durable per-turn log. **API**: `accumulator.Total()` is the SDK's `usage.Accumulator.Total()`; `UsageWriter.Record(usageRecord)` is the local write path. **Tests**: the existing durable-schema tests keep working; the bridge has its own tests. |
| 7 | `internal/agentmsg.ContentRef` (call sites that mint a ref) | `mivia-ai-sdk/contextstate.Mint` | **Goal**: drop the duplicate content-ref minter; the call sites in `internal/agentmsg/message.go:241` use `contextstate.Mint`. **Scope**: the roll-in is part of the Wave 1 #3 drop; the dual-format parsing covers the persisted-data migration. **API**: `agentmsg.Message` keeps its current shape; only the `ContentRef` field's underlying minter changes. **Tests**: dual-format parse test (same as Wave 1 #3). |

#### Wave 2 (after B.1 extensions land)

| Order | Local mirror | Replaces with | ADLC shape |
|---|---|---|---|
| 8 | `internal/agent` (CLI's `Loop` stays; CLI's `Loop` is wrapped by an SDK `*Loop` adapter) | `mivia-ai-sdk/agentloop` (`New(opts Options) (*Loop, error)` is the entry point; the CLI's session loop constructs an `*Loop` and calls `(*Loop).Run` / `(*Loop).RunSteerable` / `agentloop.NewSteer` + `(*Steer).Trigger`) | **Goal**: keep `internal/agent` as the CLI's session-loop layer; swap the inner tool-call loop to the SDK's. **Scope**: `internal/agent/loop.go:104` `Loop.Run` is wrapped; the SDK's `(*Loop).Run(ctx, msgs []provider.Message) (Result, error)` (`agentloop/loop.go:42-43`, `api/agentloop.txt:23`) is the lower-level driver. CLI's `Options` keeps its existing fields (the `WithHeartbeat`/`WithToolScrubOnRegistryChange`/`WithToolResultSpool` fields proposed in the prior revision did not exist and are deleted per B.1.1); the inner-loop swap maps per-field onto `agentloop.Options` (verified at `agentloop/options.go:167-279`). Steering uses `agentloop.NewSteer() (*Steer, error)` and `(*Steer).Trigger()` (`api/agentloop.txt:26, 29`); the CLI's `Options.InterruptCh` / `MailboxPending` / `WatchdogInterval` get bridged to `*Steer.Trigger` calls at the boundary. The CLI's `internal/runtime.Dispatcher` is a separate package and is NOT in this drop (see B.4 for the residual gap). **API**: `Loop.Run(ctx, prompt, writer)` continues to work; under the hood, it constructs an `*Loop` from `agentloop.Options` and calls `(*Loop).Run`. **Tests**: every `internal/agent` test that runs the loop must still pass with the SDK-backed inner loop; the leak test from Phase 2 still passes; the `*Loop`'s own tests cover the SDK side. **Commit subject**: `refactor(agent): back internal/agent with mivia-ai-sdk/agentloop`. **Pre-Phase-8 gate**: this drop is a precondition for Phase 8. If the inner loop is not SDK-backed by Phase 8, the cutover falls back to the legacy loop. |
| 9 | `internal/ledger` (CLI's `StorageLedgerRepository` is replaced by `ledger.SQLiteStore`; `MemoryLedgerRepository` drops) | `mivia-ai-sdk/ledger` | **Goal**: drop the local ledger; the CLI no longer owns the SQLite repo. **Scope**: delete `internal/ledger/storage.go`, `internal/ledger/memory.go`, `internal/ledger/memory_claims.go`, `internal/ledger/storage_recovery.go`. The CLI imports `github.com/MiviaLabs/mivia-ai-sdk/ledger` and uses `ledger.NewSQLiteStore(path) (*SQLiteStore, error)` (`api/ledger.txt:38`) gated by the `ledger_sqlite` build tag. The mivia-specific `Recover`/`SetTaskAttempt`/`CompareAndSetTaskStatus`/`DisplayName` adapter layer lives in `internal/sdkadapter/ledger.go` (CLI-side; not on the SDK's `Store` interface per `api/ledger.txt:57-61`). **API**: same exported surface where the SDK already has it (the CLI's adapter fills the gaps); the SDK's `ledger.Store` is unchanged (Load / CompareAndSwap / Range only). **Tests**: the SDK's `ledger/sqlite_store_test` (run in the SDK repo) covers the migration; the CLI's integration tests assert the same observable behaviour; the adapter has its own round-trip tests. |
| 10 | `internal/hooks` (split: registry shape to SDK; executor stays in CLI; Event/Point vocabulary bridged at the Fire call sites) | `mivia-ai-sdk/hooks` | **Goal**: split the registry shape from the executor. **Scope**: `internal/hooks/config.go` (the registry) and `internal/hooks/exec.go:47` (the JSON-I/O executor) split. The registry moves to the SDK repo per B.1.3; the executor stays. The CLI's executor is one `ExternalHandler` impl; the SDK is the registry. **Event/Point vocabulary (decided here, applied at the bridge)**: the CLI's `Event` names (`PreToolUse`, `PostToolUse`, `Stop` per `internal/hooks/config.go:27-31`) are the source of truth because the user configures hooks by name in `mivia.toml` (`internal/hooks/config.go:111-139`'s `deferredEvents` map explicitly references Claude Code config keys like `"PostToolUseFailure"`, `"SessionStart"`, etc., proving the user-visible vocabulary is the CLI's). The SDK's `Point` constants (`PointPreTool`, `PointPostTool`, `PointStop` per `api/hooks.txt:2-4`) are bridged at the four `Fire` call sites: a 10-20 LOC table in `internal/sdkadapter/hooks.go` mapping `Event` → `Point` for the three names in `V1Events()`. Any future vocabulary expansion (e.g. `PointSessionStart`) requires a CLI-side `EventSessionStart` declaration first; the bridge is the single point of change. **API**: CLI's `internal/hooks/exec.go:79` `Run` becomes a `Handler` impl that the SDK's `Registry` calls (per the bridge in B.1.3). The `Fire` call sites take an `Event` and translate to `Point` before invoking `hooks.Registry.Fire`. **Tests**: the SDK's `hooks/external_test` covers the registry shape; the CLI's `internal/hooks/exec_test` covers the JSON-I/O parsing; the bridge has a 3-row table test (each V1 event maps to the right `Point`; an unknown event returns the SDK's `Point.Validate` error wrapped with the CLI event name). |

#### Partial drops (CLI keeps the product-specific part, drops the generic part)

| Order | Local mirror | What moves to SDK | What stays in CLI | Commit subject |
|---|---|---|---|---|
| 11 | `internal/mcp` | (no SDK extension; the SDK's `mcp.Client` is the canonical client) | The redaction, schema-byte-cap, and tool-name-encoding wrappers live in `internal/sdkadapter/mcp.go` and wrap the SDK's `mcp.Client`; the CLI's `internal/mcp/` deletes. | `refactor(deps): remove internal/mcp superseded by mivia-ai-sdk` |
| 12 | `internal/tools` (the `Tool` interface and registry) | The `Tool` interface and registry (already in `mivia-ai-sdk/tools`) | The CLI's 30+ tool impls (which need a richer interface than the SDK's 2-method one) bridge through `internal/sdkadapter/tool.go`; the CLI's `cappedBuffer` (stream-output capture), `glob_match.go` (Go file globbing), `file_observation.go` (file read) stay. | `refactor(deps): remove internal/tools type definitions superseded by mivia-ai-sdk` |
| 13 | `internal/workspace` (the sandbox core) | The sandbox (`os.Root`-based, already in `mivia-ai-sdk/workspace`) | The `mivia`-specific namespacing (`AgentsPath`, `SkillsDir`, `SessionsDir`, `WorktreesDir`, `ContextStorePath`, `MemoryDBPath`) | `refactor(deps): remove internal/workspace sandbox superseded by mivia-ai-sdk` |
| 14 | `internal/skills` (the in-memory registry) | The in-memory registry and `Skill` type (already in `mivia-ai-sdk/skills`) | The file loader (`loader.go`, `skill_markdown.go`) and the resource-snapshot subsystem (`resources.go`); the bridge through `internal/sdkadapter/skill.go` | `refactor(deps): remove internal/skills registry superseded by mivia-ai-sdk` |

#### Out-of-scope (intentional non-drops)

These packages have no SDK analogue and are kept as-is: `internal/agents`
(CLI's own agent-resolution layer), `internal/composition`,
`internal/diff`, `internal/faultinject`, `internal/legacytui`,
`internal/miviaauth`, `internal/providerregistry`, `internal/ui`,
`internal/uiadapter`, `internal/workflows/*`.

The cli-family (`cliworktree`, `clichat`, `cliagents`,
`cliworkflow`, `cliorchestrate`) is also out of scope as a
*drop target* — they are CLI command surfaces, not SDK mirrors.
They become thinner in B.3 / Phase 10.

### B.3 — cli-family simplification

After B.2's drops, the cli-family is smaller because the
cli-family's inner dependencies are smaller. The cli-family itself
is **not** in the drop list above (none of it duplicates an SDK
primitive), but it *composes* the dropped mirrors; dead code paths
in the cli-family become visible only after the mirrors die. Phase
10 of Thread A combines the cli-family collapse with the remaining
dead-set audit. The two threads merge at the Phase 10 commit.

### B.4 — gap admissions (the things the SDK doesn't have and the CLI keeps)

The audit found three local systems that have **partial** SDK
analogues. The SDK's `agentloop.Options` already covers three
concerns the prior plan revision claimed were SDK-gaps (per-tool
output ceiling, audit target, graceful conclude); the CLI's
adapter layer maps them per-field at the B.2 #8 inner-loop swap.
The remaining four CLI-side concerns stay in the CLI and are not
in scope for SDK extension.

- `internal/runtime.Dispatcher`: the SDK's `(*Loop).Run` runs tools,
  but the CLI's `runtime.Dispatcher` has features the SDK doesn't,
  after B.1.1's re-statement. The SDK's `Options` already covers
  three concerns the prior revision claimed were SDK-gaps:
  - **Per-tool output ceiling** — the SDK has
    `tools.ResultBudgetOf(t Tool) (int, bool)` (`api/tools.txt:18`)
    for one tool's bound, and `agentloop.Options.TurnResultBudget`
    (`agentloop/options.go:264-278`) for the batched result shape.
    The CLI's `internal/runtime/output_ceiling.go` (the
    per-call byte backstop) and `internal/agent/options.go`'s
    `BatchResultBudgetBytes` field map onto these.
  - **Audit preview** — the SDK's `Options.Audit AuditFunc`
    (`agentloop/options.go:218-220`) gives the audit target per
    call. The CLI's `Metadata.OutputPreview` and `OutputHash`
    (`internal/runtime/result.go:29-30`) are *CLI-side presentation
    fields* on `runtime.Result`, not an SDK contract.
  - **Graceful conclude** — the SDK's `Options.ConcludeMargin` and
    `StopConcluded` (`agentloop/options.go:234-253`, `api/agentloop.txt:17`)
    are the canonical pattern; the CLI's `MaxIterations`-based
    notice in the inner loop is replaced by `ConcludeMargin` per
    the per-field map in B.2 #8.
  The four remaining CLI-side concerns stay in the CLI:
  - **id-keyed dedup across steps** — the SDK's `DedupWithinTurn`
    (`agentloop/options.go:254-258`) dedups within one turn by
    (tool, canonical-arguments); the CLI's
    `internal/runtime/dispatcher_id_dedup.go` keys on
    `(request.ID, request.Step)` for cross-step and
    cross-generation dedup with waiter fan-in. Different invariant.
  - **hook runs** — the CLI records `runtime.Result.HookRuns`
    (`internal/runtime/dispatcher.go:63-67`) for the operator's
    view (a hook that ran and said nothing appears here and
    nowhere else). The SDK's `hooks.Registry.Fire` does not
    return a run-record; it returns `(allow, err)` per handler.
  - **work limits** — the CLI's `runtime.WorkLimits` are a session
    contract; the SDK has no equivalent.
  - **the dispatcher-as-`Handler` pattern** — the CLI's
    `runtime.Dispatcher` is the SDK `Handler`'s payload side; the
    bridge in `internal/sdkadapter/hooks.go` (B.1.3, B.2 #10)
    makes the dispatcher's reserved-invocation state visible to
    the hook. The pattern itself stays CLI-side because the
    reservation primitive is CLI-side.
  The CLI's `internal/runtime/` is not in the drop list.
- `internal/agent` (the larger session-loop layer): the CLI's
  `Loop` is the layer that composes `agentloop.New(...).Run(...)`
  with the session's tool admission (`runtime.Dispatcher`),
  model binding, autosave, and checkpointing. Drop #8 is *wrap*
  the inner loop, not delete the layer.
- `internal/coordinator` (run-level orchestrator with
  idempotency-keyed admission, claim-lease heartbeat, referral
  spawn, mailboxes): no SDK analogue. The SDK's `subagent` is a
  tool surface (subagents as tools), not a run orchestrator. Keep
  the CLI's `internal/coordinator`.

### B.5 — what is NOT in this plan (per the "do not over-engineer" rule, and per the plan-reviewer's first-round findings)

- **No full chat-REPL rewrite.** B.2 #8 (Wave 2) wraps
  `internal/agent.Loop` to use the SDK's `agentloop.New(opts) (*Loop, error)`
  plus `(*Loop).Run` / `(*Loop).RunSteerable` / `agentloop.NewSteer`
  + `(*Steer).Trigger`; the REPL's structure (slash commands, bubble
  rendering, dispatcher) stays. The diff is the inner-loop swap
  plus the call-site updates; the surrounding REPL is unchanged.
- **No SDK-backed `Conversation` adapter.** The Phase 2
  `Conversation` wraps `*chat.Session`. It does NOT need a sibling
  `SDKConversation` wrapping `mivia-ai-sdk/agentloop.Loop`;
  the Phase 8 cutover delegates the entire chat path to the
  SDK-backed loop the cli-family owns, and the new UI consumes
  that loop's output through the same uievent stream.
- **No `sdkcompat` package.** The plan-reviewer found that an
  `sdkcompat` is necessary for the type-shape mismatches in
  drops #8 (inner-loop swap), #10 (hooks split), and the partial
  drops #11-#14 (mcp, tools, workspace, skills); the
  user-flagged "do not over-engineer" rule kept me from including
  one. The plan now has a small per-type-shape `internal/sdkadapter`
  package (B.0.5) that is named for what it does and lives in one
  directory; this is the narrowest form of bridge that resolves
  the reviewer's findings. The package stays under 200 LOC per
  file where possible; `tool.go` lands near 250-300 LOC because
  it must forward eight CLI methods (B.0.5), and is split if it
  grows past 300 LOC.

---

## Sequencing (merged view)

| Order | Phase | Thread | Subject |
|-------|-------|--------|---------|
| 1 | B.0 | SDK | bring mivia-ai-sdk into the module |
| 2 | B.0.5 | SDK | add sdkadapter bridges for type-shape mismatches |
| 3 | B.1 | SDK | (multiple sibling-repo PRs) extensions land upstream |
| 4 | B.2 #1-#7 | SDK | Wave 1 drops (no upstream extension needed) |
| 5 | Phase 4 | UI | tool approval gating in uiadapter |
| 6 | Phase 5 | UI | session store port in uiadapter |
| 7 | B.2 #8-#10 | SDK | Wave 2 drops 8-10 (agent, ledger, hooks) |
| 8 | Phase 6 | UI | command runner port in uiadapter |
| 9 | Phase 7a | UI | general, provider, MCP settings ports |
| 10 | B.2 #11-#14 | SDK | partial drops (mcp, tools, workspace, skills) |
| 11 | Phase 7b | UI | agent and automation settings ports |
| 12 | B.3 | UI | cli-family simplification (post-mirror-collapse) |
| 13 | Phase 8 | UI | launch the new UI from cmd/mivia (**gated on B.2 #8**) |
| 14 | Phase 9 | UI | delete legacytui |
| 15 | Phase 10 | UI | collapse packages orphaned by the UI replacement |

The order is suggested, not fixed. Any phase whose preconditions are
met may run. The edge-cap re-baseline at each phase is the
rate-limiter; if a phase's planned cap exceeds the measured count,
re-baseline before proceeding.

The Phase 8 gate is explicit: Phase 8 cannot land until B.2 #8 (the
SDK-backed inner loop) has shipped. If B.2 #8 is removed or fails,
Phase 8 falls back to the legacy loop. The next planner must
surface this dependency in the Phase 8 plan.

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
   of every `mivia-agent/internal/` package against the SDK
   primitive it overlaps with. The audit cites file:line evidence
   for both the local mirror and the SDK primitive.

This plan was reviewed by the ADLC plan-reviewer (one round) and
amended. The reviewer's findings are recorded in the commit body
that introduces this file; the amendments are the corrections in
this rewrite. A second review round is queued; the next agent that
picks up any Thread A or Thread B phase should treat this file as
the master sequence.

The `mivia-agent` repo's `AGENTS.md` and the `mivialabs-sdk` repo's
`AGENTS.md` are the canonical sources for the rules each phase
must follow. This file is a planning artefact, not policy.