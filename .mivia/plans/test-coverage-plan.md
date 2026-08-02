# Test Coverage Remediation Plan

**Generated:** Auto-generated from `go test -coverprofile`
**Baseline:** 77.2% overall statement coverage across `internal/...`

## Priority Tiers

### Tier 1 — Packages below 70% coverage (highest impact)

| Package | Coverage | Key Gaps |
|---------|----------|----------|
| `contextstate` | 85.0% | Phase 1 validation and contract coverage completed |
| `contextmgr` | 64.2% | Plan, Prepare, Discard, ProjectSource all 0% |
| `storage` | 62.5% | SQLite store ops, context store advance/commit, queue Submit |

### Tier 2 — Packages 70–80% coverage

| Package | Coverage | Key Gaps |
|---------|----------|----------|
| `chat` | 72.8% | binding helpers, context integration, session agent surface |
| `diff` | 92.7% | ✅ `trimContext` and `minInt` complete |
| `envfile` | 89.5% | ✅ `Lookup` complete |
| `events` | 95.9% | ✅ identity, attribution, and validation complete |

### Tier 3 — Packages 80–90% coverage (incremental wins)

| Package | Coverage | Key Gaps |
|---------|----------|----------|
| `cli` | 76.1% | Hundreds of TUI rendering/dialog functions at 0% (hard to unit-test) |
| `agent` | 83.2% | ScrubEphemeralToolMessages 0%, truncate 0%, interruptedContext 0% |
| `tools` | 83.4% | skill_resource.go (all 0% — integration-only, skip), glob_match edge cases |
| `coordinator` | 81.3% | ListInterruptedRuns 0%, ResultsFromSnapshots 0% |
| `config` | 80.9% | ExpandPath, validateBaseURL |
| `subagents` | 81.2% | MaxFanout/MaxDepth/MaxBudget/Timeout 0% (env-configured, skip) |
| `agents` | 85.8% | LoadAndResolve 0%, AllowlistSet 0% |
| `provider` | 86.0% | openai_compat NewOpenAICompatWithRetry 0% |

### Tier 4 — 90%+ packages (polish only)

| Package | Coverage | Notes |
|---------|----------|-------|
| `contentref` | 100.0% | ✅ Complete |
| `providerregistry` | 100.0% | ✅ Complete |
| `redact` | 100.0% | ✅ Complete |
| `hooks` | 95.4% | ✅ Tier 4 edge cases covered |
| `codeintel` | 92.6% | ✅ `posInfo`, `classifyUseRole` covered |

---

## Phased Implementation Plan

### Phase 1: Pure logic / validation functions (no I/O, no mocking)

These are self-contained functions that can be tested with pure table-driven tests.

#### `internal/contextstate/` — Validation & Contracts
- [x] `contracts.go:NewRevision` / `Validate`
- [x] `contracts.go:NewSourceRange.Validate`
- [x] `contracts.go:NewBindingRevision.Validate`
- [x] `contracts.go:NewCheckpointID.Validate`
- [x] `contracts.go:NewPrincipal.Validate`
- [x] `contracts.go:CapabilityDigest`
- [x] `contracts.go:isLowerHex`
- [x] `contracts.go:validateBoundedText`
- [x] `commit_validation.go` validation functions
- [x] `sanitize.go:Classify`
- [x] `sanitize.go:contextError`
- [x] `json.go:UnmarshalCanonical`
- [x] `source.go:ValidateSourceEvents`
- [x] `store_contracts.go:AdvanceRequest.Validate`

#### `internal/diff/`
- [x] `trimContext`
- [x] `minInt`

#### `internal/envfile/`
- [x] `Lookup`

#### `internal/events/`
- [x] `NewIdentity`
- [x] `WithAgentAttribution`
- [x] `event.go:CompactionEvent.Validate`

#### `internal/tools/`
- [ ] `glob_match.go:matchGlob` — 0%
- [ ] `scope.go:FilterNames` — 0%
- [ ] `search_capability.go:Capability` — 0%
- [ ] `capped_buffer.go:Write` (single) — 64.7%
- [ ] `tools.go:CloneForGeneration` — 0%
- [ ] `open_regular_unix.go` — fnctl wrappers 64–80%
- [ ] `search_helpers.go:writeEntity` — 21.4%

#### `internal/agent/`
- [ ] `loop.go:truncate` — 0%
- [ ] `loop_tools.go:ScrubEphemeralToolMessages` — 0%
- [ ] `context.go:interruptedContext` — 0%
- [ ] `context.go:promptBudgetError` — 0%

#### `internal/agents/`
- [ ] `catalogue.go:LoadAndResolve` — 0%
- [ ] `policy.go:AllowlistSet` — 0%
- [ ] `inspection.go:replaceInspectionRow` — 0%

#### `internal/coordinator/`
- [ ] `recovery.go:ListInterruptedRuns` — 0%
- [ ] `recovery.go:ResultsFromSnapshots` — 0%
- [ ] `types.go:Done` — 0%
- [ ] `spawn.go:releaseAndDeleteRun` — 0%
- [ ] `retry.go:Exhausted` / `Done` — 0%
- [ ] `dag.go:runDAG` — 0%

#### `internal/ledger/`
- [ ] `displayname.go:Reset` — 0%
- [ ] `storage_claims.go:ClearRunClaim` — 0%
- [ ] `memory_claims.go:ClearRunClaim` — 0%
- [ ] `storage_owner.go:NewBorrowedStorageLedgerRepository` — 0%
- [ ] `storage_owner.go:UnderlyingStore` — 0%

#### `internal/runtime/`
- [ ] `dispatcher.go:Has` — 0%
- [ ] `dispatcher.go:Allow` — 0%
- [ ] `dispatcher_validate.go:Validate` — 0%
- [ ] `context.go:NewSessionID` — 0%

#### `internal/skills/`
- [ ] `skills.go:ListModelFacing` — 0%
- [ ] `resources.go:Prompt` / `ToolKey` / `ToolResultBudget` — 0%

#### `internal/subagents/`
- [ ] `MaxFanout` / `MaxDepth` / `MaxBudget` / `Timeout` — 0% (env-configured, test with env vars)
- [ ] `ValidateTask` — 0%

#### `internal/config/`
- [ ] `hooks_scope.go:UserPath` — 0%
- [ ] `hooks_scope.go:containsPath` — 0%
- [ ] `types.go:ModelChoicesFor` — 0%

#### `internal/workspace/`
- [ ] `namespace.go:ContextStorePath` — 0%

### Phase 2: Functions requiring simple mocking (interfaces, fakes)

These need test doubles but are not deeply integrated.

#### `internal/storage/` — SQLite-backed operations
- [ ] `store.go:DeleteRun` / `Count` / `ListRunIDs` / `Close` / claim ops — all 0%
- [ ] `sqlite.go:DeleteRun` / `ListRunIDs` / claim ops — all 0%
- [ ] `context_store.go` — advance/commit/ensure chain (11–87%)
- [ ] `queue.go:Submit` — 58.3%

#### `internal/contextmgr/`
- [ ] `structural.go` — all 0% (Prepare, Discard, contextDone)
- [ ] `source_projector.go` — all 0%
- [ ] `planner.go:invalidPlan` — 0%, `validatePlanKey` — 0%
- [ ] `summary.go:Value` — 0%
- [ ] `contracts.go:Prepare` / `Commit` — 0%

### Phase 3: Hard-to-test / UI-only (deferred or accepted gaps)

These are TUI rendering functions, terminal I/O, and deeply integrated CLI flows.
**Decision: Accept these gaps.** They require bubbletea model setup or OS terminal mocking.

- `internal/cli/` — Hundreds of 0% TUI/dialog/rendering functions
- `internal/cli/terminal.go` — All terminal I/O methods at 0%
- `internal/cli/chat_repl_loop.go` — REPL event handlers at 0%
- `internal/cli/dialog.go` — TUI dialog rendering at 0%
- `internal/cli/chat_command.go` — `runChat` / `runConfiguredChat` at 0%
- `internal/cli/doctor.go` — `runDoctor` at 0%

#### `internal/tools/skill_resource.go` — ALL at 0%
**Decision: Skip.** This is integration-only code that requires runtime skill resolution.
All functions (NewSkillResourceTool, Name, Description, Parameters, Execute, etc.) are 0% by design.

---

## Scope Exclusions (Intentionally Skipped)

| File/Function | Reason |
|--------------|--------|
| `tools/skill_resource.go` (all functions) | Integration-only, requires runtime skill resolution |
| `cli/` TUI rendering (~100+ functions at 0%) | Requires bubbletea model mocking; cost/benefit unfavorable |
| `cli/terminal.go` (all methods) | Direct OS terminal I/O, not unit-testable |
| `cli/chat_repl_loop.go` | REPL loop with terminal reads |
| `subagents/` MaxFanout/MaxDepth/MaxBudget/Timeout | Env-var configured constants, tested indirectly |
| `provider/openai_compat.go:NewOpenAICompatWithRetry` | Deprecated constructor wrapper |

---

## Success Criteria

- [ ] All Phase 1 items implemented and passing
- [ ] Phase 2 items implemented where practical
- [ ] `go test ./internal/...` passes with no regressions
- [ ] `make diff-coverage` passes (only changed lines checked)
- [ ] Overall coverage improves measurably from 77.2% baseline
