# 50 - ACP adapter (`mivia acp`)

**Status**: 📋 Draft - **ADLC Step 0 has NOT run.** No challenge agents dispatched,
no scorecard, nothing locked. This document is the input to Step 0, not its output.
Do not implement from it.

**Goal**: Ship a second, non-interactive front end for the existing agent core that
speaks the Agent Client Protocol, so mivia runs inside Zed, JetBrains, and Neovim
without a GUI project and without touching `internal/cli`.

**Blast radius**: **HIGH.** Structurally it is one new package tree and one new
subcommand, but §4c folds in a first-class interactive authorization capability that
mivia has never had. That crosses `.mivia/rules/10-security-privacy.md` and is the
reason this plan needs a hostile Step 0 and not a fast path.

---

## 0. Ground truth (verified at HEAD, 2026-08-02)

Every claim below was read out of the tree, not assumed.

| Fact | Evidence |
|------|----------|
| The binary has five commands and no machine surface | `internal/cli/root.go:19` - `version｜help｜chat｜config｜doctor｜agents` |
| There is no structured output anywhere | `grep -rn "\-\-json\|\-\-format\|stream-json\|OutputJSON" internal/cli/` (non-test) → **0 hits** |
| Only one event type has a wire codec | `internal/events/serialize.go` marshals `CompactionEvent` and nothing else |
| `events.Event` is not serializable as declared | `internal/events/event.go` - carries `Err error` and `*Identity` |
| The bus is synchronous and in-process | `internal/events/bus.go` - `Publish` calls handlers inline on the publisher's goroutine |
| The only bus consumer is the TUI | `internal/cli/ui_adapter.go` - subscribes, forwards to Bubble Tea |
| The agent loop entry point is already headless | `internal/agent/loop.go:166` - `func (l *Loop) Run(ctx, userText string, opts Options) (string, error)` |
| **Tool authorization is static, not interactive** | `internal/runtime/dispatcher_validate.go:26`, `internal/runtime/dispatcher.go:305` - policy allowlist returns `permission denied`; there is no prompt anywhere in the tree |
| Session load/resume already exists | `internal/chat/session_store.go:123` `Load`, `:130` `LoadWithInfo`, `:173` `List`; plan 15 shipped `/resume` |
| Workspace tool set | `internal/tools/names.go` - `read_file, list_dir, grep, glob, write_file, search_replace, multi_edit, run_command, search, fetch_url, extract, find_references, read_skill_resource` |
| A diff renderer exists | `internal/diff/` (381 LOC) |
| Redaction is opt-in and off by default | plan 10; `redact.SetPolicy` installed in `internal/cli/chat_command.go` |

**The load-bearing consequence**: the headless core the app-server would need does not
exist yet, but *the loop it wraps does*. This plan builds that headless core and gives
it exactly one consumer. The TUI is untouched and remains a peer.

---

## 1. Protocol ground truth

Read from the published spec, 2026-08-02. **Every enum below must be re-derived from
the pinned schema at implementation time** (§3.1) - these lists are for sizing the
work, not for hand-transcription into Go.

- `PROTOCOL_VERSION` = **1** (single integer, MAJOR only).
- Negotiation: client sends its latest; agent echoes it if supported, else replies
  with its own latest and the client decides whether to disconnect.

**Agent handles (client → agent):** `initialize`, `authenticate`, `logout`,
`session/new`, `session/load`, `session/resume`, `session/list`, `session/close`,
`session/delete`, `session/prompt`, `session/set_config_option`, `session/set_mode`.

**Client handles (agent → client):** `fs/read_text_file`, `fs/write_text_file`,
`session/request_permission`, `session/update`, `elicitation/create`,
`elicitation/complete`, `terminal/create｜output｜wait_for_exit｜kill｜release`.

**Notifications:** `session/cancel`, `$/cancel_request`.

Baseline vs. capability-gated: the prose spec names `initialize`, `authenticate`,
`session/new`, `session/prompt` as baseline; the generated Rust trait lists
`session/cancel` and `session/load` as required too. **Treat the discrepancy as
unresolved** and implement all six; it costs little and removes the ambiguity.

**Values needed for mapping** (`ToolKind`: `read, edit, delete, move, search,
execute, think, fetch, other`; `ToolCallStatus`: `pending, in_progress, completed,
cancelled` - the community Go SDK also emits `failed`, another discrepancy to
resolve against the pinned schema; `StopReason`: `end_turn, max_tokens,
max_turn_requests, refusal, cancelled`; `PermissionOptionKind`: `allow_once,
allow_always, reject_once, reject_always`; diff content: `path`, `oldText`
(nullable), `newText`).

`SessionUpdate` discriminators confirmed on the prompt-turn page: `plan`,
`agent_message_chunk`, `tool_call`, `tool_call_update`, `usage_update`. The schema
contains more (thought chunks, mode updates); **enumerate exhaustively from the
pinned schema, do not trust this list.**

---

## 2. Architecture

```
mivia acp  ──►  internal/acp        translation only, no agent logic
                     │
                     ▼
                internal/session    NEW - headless session API (the real deliverable)
                     │
        ┌────────────┼────────────┐
        ▼            ▼            ▼
   agent.Loop   chat sessions   runtime.Dispatcher
                     │
              events.Bus ──► internal/cli (TUI, unchanged)
```

**The rule that makes or breaks this plan: ACP is an adapter over an internal API,
never the internal API.** `internal/session` is designed against mivia's model -
subagents, skills, agent switching, ledger, compaction. `internal/acp` projects the
subset ACP can express and drops the rest.

Invert that and mivia's feature surface is permanently capped at whatever ACP happens
to model this year, and the desktop app in a later plan inherits the ceiling. This is
the single structural claim Step 0 must attack hardest.

### 2.1 New packages

| Path | Purpose | Budget |
|------|---------|--------|
| `internal/session/` | Headless session lifecycle: create, prompt, cancel, load, list, close. Owns nothing the TUI owns. | ~600 LOC over ≥2 files |
| `internal/session/wire.go` | Serializable projection of `events.Event`; redaction applied **here** | ≤300 LOC |
| `internal/session/approval.go` | `Approver` interface + the narrow-only decision rule (§4c) | ≤200 LOC |
| `internal/acp/` | JSON-RPC 2.0 framing, method dispatch, ACP type mapping | ~900 LOC over ≥4 files |
| `internal/cli/acp_command.go` | `mivia acp` subcommand; **thin**, no protocol logic | ≤120 LOC |

`.mivia/policy/go-structure.json` caps files at 500 LOC / functions at 80. The file
splits above are budgeted for it, not to be discovered later.

### 2.2 Event mapping

| mivia (`internal/agent/event.go`) | ACP |
|---|---|
| `EventAssistant` | `session/update` → `agent_message_chunk` |
| `EventThinking` | `agent_thought_chunk` (confirm the discriminator against the schema) |
| `EventToolStart` | `tool_call`, status `in_progress`, `ToolCallID` → `toolCallId` |
| `EventToolEnd` | `tool_call_update` → `completed` / `failed` |
| `EventStep`, `EventPrune`, `EventCompaction`, `EventCacheUsage` | **dropped** - no ACP representation |
| `EventHook` | **dropped**, or folded into a tool call's content. §4c |
| `EventSubagentStart/End/Heartbeat` | **no representation.** §4b |
| `EventToolParallel` | dropped; individual tool calls carry the truth |

`agent.Event.ToolCallID` already exists as "a stable correlation key for tool
lifecycle events" - that is exactly ACP's `toolCallId`, so the correlation this
mapping needs is already load-bearing in the core.

### 2.3 Tool → `ToolKind`

`read_file, list_dir, grep→search, glob→search, write_file→edit, search_replace→edit,
multi_edit→edit, run_command→execute, search, fetch_url→fetch, extract→fetch,
find_references→search, read_skill_resource→read`. Mechanical; a table test pins it
so a new tool cannot land without a kind.

Edit-kind tools must emit `ToolCallContent` of type `diff` (`path` / `oldText` /
`newText`) or the editor renders them as opaque text. `internal/diff` produces
rendered output for a terminal; ACP wants the raw before/after strings. **Do not
reuse the renderer** - take the strings at the tool boundary.

---

## 3. Delivery slices

Each slice is independently shippable and independently revertable.

### 3.1 Slice 0 - schema pin (gate)

Vendor the published ACP JSON Schema at a pinned commit under
`internal/acp/schema/`. Generate or hand-write Go types **from that file**, and add a
contract test that fails when the vendored schema and the Go types disagree.

Do **not** depend on `github.com/joshgarnett/agent-client-protocol-go`: community, MIT,
pinned at `v0.0.0-20250902121345-...`, untagged, last published Sept 2025, terminal
support flagged UNSTABLE. Taking a v0.0.0 dependency on the primary external contract
is the wrong trade for this repo. Read it as a reference implementation.

This slice is a gate: every enum in §1 is a placeholder until it lands.

### 3.2 Slice 1 - `internal/session`

Headless API + serializable event projection + redaction at the codec. **Zero ACP
types.** Proven by `internal/cli` continuing to pass unchanged and by a test that
drives a full turn with no TUI.

### 3.3 Slice 2 - `initialize` / `session/new` / `session/prompt` / `session/cancel`

Minimum viable agent. Advertises `loadSession: false`, no fs capability, no terminal.
Real streaming, real tool calls, real stop reasons. **Ends with a live Zed connection
as the acceptance test** - conformance to a spec you cannot exercise is a guess.

### 3.4 Slice 3 - `session/load` + `session/list`

`FileSessionStore` already implements `Load`/`LoadWithInfo`/`List`, and plan 15
shipped `/resume`. Flip `loadSession: true`. Cheap, and most ACP agents do not have it.

### 3.5 Slice 4 - interactive approval (`session/request_permission`) - see §4c

Two parts, in order, and **the first is not ACP work**:

1. **`internal/session/approval.go`** - an `Approver` seam the dispatcher consults
   *after* static policy has already allowed a call. Default implementation is
   `AutoApprove`, which preserves today's behaviour byte for byte. The TUI keeps
   the default and is unchanged.
2. **`internal/acp/permission.go`** - an `Approver` that calls the client's
   `session/request_permission` and maps `allow_once｜allow_always｜reject_once｜
   reject_always` onto the decision, with `allow_always`/`reject_always` remembered
   for the session's lifetime only.

The invariant this slice exists to pin, stated once and tested (§6):

> **Approval narrows; it never widens.** A call the static policy denies can never be
> allowed by any approver. `Approver` is consulted only on the allow path.

That is what keeps this from becoming the bypass path
`.mivia/rules/10-security-privacy.md:60` forbids, and it is what makes the capability
*additive* to the existing authorization model rather than a second, competing one.

Fail-closed (`10-security-privacy.md:50`): if the client advertises no permission
capability, or the request errors, or the connection drops mid-request, the decision
is **reject**, not allow.

### 3.6 Slice 5 - `fs/read_text_file` / `fs/write_text_file` (see §4a)

### 3.7 Slice 6 - `mivia acp` subcommand + docs

Add an `acp` case to `internal/cli/root.go`. Add a docs topic to `docs/OWNERS.yaml`
(proposed `acp-adapter` → `docs/product/acp.md`) - `scripts/check_docs_ownership.py`
runs in `make verify`, so an unowned doc fails the build.

---

## 4. Open decisions - Step 0 must close these

### 4a. Filesystem inversion **(the correctness trap)**

ACP has the **client** own file I/O so the agent sees unsaved editor buffers.
`read_file`, `write_file`, `search_replace`, and `multi_edit` all go straight to disk.

Ship without routing through `fs/*` and mivia-in-Zed reads stale content whenever the
user has unsaved changes. To a user that reads as "this agent is broken," not "this
agent lacks a capability." It will be the first bug report.

`fs/*` are optional and capability-negotiated, so v1 *may* legitimately use disk.

- **(A)** Direct disk in v1; slice 4 routes through `fs/*` when the client advertises
  the capability. Ships sooner, with a known-wrong window.
- **(B)** Route through `fs/*` before any public release. Correct, delays slice 2.
- **(C)** Direct disk permanently; document the limitation.

Recommendation: **A**, with slice 4 as a **release gate**, not a backlog item. But note
the trap in A: a `read_file` that sometimes goes to the client and sometimes to disk is
two code paths through the most-used tool in the product. Step 0 should decide whether
that seam belongs in `internal/tools` or behind a `session`-level filesystem interface.

### 4b. Subagents have no wire representation **(the product question)**

`internal/coordinator` (5.4k LOC) and `internal/subagents` (3.1k LOC) are a large part
of what mivia *is*. ACP's `SessionUpdate` union has no concept of a nested agent.

- **(A)** Flatten: subagent tool calls appear as the parent's tool calls, prefixed.
- **(B)** Model each subagent run as one long-running `tool_call` whose content
  streams the child's activity.
- **(C)** Emit a `plan` update per subagent wave, approximating fleet view.
- **(D)** Refuse subagent dispatch in ACP sessions.

Recommendation: **B**, with `ToolKind: other`. It is honest about nesting and gives the
editor something coherent to render. **D deserves a real hearing** - a silently
degraded fleet view may be worse for the product than an explicit "not here."

This is the load-bearing product question: if mivia's value is orchestration, ACP
undersells it and the answer is a GUI, not this plan.

### 4c. Interactive authorization **(the doctrine question)**

**mivia has never had a per-call approval prompt.** Authorization is static:
`runtime.Policy` allowlists, `--allow-program`/`--deny-program`/`--disable-tool`, and
deterministic `PreToolUse` hooks. Plan 44 *removed* the last confirmation step on
purpose - "a declared hook runs, with disclosure replacing the prompt."

Editor users arriving from Claude Code and Gemini CLI expect approval prompts.

- **(A)** Never call `session/request_permission`. Static policy governs, exactly as in
  the terminal. Doctrinally consistent; will read as unsafe to Zed users.
- **(B)** Call it for `run_command` and edit-kind tools when the static policy would
  otherwise allow. Introduces a second, session-scoped authorization concept.
- **(C)** Introduce interactive approval as a first-class core capability that both the
  TUI and ACP consume.

**Decided: (C), folded into this plan as slice 4 (§3.5) rather than deferred.**
Owner decision, 2026-08-02. Rationale: the capability is required for the ACP surface
to be credible, and building it as an adapter-local detail (B) is how a product ends
up with two authorization models and no authoritative one. If it has to exist, it
exists once, in the core, on a seam both front ends consume.

What (C) obliges this plan to carry, and what Step 0 must therefore attack:

1. **The narrowing invariant is the whole safety argument** (§3.5). If Step 0 can
   construct any path where an `Approver` widens authority beyond static policy, the
   decision reverts to A and slice 4 is cut. `10-security-privacy.md:60` -
   "do not invent bypass paths."
2. **Fail-closed on every degenerate case** - no capability, RPC error, timeout,
   dropped connection → reject (`10-security-privacy.md:50`).
3. **`10-security-privacy.md` gains an authorization section.** That file is control
   surface; §9 records the verification that follows from editing it.
4. **The TUI must be provably unaffected.** `AutoApprove` is the default and
   `internal/cli` is in the race set (§7) precisely to hold that claim to evidence.
5. **`allow_always` scope is session-lifetime only.** No persistence, no
   `~/.mivia/` store, no cross-session memory. Durable grants are a distinct problem
   with a distinct threat model and are **not** in this plan - a remembered "always
   allow `run_command`" written to disk is exactly the bypass path rule 60 names.
6. **Approval must not deadlock the agent.** `session/request_permission` is a
   round-trip to the editor while a tool call is held open; it interacts directly
   with §4d backpressure and with `session/cancel` arriving mid-request. A cancel
   during a pending approval must resolve to reject-and-unwind, not a hung goroutine.

Step 0 should also weigh the counter-case honestly: plan 44 removed confirmation on
purpose, and this reintroduces a shape of it. The distinction being claimed is that
44 removed *trust confirmation for declared hooks* (a startup-time gate on operator
config) while this adds *per-call approval requested by a client that has a human
attached*. If Step 0 finds that distinction thin, A remains available.

### 4d. Backpressure

`events.Bus.Publish` runs handlers inline. `UIAdapter` copes with a 512-slot buffer,
a 5s `criticalSendTimeout`, and *dropping non-critical events*. **Dropping is
acceptable for a UI and is a protocol violation for ACP** - a dropped `tool_call`
leaves the editor showing a tool that never finishes.

- **(A)** Unbounded queue per ACP session. Correct; unbounded memory.
- **(B)** Bounded queue; on overflow, abort the turn with `StopReason` and a clear error.
- **(C)** Make the wire projection synchronous - stdout write blocks the agent loop.

Recommendation: **B**. Never silently drop. `internal/ledger` already carries
sequence numbers; use them so a gap is *detectable* rather than invisible.

### 4e. Redaction

Redaction is opt-in and **off by default** (plan 10). `AGENTS.md` forbids raw prompts
and secrets leaving the process. The wire projection is a new egress path.

Decide whether ACP output is redacted independently of the workspace policy. Note
the counter-argument: the editor is the same trust domain as the terminal, so
inventing an ACP-only policy may be security theatre that diverges the two surfaces.
Whatever is decided, the redaction call site belongs in `internal/session/wire.go`
so no future transport can bypass it.

### 4f. Protocol version drift

The spec is at version 1 and moving (`session/resume`, `session/list`,
`elicitation/*`, and `logout` all postdate the summaries this plan was drafted from).
Decide the support policy now: which version(s) `initialize` accepts, and what happens
on mismatch beyond the spec's "client decides."

---

## 5. What this plan explicitly does NOT do

- No HTTP server, no WebSocket, no port, no daemon. **stdio only.**
- No refactor of `internal/cli`. The TUI is untouched; it does not become an ACP client.
- No desktop app, no Electron, no Tauri, no web UI.
- No `terminal/*` (UNSTABLE in the spec; `run_command` stays local).
- No `elicitation/*`, no MCP capability advertisement, no `session/set_config_option`.
- No new tools, no changes to tool semantics.
- **No durable approval grants.** Interactive approval ships (§3.5, §4c) but
  `allow_always` lives and dies with the session. No on-disk grant store.
- **No TUI approval UI.** Slice 4 builds the `Approver` seam and one ACP
  implementation. Wiring a prompt into `internal/cli` is a later, separate plan that
  the seam makes cheap.
- **No general-purpose app-server.** If one is wanted later it is a sibling adapter
  over the same `internal/session`. That is the whole point of §2.

---

## 6. Test strategy

| Level | Scenario |
|---|---|
| Contract | Vendored schema ↔ Go types agree (slice 0 gate) |
| Unit | `initialize` version negotiation: match, agent-newer, agent-older |
| Unit | Every `agent.EventKind` maps to an ACP update **or is explicitly listed as dropped** - a new kind must fail this test until dispositioned |
| Unit | Every name in `tools.AllToolNames()` has a `ToolKind` - a new tool fails until mapped |
| Unit | Redaction applied at `wire.go` for each event kind carrying content |
| Integration | Full turn over a piped stdio pair: prompt → chunks → tool call → completion → `end_turn` |
| Integration | `session/cancel` mid-tool → `cancelled` stop reason, no orphaned goroutine |
| Integration | Overflow → bounded-queue behaviour per §4d, never a silent drop |
| **Invariant** | **Approval narrows, never widens**: for every tool, a statically denied call stays denied under every `Approver` verdict including `allow_always`. Table-driven over `tools.AllToolNames()`. |
| Unit | Each `PermissionOptionKind` maps to the right decision; `allow_always`/`reject_always` are remembered for the session and **not** persisted |
| Unit | Fail-closed: no client capability / RPC error / timeout / closed connection → reject |
| Integration | `session/cancel` arriving while an approval is pending → reject, unwind, `cancelled` stop reason, no leaked goroutine (§4c.6) |
| Regression | `AutoApprove` is the default and a full TUI session is byte-identical to HEAD - the claim in §4c.4 held to evidence |
| Race | `go test -race ./internal/acp/... ./internal/session/...` |
| Manual | **Live Zed session.** Non-negotiable acceptance gate for slice 2. |

---

## 7. Verification

```bash
make verify
go test -race ./internal/acp/... ./internal/session/... ./internal/cli/...
go build -o bin/mivia ./cmd/mivia && ./bin/mivia acp < testdata/handshake.jsonl
```

`internal/cli` is in the race set deliberately: this plan's claim is that the TUI is
unaffected, and an untested claim is not a claim.

---

## 8. Rollback

`mivia acp` is additive - a new subcommand and two new packages. Deleting
`internal/acp/` and the `root.go` case restores HEAD behaviour exactly.

Two parts do **not** roll back cleanly:

- `internal/session` - it is the extraction the app-server and any future GUI also
  need. Keep it regardless.
- **The `Approver` seam (§3.5 part 1)** - it changes the dispatcher's allow path for
  every front end, not just ACP. Its revert story is `AutoApprove` remaining the only
  implementation, which is behaviour-identical to HEAD but leaves the seam in the
  tree. Step 0 should accept that seam on its own merits, because deleting it later
  costs more than never adding it.

If §4b resolves to **D** (refuse subagents), reconsider whether the ACP surface is
worth shipping at all - `internal/session` and the `Approver` seam still are.

**Kill criteria**:

- If slice 2's live-Zed test shows the mapping in §2.2 loses so much that the session
  is not usably better than `mivia chat`, stop after slice 1 and keep the headless core.
- If Step 0 breaks the narrowing invariant (§4c.1), cut slice 4, revert §4c to **A**,
  and ship the ACP surface without interactive approval. **Slice 4 is severable from
  the rest of the plan by construction** - that is why it is a late slice with its own
  seam rather than a change threaded through slices 1-3.

---

## 9. Sequencing

Independent of every open plan. Touches no file that `14`, `18`, `30`, `31`, `34`,
`38`, `45`, `48`, or `49` touch.

One coupling: plan **45** (v2 lifecycle events) adds `SessionStart`/`SubagentStart`.
If 45 lands first, §2.2 gains event kinds and the exhaustiveness test in §6 will fail
until they are dispositioned - which is the test working as intended.

**Control-surface obligation from §4c.** Slice 4 edits
`.mivia/rules/10-security-privacy.md` (new authorization section) and so triggers
`.mivia/INDEX.md` → "Verification After Control-Surface Edits": re-read the INDEX and
the touched rule, run `make verify`, and report what was and was not verified. That is
a gate on slice 4, not a follow-up.

The narrowing invariant (§3.5) is the kind of claim `.mivia/invariants.md` exists to
pin, and should be registered when slice 4 lands. No ID is allocated here: per
`.mivia/INDEX.md`, IDs are allocated at landing time, lowest free per prefix, and
`scripts/validate_invariants.py` rejects duplicates.

---

## 10. Next step

Run ADLC Step 0 with a panel of at least four, given the blast radius:

| Lens | Target |
|---|---|
| `reviewer` + `architecture-review` | §2 - is the adapter/API split real or aspirational? Does `internal/session` earn its existence, or is it a wrapper? |
| `auditor` | **§4c - attack the narrowing invariant.** Find any path where an `Approver` widens authority, any non-fail-closed degenerate case, or any way `allow_always` outlives the session. This is the highest-value seat on the panel. |
| `auditor` | §4a two-path filesystem seam, §4d backpressure, and the cancel-during-pending-approval unwind (§4c.6) |
| `reviewer` | §4b - does a degraded mivia in Zed hurt the product more than absence? |

§4c is **decided but not locked**: the owner chose (C) and the panel's job is to
falsify the safety argument, not to relitigate the preference. If the invariant
survives, slice 4 stays. If it does not, §8's kill criterion applies and the plan
ships without it.

Nothing else in this document is locked.
