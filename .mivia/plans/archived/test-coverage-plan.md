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
| `tools` | 85.0% | Listed pure helper coverage complete; skill_resource remains intentionally skipped |
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
- [x] `glob_match.go:matchGlob`
- [x] `scope.go:FilterNames`
- [x] `search_capability.go:Capability`
- [x] `capped_buffer.go:Write` (single)
- [x] `tools.go:CloneForGeneration`
- [x] `open_regular_unix.go` fcntl wrappers
- [x] `search_helpers.go:writeEntity`

#### `internal/agent/`
- [x] `loop_limits.go:truncate`
- [x] `loop_tools.go:ScrubEphemeralToolMessages`
- [x] `context.go:interruptedContext`
- [x] `context.go:promptBudgetError`

#### `internal/agents/`
- [x] `catalogue.go:LoadAndResolve`
- [x] `policy.go:AllowlistSet`
- [x] `inspection.go:replaceInspectionRow`

#### `internal/coordinator/`
- [x] `recovery.go:ListInterruptedRuns`
- [x] `recovery.go:ResultsFromSnapshots`
- [x] `types.go:Done`
- [x] `spawn.go:releaseAndDeleteRun`
- [x] `retry.go:Exhausted` / `Done`
- [x] `dag.go:runDAG`

#### `internal/ledger/`
- [x] `displayname.go:Reset`
- [x] `storage_claims.go:ClearRunClaim`
- [x] `memory_claims.go:ClearRunClaim`
- [x] `storage_owner.go:NewBorrowedStorageLedgerRepository`
- [x] `storage_owner.go:UnderlyingStore`

#### `internal/runtime/`
- [x] `dispatcher.go:Has`
- [x] `dispatcher.go:Allow`
- [x] `dispatcher_validate.go:Validate`
- [x] `context.go:NewSessionID`

#### `internal/skills/`
- [x] `skills.go:ListModelFacing`
- [x] `resources.go:Prompt` / `ToolKey` / `ToolResultBudget`

#### `internal/subagents/`
- [x] `MaxFanout` / `MaxDepth` / `MaxBudget` / `Timeout`
- [x] `ValidateTask`

#### `internal/config/`
- [x] `hooks_scope.go:UserPath`
- [x] `hooks_scope.go:containsPath`
- [x] `types.go:ModelChoicesFor`

#### `internal/workspace/`
- [x] `namespace.go:ContextStorePath`

### Phase 2: Functions requiring simple mocking (interfaces, fakes)

These need test doubles but are not deeply integrated.

#### `internal/storage/` — SQLite-backed operations
- [x] `store.go:DeleteRun` / `Count` / `ListRunIDs` / `Close` / claim ops
- [x] `sqlite.go:DeleteRun` / `ListRunIDs` / claim ops
- [x] `context_store.go` — ensure/commit/advance lifecycle chain
- [x] `queue.go:Submit`

#### `internal/contextmgr/`
- [x] `structural.go` — preparation, discard, and context cancellation
- [x] `source_projector.go`
- [x] `planner.go:invalidPlan` / `validatePlanKey`
- [x] `summary.go:Value`
- [x] `contracts.go:Prepare` / `Commit`

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
