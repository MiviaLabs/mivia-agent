# P2.3 — Collapse the `NewSessionDispatcher*` constructor explosion

**Status:** DESIGN-READY — implementation must pass ADLC Step 0 (plan challenge +
scorecard) before any code is written.
**Date:** 2026-07-31
**Review finding:** `.mivia/reports/cli-internal-refactoring-review.md` §P2.3
(orchestration slice).
**Depends on:** `p1-5-delete-dead-code.md` — **HARD BLOCKER.** The two dead
unexported constructors `newSessionDispatcher` and `newSessionDispatcherWithContext`
(`dispatcher.go`) must be deleted first; this plan removes the layer they sit in and
their presence would make the diff ambiguous. Do not start Wave 1 until P1.5 lands.
**Soft dependency:** `p1-4-open-durable-ledger-repo.md` (Wave 3 in the review's
suggested order). If P1.4 has shipped, the collapsed constructor consumes
`openDurableLedgerRepo`; if not, the SQLite-open block is inlined once (de-duplicated
by the collapse itself). Either way P2.3 is implementable — see §3.C.
**Blocks:** nothing.
**Blast radius:** LOW–MEDIUM — pure mechanical signature change inside one package;
no behavior change, no new types reaching production callers beyond a struct literal.
All call sites are in `internal/cli` (verified by grep, see §2).

---

## 1. Problem

`internal/cli/dispatcher.go` exposes **five** `NewSessionDispatcher*` constructors
(four public + one unexported workhorse) that thread combinations of the same
`(reg, comp, model, cfg, repo, toolResultCapBytes, maxContextTokens, maxTokens,
budget, skillReg)` parameters. Two of them (`NewSessionDispatcherWithContext` and
`NewSessionDispatcherWithBudgetProvider`) each **re-open the SQLite store and
re-run `Recover`/`reportInterruptedRuns` inline** — the exact triplication P1.4
targets. The constructors exist to paper over three orthogonal optional inputs
(`repo` vs. open-it-yourself, `budget` present vs. absent, `maxTokens` present vs.
absent), which is precisely the "over-abstraction" the review flags.

After P1.5 deletes the two dead unexported wrappers, the live set is:

| Constructor | Repo source | `budget` | `maxTokens` | Used by |
|---|---|---|---|---|
| `NewSessionDispatcher` | opens SQLite | nil | nil | 8 test sites |
| `NewSessionDispatcherWithContext` | opens SQLite | nil | `*int` | **0 callers** (dead-ish; only forwards) |
| `NewSessionDispatcherWithBudgetProvider` | opens SQLite | `func()int` | `*int` | **2 prod** + 1 test |
| `NewSessionDispatcherWithLedger` | caller-supplied | nil | nil | 2 test sites |
| `newSessionDispatcherWithContextAndBudget` (unexported) | caller-supplied | `func()int` | `*int` | the real workhorse |

`NewSessionDispatcherWithContext` has **zero callers** post-P1.5 — it is kept alive
only because `NewSessionDispatcher` forwards to it. That forwarding chain is the
clearest sign of the over-abstraction.

## 2. Goals and non-goals

### Goals

- Replace the five constructors with **one** `NewSessionDispatcher(opts
  SessionDispatcherOpts) (*runtime.Dispatcher, error)` plus a **single thin
  convenience wrapper** for the dominant call shape (see §3.B).
- Collapse the duplicated SQLite-open/`Recover`/`reportInterruptedRuns` block to
  one site (consuming P1.4's helper when available).
- Update **every** call site. Verified complete call-site set (grep
  `NewSessionDispatcher` across the repo, excluding `.mivia/` docs and session
  logs):

  **Production (2):**
  - `internal/cli/chat_repl.go:90` — `NewSessionDispatcherWithBudgetProvider(sess.Tools, binding.Completer, model, cfg, sess.MaxToolResultChars, sess.PromptBudget(), sess.MaxTokens, sess.PromptBudget, skillReg)`
  - `internal/cli/model_binding.go:52` — `NewSessionDispatcherWithBudgetProvider(toolGeneration, comp, model, res.Subagents, sess.MaxToolResultChars, binding.PromptBudgetTokens, res.MaxTokens, sess.PromptBudget, skillReg)`

  **Tests (≈11 sites across 7 files):**
  - `budget_integration_test.go:67` — `WithBudgetProvider` (budget path)
  - `delegation_test.go:484,531,613` — `NewSessionDispatcher` (minimal)
  - `delegation_test.go:646,684` — `NewSessionDispatcher` + skillReg
  - `dispatcher_output_ceiling_test.go:28` — `NewSessionDispatcher` (minimal)
  - `ledger_tools_paging_test.go:212,239` — `WithLedger` (caller repo)
  - `session_tool_budget_test.go:52` — `NewSessionDispatcher` (minimal)
  - `session_tool_surface_test.go:79,228` — `NewSessionDispatcher` (+ skillReg at :79)
  - `tool_result_cap_test.go:86` — `NewSessionDispatcher` + capBytes + skillReg

- Preserve every existing behavior exactly: SQLite fallback wording, `OnClose`
  store ownership, `initCoordinator` registration, tool/skill/delegation/orchestration/ledger
  handler wiring order.

### Non-goals

- Do **not** change what the dispatcher registers or the order in which handlers
  are wired (INV-AG-7 nested-agent scoping depends on the registration sequence).
- Do **not** touch the `MaxTokens: 4096` default or any other magic number — that
  is P3.
- Do **not** redesign the `budget func() int` indirection itself; only how it is
  passed in.
- Do **not** rename the package or the `runtime.NewToolDispatcher` internals.
- Do **not** add a functional-options (`With…`) variant on top of the struct — the
  review explicitly wants the struct, not another option-pattern layer.

## 3. Decisions required before implementation

### A. The options struct — recommended

```go
// SessionDispatcherOpts carries every input the session dispatcher needs.
// Repo and Budget are optional; their absence selects the legacy defaults
// (open a SQLite store from cfg, no live budget provider).
type SessionDispatcherOpts struct {
    Registry           *tools.Registry
    Completer          provider.Completer
    Model              string
    Config             config.SubagentConfig
    ToolResultCapBytes int

    // Repo, if set, is used as-is and its lifetime is caller-owned.
    // If nil, the constructor opens a SQLite store from Config (with the
    // memory-backend fallback) and owns its Close via dispatcher.OnClose.
    Repo ledger.LedgerRepository

    // MaxContextTokens / MaxTokens configure the nested subagent handlers.
    // Zero values mean "unset" (handler defaults apply).
    MaxContextTokens int
    MaxTokens        *int

    // Budget, if non-nil, is the live session budget provider read by nested
    // handlers when invoked (so /budget applies without rebuilding).
    Budget func() int

    // SkillReg, if non-nil, registers each skill as a Subagent handler.
    SkillReg *skills.Registry
}

// NewSessionDispatcher builds a runtime.Dispatcher for agent sessions from a
// single options struct. This is the only public constructor.
func NewSessionDispatcher(opts SessionDispatcherOpts) (*runtime.Dispatcher, error)
```

Note `SkillReg` becomes a single `*skills.Registry` rather than a variadic
`...*skills.Registry`. Every live caller passes at most one; the variadic was
unused flexibility (the workhorse already collapses it via `skillReg[0]`).

### B. The convenience wrapper — recommended

Keep **one** thin wrapper for the dominant minimal test shape, to avoid rewriting
~8 test call sites into struct literals and to keep test diffs readable:

```go
// newSessionDispatcherMinimal is a test-only convenience for the common
// no-budget, no-repo, no-maxTokens case. Production must use NewSessionDispatcher
// with an explicit SessionDispatcherOpts.
func newSessionDispatcherMinimal(reg *tools.Registry, comp provider.Completer, model string, cfg config.SubagentConfig, toolResultCapBytes int, skillReg ...*skills.Registry) (*runtime.Dispatcher, error) {
    return NewSessionDispatcher(SessionDispatcherOpts{
        Registry: reg, Completer: comp, Model: model, Config: cfg,
        ToolResultCapBytes: toolResultCapBytes, skillRegFromVariadic(skillReg),
    })
}
```

(Exact name + whether it stays exported is a Step-0 disposition point — see §3.D.
The recommendation is **unexported**, because every current caller of the minimal
shape is a `_test.go` file in the same package, and `ledger_tools_paging_test.go`'s
`WithLedger` shape is different enough to justify using the struct directly.)

**Alternative rejected:** rewrite all test sites to use the struct verbatim and
ship *zero* wrappers. Cleaner endpoint, but it churns ~8 test files for no
behavioral gain and makes the diff harder to review. Revisit if a Step-0 challenger
argues the wrapper is itself speculative generality.

### C. SQLite-open de-duplication — depends on P1.4 timing

- **If P1.4 has shipped:** `NewSessionDispatcher` calls `repo, ownedStore :=
  openDurableLedgerRepo(opts.Config)` when `opts.Repo == nil`, and wires
  `ownedStore.Close()` into `d.OnClose` exactly as today.
- **If P1.4 has not shipped:** inline the existing open/fallback/`Recover`/
  `reportInterruptedRuns` block once in `NewSessionDispatcher` (it is currently
  duplicated across `WithContext` and `WithBudgetProvider`). The collapse itself
  removes the duplication; P1.4 can later extract it without conflict.

Either branch produces identical observable behavior. The chosen branch is
determined at implementation time by reading `dispatcher.go` for an
`openDurableLedgerRepo` symbol — no plan change needed.

### D. Step-0 disposition list

The challenge panel (ADLC Step 0) must explicitly disposition:
1. Convenience wrapper: keep (unexported) vs. drop (struct-only). §3.B recommends keep.
2. `SkillReg` singular vs. variadic. §3.A recommends singular.
3. Whether `NewSessionDispatcherWithContext` (zero live callers post-P1.5) is
   deleted outright rather than folded — **yes, delete; it has no callers.**

## 4. Architecture

The collapsed constructor preserves the existing layering exactly; only the entry
points change:

```text
NewSessionDispatcher(opts)                     ← the ONE public constructor
  ├── resolve repo: opts.Repo OR openDurableLedgerRepo(cfg)   [P1.4 helper if present]
  ├── newSessionDispatcherCore(opts, repo)      ← renamed workhorse (was …WithContextAndBudget)
  │     ├── runtime.NewToolDispatcher(reg, Policy{MaxDepth, MaxBudget})
  │     ├── registerOneShotHandlers(...)
  │     ├── registerMultiStepHandler(...)
  │     ├── registerSkillHandlers(...)
  │     ├── registerDelegationTools(...)
  │     ├── registerOrchestrationTools(...)
  │     └── registerLedgerTools(...)
  ├── wire ownedStore.Close() into d.OnClose   (only when constructor opened the store)
  └── initCoordinator(d, cfg, repo)

newSessionDispatcherMinimal(...)               ← unexported test convenience (§3.B)
  └── NewSessionDispatcher(SessionDispatcherOpts{...})
```

No interface changes. No new files strictly required — the struct + constructor
live in `dispatcher.go`. The workhorse `newSessionDispatcherWithContextAndBudget`
is renamed `newSessionDispatcherCore` (or kept unexported under its old name;
Step-0 call) and drops the now-redundant positional parameters in favor of
`opts`.

**Invariant touch:** `internal/cli/` is on the invariant list (INV-AG-7 nested-agent
scoping, INV-AG-25/27 tool-result budgets). None of these depend on constructor
*arity* — they depend on *what gets registered*, which is unchanged. The
`MaxOutputBytes` derivation exercised by `dispatcher_output_ceiling_test.go` and
`session_tool_budget_test.go` flows through `runtime.NewToolDispatcher` + the
registry, both untouched.

## 5. Implementation waves

REFACTOR, TDD-preserving. Every production task is preceded by a compiling RED
test that fails an assertion on the target API. Because this is a pure
signature change with pinned existing tests, the "RED" tests are largely the
existing call sites retargeted onto the new struct — they fail to compile
(→ counted as RED under the Fast-Path-adjacent refactor rule only if an assertion
also fires; prefer to add at least one new assertion-based test, §7).

| Wave | Scope (1 file per task) | Required proof |
|---|---|---|
| 0 | Challenge §3 design; read `dispatcher.go`, all §2 call sites, `.mivia/invariants.md` | Architecture + correctness reviews dispositioned; scorecard all PASS |
| 1 | **dispatcher.go** — add `SessionDispatcherOpts` + `NewSessionDispatcher(opts)`; route through renamed core; add `newSessionDispatcherMinimal`. Old public ctors temporarily delegate to new one (compile-only bridge) | `go build ./internal/cli`; new `TestNewSessionDispatcherOptsBuildsDispatcher` RED→GREEN |
| 2 | **chat_repl.go** + **model_binding.go** (the 2 prod callers, one task each) migrate to `NewSessionDispatcher(SessionDispatcherOpts{…})` | `go build ./...`; both packages' existing tests still pass |
| 3 | Test-site migration, **one file per task**: `budget_integration_test.go`, `delegation_test.go`, `dispatcher_output_ceiling_test.go`, `ledger_tools_paging_test.go`, `session_tool_budget_test.go`, `session_tool_surface_test.go`, `tool_result_cap_test.go` | Each file's tests pass; `WithLedger` callers use the struct with `Repo:`; minimal callers use `newSessionDispatcherMinimal` (or struct, per §3.D) |
| 4 | **dispatcher.go** — delete the now-unused public ctors (`NewSessionDispatcherWithContext`, `NewSessionDispatcherWithBudgetProvider`, `NewSessionDispatcherWithLedger`) and the bridge; keep only `NewSessionDispatcher` + `newSessionDispatcherMinimal` | `go vet ./...`; grep confirms zero remaining references to deleted names outside their definitions |
| 5 | Review: read final `dispatcher.go` + a sample of migrated call sites; confirm no behavior change | Reviewer PASS |
| 6 | Hostile audit + race | `go test -race ./internal/cli/...`; zero confirmed bugs |

Wave 2's two prod callers may run in parallel (independent files). Wave 3's test
files are independent and run in parallel. Wave 4 is sequential after 2+3 (it
deletes names the prior waves stopped using).

## 6. Migration table (old call → new call)

| Old | New |
|---|---|
| `NewSessionDispatcher(reg, comp, model, cfg, cap)` | `newSessionDispatcherMinimal(reg, comp, model, cfg, cap)` *(test-only)* |
| `NewSessionDispatcher(reg, comp, model, cfg, cap, skillReg)` | `newSessionDispatcherMinimal(reg, comp, model, cfg, cap, skillReg)` |
| `NewSessionDispatcherWithBudgetProvider(reg, comp, model, cfg, cap, maxCtx, maxTok, budget, skillReg)` *(prod)* | `NewSessionDispatcher(SessionDispatcherOpts{Registry:reg, Completer:comp, Model:model, Config:cfg, ToolResultCapBytes:cap, MaxContextTokens:maxCtx, MaxTokens:maxTok, Budget:budget, SkillReg:skillReg})` |
| `NewSessionDispatcherWithLedger(reg, comp, model, cfg, repo, cap)` | `NewSessionDispatcher(SessionDispatcherOpts{Registry:reg, Completer:comp, Model:model, Config:cfg, Repo:repo, ToolResultCapBytes:cap})` |
| `NewSessionDispatcherWithContext(...)` | **deleted** (no callers post-P1.5) |

## 7. Verification

**New assertion-based test (RED first, to satisfy TDD-for-refactor):**

- `TestNewSessionDispatcherOptsBuildsDispatcher` — constructs via the struct with
  a budget provider and asserts (a) no error, (b) the `multi_step` and `delegate`
  subagents are registered (`d.Has(...)`), (c) the budget function is wired (invoke
  a `oneshot` after mutating the budget source and confirm the handler observes the
  new value — mirrors `budget_integration_test.go`'s intent).

**Existing tests that must still pass unchanged in behavior** (they migrate
syntactically in Wave 3):
- `TestNewSessionDispatcherRegistersDelegationTools`,
  `TestNewSessionDispatcherRegistersMultiStepHandler` (`delegation_test.go`)
- `TestIntegrationBudgetChangeAffectsNestedSubagentInvocation`
  (`budget_integration_test.go`) — the load-bearing budget-wiring proof
- `TestLedgerReadInvalidArgumentsDoNotEchoUntrustedFieldNames`,
  `TestLedgerReadPageIsNotTailCutByTheAgentLoop` (`ledger_tools_paging_test.go`) —
  the `Repo:` path
- The ceiling tests (`dispatcher_output_ceiling_test.go`,
  `session_tool_budget_test.go`) — INV-AG-25/27 adjacent

**Mutation table** (each mutation must turn a GREEN test RED):

| # | Mutation | Test that MUST fail |
|---|---|---|
| M1 | `NewSessionDispatcher` ignores `opts.Budget` (passes nil to core) | `TestIntegrationBudgetChangeAffectsNestedSubagentInvocation` |
| M2 | `NewSessionDispatcher` ignores `opts.Repo` (always re-opens SQLite) | `TestLedgerReadInvalidArgumentsDoNotEchoUntrustedFieldNames` (would hit the `nil repo` / fallback path) |
| M3 | `ownedStore.Close()` not wired into `OnClose` when the constructor opened the store | a new `TestNewSessionDispatcherClosesOwnedStore` asserting the store is closed after `d.Close()` |
| M4 | `SkillReg` dropped (not forwarded to `registerSkillHandlers`) | `TestNewSessionDispatcherRegistersMultiStepHandler` (skill variant) |

**Minimum command gates:**

```text
go build ./...
go vet ./...
go test  ./internal/cli/... -count=1
go test -race ./internal/cli/... -count=1
make invariants
make verify
```

`scripts/check_go_structure.py --strict --all internal/cli` must still pass (it
passes today per the review).

## 8. Rollback

This is a pure mechanical refactor with no behavior change. Rollback is simply
`git revert`. There is no state migration, no storage format change, and no config
key. If a Wave-6 audit reveals a behavioral regression (e.g. budget not applied,
owned store leaked), the criterion is: **fix forward in the collapsed
constructor**, do not restore the five-ctor fan-out — the fan-out is the bug the
review identified.

## 9. Out of scope, explicitly

- P1.4 (`openDurableLedgerRepo`) — consumed if present, not depended on.
- P1.5 (delete dead unexported ctors) — hard prerequisite, not duplicated here.
- P3 magic-number sweep (`MaxTokens: 4096` etc.) — separate plan.
- Any change to `registerSessionTool`, `OnEventForMultiStep`, or the handler
  registration order.
