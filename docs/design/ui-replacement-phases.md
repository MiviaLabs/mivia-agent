# UI Replacement — Phased Integration Plan

Repo: `/home/mac/projects/mivialabs/mivia-agent`, branch `dev`.

Replace `internal/legacytui` with the new UI (`internal/ui/*`, `internal/uikit/*`),
one shippable phase at a time. Each phase is an independent commit that leaves
the tree green.

---

## Architecture decision (settled — do not relitigate)

The new UI sits on `internal/chat` + `internal/agent` + `internal/tools`
**through `internal/uikit/ports`**. It must never import any cli-family
package (`internal/cli`, `clichat`, `cliagents`, `cliworkflow`,
`cliorchestrate`, `cliworktree`, `legacytui`).

Those cli-family packages are the *old* front end's composition. They get
deleted alongside `legacytui`, not consumed by the new UI.

The adapter that bridges domain to UI lives in a new package,
`internal/uiadapter`. It imports domain packages and implements `ports.*`.
Direction of dependency:

```
cmd/mivia-ui ──> internal/ui/*      (presentation)
             └─> internal/uiadapter (implements ports over real backends)
                      └─> internal/composition, chat, agent, tools, config, storage
internal/ui/* ──> internal/uikit/ports, uievent   (never uiadapter)
```

`internal/ui/*` depends on the `ports` interfaces only. `uiadapter` is
injected at `cmd` level. This keeps the UI testable against `demoharness`
forever.

---

## Current state (verified)

- New UI already imports **zero** cli-family packages.
- `legacytui` is imported by exactly one file: `cmd/mivia/main.go`.
  Deleting it is a 3-line change plus the package removal.
- `cmd/mivia-ui` runs entirely against `internal/uikit/demoharness`
  (scripted conversations). `--demo` is on by default; no other mode exists.
- `internal/uikit/ports` defines: `Conversation`, `TurnHandle`,
  `CommandRunner`, `Approver`, `SessionStore`, `SubagentThreads`,
  `GeneralSettings`, `AgentSettings`, `MCPSettings`, `ProviderSettings`,
  `AutomationSettings`.
- `internal/uikit/uievent` defines 13 event kinds: `turn.start`,
  `text.delta`, `text.end`, `reasoning.delta`, `tool.pending`, `tool.start`,
  `tool.output`, `tool.end`, `plan`, `notice`, `usage`, `error`, `turn.end`.
- `internal/agent` emits ~18 `EventKind` values that must map onto those 13.
- `composition.BuildSession(SessionInput) (*chat.Session, *storage.SQLite,
  contextstate.Principal, error)` already exists and imports no cli package.
  This is the session constructor the adapter uses.
- `chat.Session.SwapOnAgentEvent(handler func(agent.Event)) func(agent.Event)`
  is the event tap.

---

## Standing rules for every phase

- Never use `--no-verify`. The pre-commit hook must pass on its own.
- `internal/ui/*` and `internal/uikit/*` must never import `internal/uiadapter`
  or any cli-family package. Verify each phase:
  ```bash
  go list -deps ./internal/ui/... ./internal/uikit/... | \
    grep -E 'internal/(cli|clichat|cliagents|cliworkflow|cliorchestrate|cliworktree|legacytui|uiadapter)$'
  ```
  Must return nothing.
- `internal/uiadapter` must never import a cli-family package. Verify:
  ```bash
  go list -deps ./internal/uiadapter/... | \
    grep -E 'internal/(cli|clichat|cliagents|cliworkflow|cliorchestrate|cliworktree|legacytui)$'
  ```
  Must return nothing.
- Every new package needs a row in `.mivia/policy/import-layers.json`.
- Every exported symbol needs a doc comment starting with the symbol name.
- Files ≤ 500 lines, functions ≤ 80 lines (hard gate).
- No TODO/FIXME/HACK/XXX in comments (semgrep blocks the commit).
- The demo path must keep working after every phase. `demoharness` is the
  contract test for `ports`; if a phase changes a `ports` interface, the
  demo implementation changes in the same commit.

Standard gate block:

```bash
cd /home/mac/projects/mivialabs/mivia-agent
gofmt -l <touched dirs>
go build ./...
go vet ./...
go test ./... -count=1
python3 scripts/check_go_structure.py --strict --all
python3 scripts/check_import_layers.py
make verify
```

---

# PHASE 0 — Get `make verify` green

**Why first:** every later phase inherits this gate. Starting integration on a
red `make verify` means you cannot tell whether your new work broke something.

**Problem:** `make diff-coverage` fails on uncovered changed lines in
`internal/workflows/localengine` (`engine_stack.go`, `worktree.go`). These come
from commits `780a9bd9` and `cc01c00e` (delivery-admission hardening), which
predate the UI work entirely.

**Secondary problem:** intermittent test failures in `internal/legacytui` and
`internal/clichat` that pass in isolation but fail during full-suite runs.
Shared state or ordering dependency, not a logic bug.

**Task:**

1. Run `make diff-coverage` and collect the exact uncovered line list.
2. For each uncovered line, read the enclosing function and add a real test
   that exercises that path. Do not add assertion-free tests to game the gate.
3. Reproduce the flaky failures with repeated full-suite runs:
   ```bash
   for i in 1 2 3 4 5; do go test ./internal/legacytui/... ./internal/clichat/... -count=1 2>&1 | tail -3; done
   ```
   Find the shared state (package-level var, temp dir reuse, env var, or a
   `t.Setenv` interacting with parallel tests) and fix the leak.
4. `make verify` must exit 0.

**Definition of done:** `make verify` exits 0, twice in a row.

**Commit:** `test(quality): cover localengine admission paths and fix suite ordering`

---

# PHASE 1 — `internal/uiadapter` skeleton + event translation

**Goal:** a pure, fully-tested translation layer from `agent.Event` to
`uievent.Event`. No I/O, no session, no UI. This is the highest-risk mapping
in the whole integration, so it lands alone with dense tests.

**IMPORTANT — import cap:** `check_import_layers` currently reports
**398 edges against cap 400**. Adding `internal/uiadapter` will exceed it.
Re-baseline the cap in this phase: measure the real count after the package
lands, set `edgeCap` to that plus a small documented margin, and update the
`description` field to state the new measured number and why it moved.
Do this deliberately as part of the commit, not as a reactive patch.

**Task:**

1. Create `internal/uiadapter/` with an `event.go` holding a single exported
   function:
   ```go
   // TranslateEvent converts one agent.Event into zero or more uievent.Events.
   func TranslateEvent(ev agent.Event) []uievent.Event
   ```
   Zero-or-more because some agent events (heartbeat, prune, step) have no UI
   representation and must be dropped, and some may fan out.

2. Map every `agent.EventKind` explicitly. Read
   `internal/agent/event.go` for the full list and the `Event` struct fields.
   The 18 source kinds are: `assistant`, `tool_start`, `tool_end`, `step`,
   `heartbeat`, `prune`, `tool_parallel`, `subagent_start`, `subagent_end`,
   `subagent_heartbeat`, `subagent_done`, `thinking`, `hook`, `compaction`,
   `cache_usage`, `token_usage`, `work_limit`.

   Target kinds are the 13 in `internal/uikit/uievent/event.go`.

   Every source kind must be handled by an explicit case. A `default:` that
   silently drops unknown kinds is forbidden — use an explicit
   "no UI representation" case list so a newly added agent event fails the
   switch exhaustiveness test rather than vanishing.

3. Add `internal/uiadapter/event_test.go` with a table-driven test that has
   **one row per `agent.EventKind`**, asserting the exact translated output
   (kind, body type, and key fields). Include a test that fails if a new
   `agent.EventKind` constant is added without a corresponding case — e.g.
   enumerate the known kinds in the test and assert the switch covers them.

4. Add `.mivia/policy/import-layers.json` row for `internal/uiadapter`.
   At this phase its only imports should be `internal/agent` and
   `internal/uikit/uievent`.

**Invariants:**
- `internal/uiadapter` imports no cli-family package.
- `internal/uiadapter` does not import `internal/ui/*`.
- No `default:` case that silently swallows unknown event kinds.

**Definition of done:** every `agent.EventKind` has an explicit, tested
mapping. `make verify` green.

**Commit:** `feat(ui): add uiadapter event translation from agent to uievent`

---

# PHASE 2 — `Conversation` and `TurnHandle` over a real `chat.Session`

**Goal:** implement the two core `ports` interfaces against a real session.
Still no UI wiring — this phase is verified by tests only.

**Task:**

1. Add `internal/uiadapter/conversation.go` implementing:
   ```go
   ports.Conversation  // Send, History, Model, ContextUsage, Title
   ports.TurnHandle    // ID, Events() <-chan uievent.Event, Cancel
   ```

2. `Send` must:
   - Start the agent turn on the wrapped `*chat.Session`.
   - Install an event tap via `chat.Session.SwapOnAgentEvent`, feeding each
     `agent.Event` through `TranslateEvent` from Phase 1 and pushing results
     onto the `TurnHandle`'s channel.
   - Restore the previously installed handler when the turn ends
     (`SwapOnAgentEvent` returns the prior handler — do not drop it).
   - Close the events channel exactly once on turn end, including on error
     and on `Cancel`.

3. `Cancel` must cancel the turn's context and cause the events channel to
   close. It must be safe to call after the turn already ended.

4. Map `chat.Session.ContextUsage()` onto `ports.Usage`, and the session's
   model/provider onto `ports.ModelInfo`.

5. Tests must cover, using a scripted fake completer (see how
   `internal/clichat` and `internal/cliagents` build test sessions — a
   `scriptedCompleter` pattern already exists in the tree, read it and use
   the same approach; do not import those packages, duplicate the helper):
   - A full turn: `turn.start` … deltas … `turn.end`, channel closed once.
   - `Cancel` mid-turn closes the channel and does not panic.
   - `Cancel` after turn end is a no-op.
   - Concurrent `Send` on the same conversation is either serialized or
     returns a clear error — pick one, document it, and test it.
   - The prior `SwapOnAgentEvent` handler is restored after the turn.

**Invariants:**
- No goroutine leak: every `Send` path closes its channel and returns its
  goroutine. Add a test using `runtime.NumGoroutine` deltas or a
  `sync.WaitGroup` with timeout.
- Channel close happens exactly once (guard with `sync.Once`).

**Definition of done:** `go test ./internal/uiadapter/... -race -count=1`
green, including the goroutine-leak and double-close tests.

**Commit:** `feat(ui): implement Conversation and TurnHandle over chat.Session`

---

# PHASE 3 — First light: real mode in `cmd/mivia-ui`

**Goal:** one real conversation, end to end, in the new UI. This is the
milestone that proves the whole seam.

**Task:**

1. Add `internal/uiadapter/build.go` with a constructor that assembles a real
   conversation from config:
   ```go
   // New builds a real ports.Conversation from resolved config.
   func New(ctx context.Context, in Input) (*Adapter, func(), error)
   ```
   It calls `composition.BuildSession(...)`. Read `composition.SessionInput`
   for the required fields. Return a cleanup func that closes the storage
   handle and any MCP managers.

2. In `cmd/mivia-ui/main.go`, add a `--demo=false` real path (keep `--demo`
   defaulting to true for now). When demo is off, build the adapter and pass
   it to the conversation screen in place of the demo harness.

3. Config loading: reuse whatever `cmd/mivia` uses to resolve config
   (`internal/config`). Do not reimplement resolution.

4. Manual acceptance (document the result in the commit body):
   ```bash
   go run ./cmd/mivia-ui --demo=false
   ```
   Send one message, get a streamed response, see usage update, exit cleanly.

**Invariants:**
- `--demo` (default) path is unchanged and still works.
- `internal/ui/*` gained no new imports. Only `cmd/mivia-ui` learns about
  `uiadapter`.

**Definition of done:** a real streamed conversation renders in the new UI.
Both `--demo` and `--demo=false` work.

**Commit:** `feat(ui): wire real conversation mode into cmd/mivia-ui`

---

# PHASE 4 — `Approver`: tool approval gating

**Goal:** tools that need approval prompt in the new UI and honour the answer.

**Task:**

1. Implement `ports.Approver` in `internal/uiadapter`:
   `Pending() <-chan ports.ApprovalRequest` and
   `Resolve(id string, decision ports.Decision)`.

2. Bridge to the real approval mechanism. Find how tool approval gating works
   today — start from `internal/tools` and `internal/runtime.Dispatcher`, and
   look at how `internal/clichat` currently surfaces approvals (read it for
   behaviour, do not import it).

3. `TranslateEvent` must emit `uievent.KindToolPending` for tools awaiting
   approval, carrying the request ID that `Resolve` expects.

4. Tests:
   - Approve → tool runs, `tool.start`/`tool.end` follow.
   - Deny → tool does not run, the turn continues or errors per existing
     semantics (match what the CLI does today).
   - Resolve with an unknown ID is a safe no-op.
   - Turn cancelled while an approval is pending does not deadlock.

**Definition of done:** approvals work end to end in `--demo=false`, verified
manually with a tool that requires approval. Deadlock test passes under
`-race`.

**Commit:** `feat(ui): implement tool approval gating in uiadapter`

---

# PHASE 5 — `SessionStore`: list, load, save

**Goal:** session persistence works in the new UI.

**Task:**

1. Implement `ports.SessionStore` (`List`, `Load`, `Save`) and populate
   `ports.SessionMeta` / `ports.SessionSummary`.

2. Back it with the real store. `composition.BuildSession` already returns a
   `*storage.SQLite`; `chat.Session` has `SetSessionStore(store, mgr)`.
   Read `internal/chat`'s `SessionStore` and `SaveManager` and bridge to them.

3. Tests: list on empty store, save then list, load a saved session and
   confirm history is restored, load a missing session returns a clear error.

**Definition of done:** sessions round-trip through the new UI.

**Commit:** `feat(ui): implement session store port in uiadapter`

---

# PHASE 6 — `CommandRunner`: slash commands

**Goal:** slash commands work in the new UI.

**Task:**

1. Implement `ports.CommandRunner`: `Run`, `SelectModel`, `SelectAgent`,
   `SelectSession`, each returning `ports.CommandOutcome`.

2. Decide the command set deliberately. Do not port all of the CLI's slash
   commands mechanically. List the commands the new UI will support in the
   commit body, and note which CLI commands are intentionally dropped.

3. Read `internal/clichat`'s slash handling for behaviour reference only —
   do not import it. Commands that manipulate the session (model switch,
   agent switch, compact) go through `chat.Session` methods directly
   (`SetReasoningEffort`, `Compact`, `SetAgentSettings`, and so on).

4. Tests: one per supported command, asserting the `CommandOutcome`; unknown
   command returns a clear outcome rather than an error panic.

**Definition of done:** the documented command set works in `--demo=false`.

**Commit:** `feat(ui): implement command runner port in uiadapter`

---

# PHASE 7 — Settings ports

**Goal:** the settings screens work against real config.

`ports` defines five settings interfaces: `GeneralSettings`, `AgentSettings`,
`MCPSettings`, `ProviderSettings`, `AutomationSettings`. That is a large
surface — split this phase into two commits.

### 7a — General, Providers, MCP

These map onto `internal/config` and `internal/mcp` reasonably directly.
Implement `GeneralSettings`, `ProviderSettings`, `MCPSettings`. Honour
`ports.Scope` (the edits are scoped — read `settings.go` for the semantics)
and return real `SaveHandle`s whose `Events()` channel reports save progress.

**Commit:** `feat(ui): implement general, provider, and MCP settings ports`

### 7b — Agents and Automations

`AgentSettings` maps onto `internal/agents`. `AutomationSettings` is the
largest surface (triggers, schedules, runs, watch). Check whether a real
backend for automations exists yet — if it does not, say so explicitly and
either keep the demo implementation wired for that one port or defer the
phase. Do not invent a backend to satisfy the interface.

**Commit:** `feat(ui): implement agent and automation settings ports`

---

# PHASE 8 — Cutover: `cmd/mivia` uses the new UI

**Goal:** the shipped binary launches the new UI.

**Task:**

1. In `cmd/mivia/main.go`, replace `cli.SetTUILauncher(legacytui.RunTUI)` with
   a launcher that starts the new UI through `uiadapter`.
2. Keep `legacytui` reachable behind an opt-in escape hatch for one release
   (an env var or a hidden flag), so a regression is recoverable without a
   rebuild. Document the escape hatch in the commit body.
3. Flip `cmd/mivia-ui`'s `--demo` default to false, since real mode is now
   the primary path.

**Definition of done:** `go run ./cmd/mivia` opens the new UI and a full
session works: send, stream, tool call with approval, save, reload.

**Commit:** `feat(ui): launch the new UI from cmd/mivia`

---

# PHASE 9 — Delete `legacytui`

**Goal:** remove the old TUI.

**Precondition:** Phase 8 has been running without regression long enough that
you are ready to drop the escape hatch. That is a judgement call — confirm
before starting.

**Task:**

1. Remove the escape hatch from Phase 8.
2. `git rm -r internal/legacytui`.
3. Remove the `internal/legacytui` import and `SetTUILauncher` call from
   `cmd/mivia/main.go`.
4. Remove `internal/cli/tui_launcher.go` and the `tuiLauncher` func var if it
   now has no other consumer.
5. Remove the `internal/legacytui` row from `.mivia/policy/import-layers.json`
   and any allow-list entry naming it.
6. Delete `internal/clichat/legacytui_test_exports.go` and
   `internal/clichat/legacytui_split_helpers_test.go` and any other export
   shim that existed only to serve legacytui's tests. Grep for `legacytui`
   across the tree and clean every remaining reference including comments.

**Definition of done:** `grep -ri legacytui --include=*.go --include=*.json .`
returns nothing. `make verify` green.

**Commit:** `refactor(ui): delete legacytui`

---

# PHASE 10 — Collapse the orphaned cli-family packages

**Goal:** reclaim the structure. With legacytui gone, large parts of
`internal/clichat` (terminal rendering, bubbles, dialogs, rails, markdown)
exist only to serve a front end that no longer ships.

**Task:**

1. Audit `internal/clichat` (105 production files). Classify each file:
   - **Dead** — terminal rendering that only the old TUI or the old REPL used.
   - **Live** — still reachable from `cmd/mivia`'s non-interactive paths
     (if any remain) or from another package.
   Use `go list -deps` and real call-graph checks, not filename guesses.
2. Delete the dead set. Do the same audit for `cliagents`, `cliworkflow`,
   `cliorchestrate`, `cliworktree`, and the alias shims in `internal/cli`
   (`clichat_aliases_*.go`, `cliagents_aliases.go`).
3. Whatever genuinely remains as CLI behaviour, decide deliberately: does it
   move behind `ports` as another adapter, or does it stay as a separate
   non-interactive command surface? State the decision in the commit body.
4. Re-baseline `.mivia/policy/import-layers.json`: regenerate the allow map
   from the final tree and set `edgeCap` to the real measured count plus a
   small documented margin. Rewrite the `description` field — it still
   carries "interim cap" and "Item 4 split" language that will be stale.

**Definition of done:** no package exists solely to serve a deleted front end.
`make verify` green. Import cap reflects the real final tree.

**Commit:** `refactor(cli): collapse packages orphaned by the UI replacement`

---

## Phase dependency order

```
0 ─> 1 ─> 2 ─> 3 ─> 4 ─> 5 ─> 6 ─> 7a ─> 7b ─> 8 ─> 9 ─> 10
```

Phases 4, 5, and 6 are independent of each other once 3 lands — they can be
reordered or parallelised across agents if each gets its own branch. Phases
0–3 are strictly sequential. Phases 8–10 are strictly sequential and must not
start until the earlier ports are all real.

## Where the AI SDK integration fits

This plan covers UI replacement only. Integrating `mivia-ai-sdk` is a separate
thread. The natural join point is `internal/uiadapter`: it is already the
package that owns "how a turn is produced", so an SDK-backed conversation
would be an alternative implementation of `ports.Conversation` beside the
`chat.Session`-backed one from Phase 2. Do not entangle the two threads before
Phase 3 has proven the seam.
