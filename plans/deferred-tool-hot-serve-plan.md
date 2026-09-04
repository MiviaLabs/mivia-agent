# Deferred-tool hot-serve — implementation plan (v1, draft for review)

Status: draft. Not yet review-converged. Prepared after a live repro: a
`load_tools` stage whose step-boundary publication deferred caused a call to
the staged tool to be denied with "retry next step", although the synchronous
deferred-serve path (`serveUnadmittedTool` → `admitForExecution`) could have
executed it in the same step.

## Goal

A model that calls a tool whose admission is already staged (via `load_tools`
or a prior deferred call) gets that call **executed synchronously in the same
step** whenever the surface is stable — instead of a staged-but-not-published
denial. Publication at the step boundary remains unchanged: it is still what
makes the tool natively admitted for later steps and turns.

## Drivers

1. The hot path already exists. `UnadmittedToolHandler` →
   `serveUnadmittedTool` (`internal/chat/session_turn_surface.go:80`) resolves,
   approves, and returns `UnadmittedToolResult.Execute` for a **fresh**
   deferred call. The gap is purely an ordering artifact: a *pending stage*
   shadows the handler (`internal/agent/agentloop_tool_error.go:120-128`),
   so explicitly calling `load_tools` first yields a strictly worse experience
   than calling the locked tool directly.
2. The divergence is already declared, not designed:
   `fresh-read-executes-twice/deferred` in
   `.mivia/policy/tool-execution-conformance.json` documents that admission
   interception pre-empts execution. The exemption's own rationale ("the tool
   is about to become natively callable") does not cover the first call after
   an explicit `load_tools`, which can be many steps away from publication
   when a sibling turn or background run is active.
3. Deferral windows are real, not pathological: R2-1/R2-2 fencing defers
   publication whenever a sibling turn or background orchestration holds the
   session (`internal/chat/admission_status.go`). Under load, "finish this
   turn first" can mean "many steps from now".

## Design

### 1. Hot-serve eligibility (`internal/chat/admission_status.go`)

New predicate, single source of truth for both call sites:

```go
// hotServeEligible reports whether a call to a pending-staged name may be
// served synchronously on the CURRENT surface: the name is staged, the
// surface is not mid-replacement, and no background orchestration holds the
// dispatcher. It never widens the surface, so the R2-1 sole-active-turn rule
// does not apply; the two checks it does keep are the ones under which the
// current dispatcher itself is about to be replaced or closed.
func (s *Session) hotServeEligible(name string) bool // takes s.mu.RLock internally
```

Eligible = name ∈ `pendingAdmission.Names` && `!s.switching` &&
`CheckSwitchAllowed() == nil`. Deferral reasons that block publication but not
hot-serve (`activeTurn`, `stagingTurn`, `superseded`) deliberately do NOT
block hot-serve: hot-serve executes on the existing dispatcher and closes
nothing.

### 2. The staged notice becomes a fallback, not a gate

`internal/chat/session_turn_surface.go` — the `StagedToolMessage` closure
(installed by `wireStepBoundaryAdmission`):

- Root turns (`turn == nil`): when `hotServeEligible(name)`, return
  `(("", false))` so `UnadmittedToolHandler` runs and serves the call.
  Otherwise keep today's notice verbatim.
- Scoped turns (`turn != nil`): unchanged — the closure must keep answering
  with the staged notice, because `serveUnadmittedTool` would otherwise
  replace a true "callable next step" with the wrong "ask the root agent".

The loop's check order (`StagedToolMessage` → `UnadmittedToolHandler` →
generic, `internal/agent/agentloop_tool_error.go:120-129`) is **unchanged**;
the host simply declines to answer for names it can hot-serve. No `internal/
agent` behavioral change at all.

### 3. No double charge on the hot path

`serveUnadmittedTool` gains an early branch: when `hotServeEligible(name)`,
skip `spendAdmissionFor` (the staging already spent the attempt —
`ChargeAdmissionAttempt` in `loadToolsTool.Execute` or in the fresh deferred
call that staged it) and skip the redundant `StageToolAdmission`. Everything
else is the existing sequence: `isAdvertisedToolName` guard → resolve →
approval → `admitForExecution`. Per-call approval is NOT skipped: hot-serve
must not become a way to run a write tool without its prompt.

### 4. Conformance policy and table

- Remove `fresh-read-executes-twice/deferred` from
  `.mivia/policy/tool-execution-conformance.json`. The gate fails while a
  removed divergence is still listed, so this is enforced cleanup, not
  bookkeeping.
- `internal/clichat/tool_execution_conformance_test.go`: add the row that
  replaces the exemption — a second identical call to a deferred tool in one
  turn is **dedup-served by the shim** (`runDeferredToolNow`'s dedup record),
  i.e. the dedup declaration is honored by execution, not by admission
  interception. Plus the new contract itself: a call to a `load_tools`-staged
  tool in the same turn reaches the shim.

### 5. Model-facing copy

The "NEXT turn / retry next step" wording must stop overpromising the wait:

- `internal/cliagents/load_tools_tool.go`: `Description()` and
  `render()` — staged tools become "callable now by calling them directly;
  natively admitted at the next boundary". Grep for the shared phrases
  ("available from your next turn", "queued to load automatically",
  "retry the call on your next step") — the same vocabulary appears in the
  session-side notices (`session_turn_surface.go`) and possibly prompt
  templates; update every site that states or implies the wait applies to a
  direct call. Notices that remain true (non-eligible deferrals) keep their
  text.

## Explicitly out of scope

- Any change to publication fencing (R2-1/R2-2), the wire `tools[]` snapshot,
  or `load_tools` staging semantics.
- Reordering `StagedToolMessage`/`UnadmittedToolHandler` in the loop.
- The `unset-approval-policy-agrees/all` divergence (each side is a
  deliberate security decision; belongs in its own change).
- MCP server transport failures (separate issue; see Findings).

## Centralization assessment (requested)

Current spread of the mechanism:

| Concern | Home |
|---|---|
| Loop-side contract + ordering + execution | `internal/agent` (`options.go`, `agentloop_tool_error.go`, `sdk_dispatcher_shim.go` `RunUnadmittedTool`) |
| Admission state machine (stage/defer/publish/drop) | `internal/chat` (`admission*.go`) |
| Host serve-decision | `internal/chat` (`session_turn_surface.go`) |
| Widener implementations | `internal/cliagents` (`tool_admission.go`, `agent_surface.go`, `agent_switch.go`) |
| `load_tools` tool + copy | `internal/cliagents` (`load_tools_tool.go`) |
| Conformance table + declared divergences | `internal/clichat` + `.mivia/policy/` |

Assessment: **do not extract a new package now.** The layering is already the
DC-35 doctrine done right — one executor (the shim), host decides, loop
executes. A new `internal/admission` package would have to re-export the
session lock, turn fencing, generation and activeTurns context as
interfaces (`check_import_layers.py` would also need a new edge review), and
the repo's own history shows extraction-as-rewrite is how sibling
implementations drift. There is no second consumer today: workflows and
subagents prebuild their registries at construction and never stage.

Real (smaller) debt worth taking in this change or a tight follow-up:

1. **Message vocabulary is duplicated across ~4 sites** (session notices,
   `load_tools` render/Description, `deferAdmissionLocked` notes, generic
   loop denial). Centralize the staged/deferral phrases next to
   `admission_status.go`'s deferral-reason constants so the copy cannot
   drift apart again.
2. **Serve-decision is embedded in surface wiring.** If change §2/§3 grow,
   move `hotServeEligible` + the staged-notice policy into
   `internal/chat/admission_serve.go` so `session_turn_surface.go` returns to
   pure wiring.

## Test plan

Unit / integration (new):

- `hotServeEligible` truth table: staged / not staged / switching /
  orchestration-active (admission_status_test.go).
- Root turn: `load_tools` then call in the same turn → executes (role tool
  body, no "error:" prefix), attempt counter unchanged (no double charge).
- Fresh deferred call still charges exactly one attempt (regression guard on
  the §3 branch).
- Scoped skill turn: staged name still gets today's staged notice (§2
  turn gate).
- Approval: a write-class staged tool hot-served still raises the approval
  prompt; a denial renders as a failed call.
- Dedup: second identical deferred call in one turn is dedup-served, not
  re-executed (the row replacing the removed exemption).
- Fencing: hot-serve leaves `pendingAdmission` intact; a deferred publication
  still publishes at the next qualifying boundary
  (`turn_fencing_stress_test.go` unchanged and green).

Existing gates that must stay green:

- `internal/agent/agentloop_tool_error_test.go` (ordering contract — loop
  untouched, tests pass unchanged),
- `internal/agent/sdk_load_tools_invocation_test.go`,
- `internal/clichat/tool_execution_conformance_test.go` (new rows),
- `.mivia/policy/tool-execution-conformance.json` gate (exemption removal
  enforced),
- `make invariants`, `scripts/check_mutation.py` targets touching
  `admission_status.go` / `session_turn_surface.go`,
- `go test ./internal/chat ./internal/agent ./internal/clichat
  ./internal/cliagents`.

## Risks

1. **Write-tool double execution** (the reason the old exemption existed):
   re-homed onto the shim's dedup + per-call approval, and pinned by a new
   conformance row. The interception-as-dedup accident must not be silently
   lost — hence the row, not just the JSON removal.
2. **Dispatcher replacement mid-call**: eligibility excludes exactly the two
   states in which the current dispatcher is being replaced or closed
   (`switching`, orchestration holding it). Publication itself cannot run
   concurrently with a tool call — it happens at boundary hooks between
   steps.
3. **Copy drift**: prompt-snapshot / provider-docs checks may pin the
   `load_tools` description text; run `scripts/check_provider_docs.py` and
   the docs gates in CI before merging.

## Findings (separate from this plan)

- The `codegraph` MCP server currently fails at transport level
  (`internal/mcp/tool.go:66` returns the opaque "MCP tool call failed";
  timeout/cancel would be stamped distinctly). The failure is server-side and
  hidden by redaction by design — diagnose from the server's own logs, not
  the agent's tool output.
