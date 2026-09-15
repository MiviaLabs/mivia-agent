# SDK Integration and Host-Adapter Cleanup Plan

## Status

Complete except release mechanics. Every SDK claim below was read from the
checkout at
`/home/mac/projects/mivialabs/mivia-ai-sdk` (module
`github.com/MiviaLabs/mivia-ai-sdk`, `go 1.25.0`, branch `main`, latest tag
`v0.6.0` == HEAD `5d73261`; adopted work sits on branch `release/v0.7.0`).

The host builds and tests green against that checkout through a local `replace`
directive in `go.mod`. Nothing is pushed or tagged by this plan.

## The Correction That Drives This Plan

An earlier revision of this document proposed *adding* generic extension points
to the SDK: a tool-execution host port, a tool-admission hook, a result
reference/spill port, and a telemetry sink.

**All four already exist in v0.6.0.** The real work is the opposite of what was
proposed: the host reimplements capabilities the SDK already ships, and the
cleanup is to adopt them and delete the duplicates.

| Previously proposed as "new SDK API" | Already shipping in v0.6.0 |
|---|---|
| Tool execution host port | `tools.Tool` / `tools.Registry.RunScoped` |
| Tool admission hook | `tools.ScopeOptions.Approve` + `ApprovalThreshold` |
| Result-reference / spill port | `memory.Spool`, `memory.ContentStore`, `memory.SpoolTool`, `memory.ReadOutputTool` |
| Telemetry sink | `events.Bus`, `events.Registry`, `trace.Tracer`, `provider.Accumulator` |
| Generic retry / steering | `agentloop.Steer`, `Extensions.ContinueOnStop`, `Compaction` + `EnableCompaction` |
| Budget extension points | `Extensions.WorkBudget`, `Extensions.ToolBudget` |

Do not add SDK API for any of these. Verify the existing capability first, and
only propose SDK change where a measured behavioural gap blocks adoption.

## Measured Starting Point

`internal/agent`'s SDK-bridge layer is 18 files, **4,082 LOC**, plus
`internal/sdkadapter` (1,584 non-test LOC) and `internal/remainder/spool.go`
(259 LOC).

Classification of the bridge layer:

- **Generic mechanism** (adoption candidates): `agentloop_convert.go` (315),
  `agentloop_completer.go` (276, minus effort mapping), `agentloop_steer.go`
  (171, minus cooldown numbers), `sdkTurnState`'s `pass1Map` and cancel
  registry.
- **Mivia policy** (must stay host-side): result shaping tiers
  (`sdk_shaping.go`, 420), ref-only floor and naming (`refonly_shim.go`, 251),
  summarizer redaction and evidence binding (`sdk_summarizer_adapter.go`, 354),
  denial precedence and wire vocabulary (`agentloop_tool_error.go` 234,
  `sdk_tool_events.go` 218), budget numbers (`agentloop_budget.go` 125,
  `agentloop_toolbudget.go` 38), recovery thresholds
  (`agentloop_recovery.go`, 178).

The layer is welded to host-private `Loop` state: `workLimits`, `Messages`,
`LastPreparation`/`HasPreparation`/`recordPreparation`, `sdkPendingCompaction`,
`lastEmittedCompactionKey`/`turnCompactionEmitted`, `contextAccounting()`,
`initialToolSpecs()`, `emitTurnUsage`, `Calibration`, `TurnState`. `sdkTurnState`
is a second run-scoped private surface constructed directly by 23 test files.

That coupling is why the previously attempted `internal/agent/sdk` package
extraction was abandoned: it would have created an `agent -> agent/sdk -> agent`
import cycle. Reduce the coupling first; revisit layout last.

## Verified SDK Capability Reference

Cited so each slice below can be planned without re-reading the SDK.

### agentloop

- `Options` (`agentloop/options.go:160-218`): Completer, Tools, Scope, Model,
  Bounds, OnToolError, Hooks, Tracer, Usage, SessionID, Bus, Budget, Trim,
  Audit, Compaction, ObserveRequest, HeartbeatInterval, Extensions.
- `Validate` (`options.go:287-360`): `Usage` requires non-blank `SessionID`
  (`:313`); `Compaction.Window` requires Summarizer + Calibrated and **excludes
  `Trim`** (`:336,:339,:341`); `HeartbeatInterval > 0` requires `Bus` (`:350`).
- `Extensions` (`extensions.go:13-43`): OnToolCallError, Surface,
  StreamingWriter, Conclude, StartTime, DedupWithinTurn, WorkBudget, ToolBudget,
  ContinueOnStop.
- `Surface` (`surface.go:20-30`, `:63-70`): `Advertised` replaces wholesale from
  iteration 2; nil Registry/Scope keep the previous value; hook panic becomes an
  error (`:78`).
- `WorkBudget` (`budget.go:33-43`, `:105-112`): Reserve before Chat, hard-fail;
  Refund with zero Usage on failure and real Usage on success; **a zero-Usage
  success receives no Refund**.
- `ToolBudget` (`budget.go:139-142`): Reserve once per turn with the
  pre-filter call count, no Refund.
- `ContinueOnStop` (`stop.go:65-83`, `:87`): a non-empty return continues the
  turn; a panic is a hard failure.
- `Steer` (`steer.go`): Trigger / SetInjector / HasActiveCall; a generation
  counter gates the ack (`:160`); the injector survives `reset` (`:172`). On
  `release/v0.7.0` (361af35) a steered stop is decided solely by
  `ContinueOnStop` — the injector auto-continue is removed.
- `Compaction` (`compaction.go:288-301`) and `EnableCompaction` (`:319-335`):
  explicit `MaxTokens > 0`, otherwise 80/50 derived from the ContextAccountant.
  Recovery window triggers at 1% with target
  `max(1, min(16384, Budget/4))` (`:129-137`).
- `Bounds` defaults 24 / 8 / 200_000 / 4 / 3 (`bounds.go:57-63`).
- StopReason set (`stop.go:13-45`): no_tool_calls, empty_response,
  max_iterations, hook_veto, concluded, steered, repeated_tool_failures.
  **There is no tool-error stop reason** (`:9`).
- 13 event constants and 15 error vars: see `api/agentloop.txt`.

### tools

- `ScopeOptions{Allowlist, ExtraDenylist, Approve, ApprovalThreshold}`
  (`scope.go:11-16`). Denylist always wins; privileged tools require explicit
  allowlisting (`Allowed:83-93`).
- Approval fires when `rank(profile.Class) >= rank(approvalThreshold)`
  (`registry.go:153`); a false return yields `ErrToolDeclined` (`:159`).
  **An unknown class ranks as External** (`execution_profile.go:104`), and
  `Scope.Allowed` never reads Class.
- `NewScope` tolerates an unknown threshold; `NewScopeChecked` rejects it
  (`scope.go:73-79`).
- Timeouts: `DefaultRunTimeout` 10m (`registry_timeout.go:12`), `TimeoutNone =
  -1` (`:16`); a declared zero falls through to the default. A panic and a
  context cancellation are both distinct from `ErrRunTimeout`.
- Optional interfaces `ProfiledTool`, `ResultBudgetTool`, `PrivilegedTool` with
  `ExecutionProfileOf` / `ResultBudgetOf` / `IsPrivileged`
  (`execution_profile.go:44-88`).

### memory

- `Spool` (Spool / SpoolExpiring / Load / Expire / GrantExpiry), `Store` /
  `ContentStore`, `SpoolTool`, `ReadOutputTool`,
  `MoreMarker = "[more: offset=%d]"`.
- Principal travels in context (`context.go:12,18`). `ErrNoPrincipal`
  (`tool.go:40`, `readtool.go:94`), `ErrWrongPrincipal` (`spool.go:257`),
  `ErrPrincipalConflict` (`spool.go:201`, state unchanged). Wrong-principal is
  checked **before** expiry (`memory_test/expiry_test.go:84`).
- The tool types are unexported; constructors return `tools.Tool`.

### events, trace

- `events.Bus` (Name to Handler, Emit / Subscribe) and `events.Registry` (hook
  points `PointPreTool` / `PointPostTool` / `PointStop`, veto via `ErrVetoed`)
  are two distinct mechanisms. agentloop accepts both.
- `trace.Tracer.Start` returns ctx plus Span; `SpanFrom`, `Span.SetAttribute`,
  `WriteJSONLines`.

### SDK repository conventions

- Makefile tiers: `verify-fast` (gofmt, vet, runnable doc examples, `go test`,
  ~16 Python gates, semgrep); `verify` (adds sqlite ledger, `-race`, per-package
  85% coverage floor); `verify-maintainer` (plan, prose, label gates).
- `make api-update` regenerates the `api/` locks via
  `go run scripts/api_surface.go`. `scripts/check_api.py` fails on a missing
  lock, drift, **or an orphan lock** — any exported-surface change must
  regenerate these.
- `policy/layers.json` constrains imports: `agentloop` may import only
  `context/budget`, `context/plan`, `events`, `provider`, `schema`, `tools`,
  `trace`.
- Releases are plain annotated git tags on `main`; `v0.6.0` dereferences to HEAD
  `5d732612f788f2d177e41657e0a61f56472009cb` (verified with `git rev-parse
  v0.6.0^{commit} HEAD`) with no separate release commit. That the *procedure*
  is tag-only is inferred from tag placement and the absence of release tooling,
  not from documented process.

## Local Integration Setup

`go.mod` carries:

```
replace github.com/MiviaLabs/mivia-ai-sdk => /home/mac/projects/mivialabs/mivia-ai-sdk
```

Absolute, not relative: a relative `../mivia-ai-sdk` resolves against whatever
tree the build runs in. Verified with:

```
go list -m github.com/MiviaLabs/mivia-ai-sdk
# github.com/MiviaLabs/mivia-ai-sdk v0.6.0 => /home/mac/projects/mivialabs/mivia-ai-sdk
go build ./...
go test ./internal/agent/... ./internal/sdkadapter/...
```

All green at the time of writing.

**Removal condition:** drop the `replace` and bump the `require` once the SDK
release carrying the adopted capabilities is tagged. The `replace` must never
reach a release build of the host.

## SDK Work: branch `release/v0.7.0`

Any SDK-side change lands on a `release/v0.7.0` branch in the SDK repository,
cut from `main` at `v0.6.0`. Nothing is pushed or tagged without explicit
instruction.

A change is admissible in the SDK only if all hold:

1. Its signature uses only SDK-owned and standard-library types.
2. It is implementable without importing host packages, and it respects
   `policy/layers.json`.
3. It names a capability, not a Mivia setting.
4. It is justified by a measured host gap recorded in Stage 0, or by a second
   consumer.
5. It carries SDK tests for failure, cancellation, and zero-value semantics.
6. `make api-update` is run and `scripts/check_api.py` passes.

Reject: CLI option names, Mivia budget constants, approval UX, spool formats,
audit paths, one-off configuration fields.

## Ownership Boundary

**SDK owns:** loop lifecycle and iteration invariants; steerable run mechanics;
tool registry execution, scoping, and timeout enforcement; the generic admission
decision point; opaque result references; event, usage, and trace emission;
compaction mechanics.

**Host owns:** `workLimitMeter` numbers, reservations, refunds; result shaping
tiers, notices, and degrade floors; Mivia approval semantics, staged-tool
policy, denial rendering; `remainder.Spool` durability, retention, and the
INV-AG-10/CE-07 visibility invariants; summarizer redaction, evidence capture,
and calibration; CLI/TUI event translation, audit JSONL, ledger persistence,
session identity; provider reasoning-dialect choices.

## Migration Stages

One slice per stage. Each stage is independently revertible. Do not begin a
stage before its predecessor's exit criteria are met.

### Stage 0: Gap ledger

Host-side, no SDK change.

1. For each bridge file in the inventory above, compare the host behaviour
   against the cited SDK capability and record one of: `adopt` (SDK covers it),
   `keep` (Mivia policy), or `gap` (adoption blocked, with the exact divergence).
2. Reconcile `docs/development/sdk-backend-field-mapping.md` against the v0.6.0
   surface, including the accepted-gap list.
3. Resolve the three skipped SDK-path defect tests (`summary_inject_test.go`,
   `loop_retry_steer_test.go`, `loop_steer_worklimit_test.go`) — fix, or record
   as a known divergence with an owner. They cannot serve as adoption evidence
   while skipped.
4. Record the measured duplication inventory for tool-call keying/normalization
   before proposing any shared helper.

Exit: every bridge file carries a verdict; each `gap` names the SDK behaviour
that blocks it; no stage 1-4 work begins on an unclassified file.

### Stage 1: Adopt `tools.Scope` admission

Blocked on: Stage 0 verdicts for `sdk_dispatcher_shim.go`,
`agentloop_tool_error.go`, `internal/sdkadapter/tool_registry.go`,
`approver.go`.

1. Map Mivia's approval policy onto `ScopeOptions.Approve` plus
   `ApprovalThreshold`, keeping the policy decision host-side.
2. Confirm the two documented divergences before relying on them: an unknown
   execution class ranks as External (`execution_profile.go:104`), and
   `Scope.Allowed` never consults Class.
3. Preserve Mivia denial rendering and precedence; the SDK decides admission,
   the host decides wording.
4. Use `NewScopeChecked`, not `NewScope`, so an unknown threshold fails closed.

Exit: admission branching is SDK-driven; host retains only policy and
rendering; `agentloop_tool_error.go` shrinks; all pinning tests pass.

### Stage 2: Adopt `tools.ExecutionProfile` for timeouts

Blocked on: Stage 1.

1. Replace host timeout arming in `sdk_dispatcher_shim.go` with declared
   `ProfiledTool` profiles where the tool's timeout is static.
2. Preserve the `TimeoutNone` mapping and the documented rule that a declared
   zero falls through to `DefaultRunTimeout`.
3. Keep dispatcher-routed execution: Mivia's dispatcher provides approval,
   hooks, and cancellation that the SDK registry does not model.

Exit: no host-side duplicate of static per-tool timeout policy;
`sdk_registry_run_timeout_test.go` and `sdk_tool_cancel_test.go` still pass.

### Stage 3: Evaluate `memory.Spool` against `remainder.Spool`

Blocked on: Stage 0 verdict for `refonly_shim.go` and
`internal/remainder/spool.go`.

This is an evaluation, not a presumed migration. `remainder.Spool` carries
Mivia visibility invariants (INV-AG-10/CE-07) that the SDK does not model.

1. Compare principal handling, expiry ordering (wrong-principal before expiry),
   grant semantics, and error vocabulary against the host's invariants.
2. If the SDK covers the mechanism, keep `remainder.Spool` as the host policy
   layer over `memory.ContentStore` and delete only the duplicated storage
   mechanics.
3. If it does not, record the gap and stop. Do not weaken a visibility
   invariant to enable adoption.

Exit: a written verdict with evidence; either a reduced `remainder.Spool` over
the SDK store, or a recorded gap with rationale.

### Stage 4: Consolidate event and telemetry translation

Blocked on: Stage 0 verdict for `agentloop_events.go`, `sdk_tool_events.go`.

1. Keep the host translation layer — the legacy wire vocabulary is Mivia's
   contract with its own UI.
2. Delete only host logic that re-derives what the SDK bus already emits.
3. Preserve the `AllEventKinds` registry test and the tool-start/tool-end
   vocabulary.

Exit: event bridging is translation only, with no duplicated loop decisions.

### Stage 5: Reassess package layout

Blocked on: Stages 1-4.

Only once host-private coupling is measurably reduced:

1. Re-measure the bridge layer's dependencies on `Loop` private state and on
   `sdkTurnState`.
2. Extract a subpackage only if it can stand on a small host-owned interface
   without a parent import cycle.
3. If the remaining code still shares the work-limit meter and turn policy, keep
   it in `internal/agent` and stop.

Exit: an extraction that removes real coupling, or a recorded decision not to
split. Import-path churn alone is not a goal.

**Outcome (recorded):** the coupling measurement counts 52 references to
`Loop`-private state across the 19 bridge files, with 12 of 19 files at
zero; the coupling concentrates in 7 files (`sdk_prepare.go` 15,
`sdk_summarizer_adapter.go` 11, `agentloop_adoption.go` 10,
`agentloop_budget.go` 6, `agentloop_adapter.go` 5,
`agentloop_recovery.go` 4, `agentloop_toolbudget.go` 1), binding to the
shared `workLimitMeter` (`l.workLimits`), `contextAccounting`, and the
compaction/summary cluster (`recordPreparation`/`LastPreparation`/
`sdkPendingCompaction`/`lastEmittedCompactionKey`/`turnCompactionEmitted`)
whose reset runs once per turn. An interface extraction is mechanically
feasible for the 12 zero-ref files, but the shared meter and the turn
compaction/summary policy keep the bridge layer in `internal/agent`.
Decision: no split, per this stage's own exit rule ("keep it in
`internal/agent` and stop").

## Verification

Host, per slice:

- `go build ./...`
- `go test ./internal/agent/... ./internal/sdkadapter/...`
- Projection pins: `agentloop_adapter_test.go`, `agentloop_options_test.go`
- Coverage gates: `shim_coverage_gate_test.go`, `sdk_adapter_coverage_test.go`,
  `agentloop_adoption_coverage_test.go`
- `python3 scripts/check_import_layers.py` and
  `python3 scripts/test_import_layers.py`
- Structure, diff-coverage, and mutation gates per repository policy
- Update `docs/development/sdk-backend-field-mapping.md` in the same change

SDK, per change:

- `make verify-fast` during development, `make verify` before any tag
- `make api-update` plus `scripts/check_api.py`
- `policy/layers.json` compliance
- Tests for failure, cancellation, and zero-value semantics

Cross-repository: the host suite must pass against the local `replace` before
any SDK tag is cut.

## Decision Log

Record before any SDK change: the generic problem; the second consumer or
generalization argument; the host code it deletes; SDK-owned invariants versus
host-owned policy; cancellation, retry, error, and zero-value semantics; the SDK
tests and host parity tests; the SDK version introducing it and the host version
adopting it.

If these cannot be stated concisely, keep the logic in `mivia-agent`.

### Candidate: ToolCallKey exported helper (ADOPTED)

Status: ADOPTED — SDK `release/v0.7.0` b00f76e exported the helper; host 1f3e4cf8 retired the four host copies.

- **Generic problem**: 4 host copies of ID-else-name keying (`agent` and `sdkadapter`).
- **Second consumer argument**: any SDK consumer recording per-call outcomes needs the same rule to associate outcomes with calls under blank-ID streams.
- **Host code it deletes**: the 4 copies across `agent` and `sdkadapter`.
- **Semantics**: pure function of `provider.ToolCall`, no cancellation or concurrency surface.
- **Tests**: SDK vectors + host parity tests.

### Candidate: WorkBudget refund-on-failed-zero-Usage or explicit outcome reporting (ADOPTED)

Status: ADOPTED — SDK `release/v0.7.0` 9f9a6cc changed `WorkBudget.Refund` to carry the failure cause (breaking, api locks regenerated); host 20a4ac5c adopted it.

- **Generic problem**: callers cannot distinguish cancelled from failed calls; ordinary provider errors trigger over-refunds or reservations leak.
- **Second consumer argument**: any SDK consumer sharing a token ceiling across concurrent loops needs the failure-vs-consumed distinction to avoid over-refund on ordinary errors; without it every host must fork refund policy host-side.
- **Host code it fixes**: `agentloop_budget.go` full-refund branch.
- **Semantics**: refund only on failed calls with zero `Usage` — matches `refundWork`, requires `settleWork` change or a richer `Refund` signature (SDK designer to choose).
- **Tests**: host parity pins `TestProviderErrorKeepsWorkLimitReservation`.

*Note*: superseded — the Steer soft-continue divergence is resolved; see the ACCEPTED entry above.

### Candidate: Steered stops consult ContinueOnStop (ACCEPTED)

Status: ACCEPTED — owner decision that no behavioral divergence is accepted post-switch; SDK `release/v0.7.0` behavior change (no signature change, no api-lock drift expected); host `v0.2.3` adopts.

- **Generic problem**: `ContinueOnStop` documents "consulted on every graceful stop" but a steered stop bypasses it; an installed injector force-continues every steered stop internally. Continuation policy is hard-wired to injector presence, so a caller cannot take control at a steered stop.
- **Second consumer argument**: any consumer combining queued caller messages with a hard stop gesture — or any caller-gated continuation (budget guards, approval pauses, UI interrupts) — needs a vetoable steered stop. The only lever today is removing the injector mid-run, which `SetInjector`'s own doc declares racy.
- **Host code it fixes**: `runSDKPromptTooLongRecoverable`'s retry gate (`agentloop_recovery.go:52`); makes the dispatcher's `StopSteered` -> `errSteerInterrupt` mapping reachable on injector runs (`loop_dispatch.go:90`); unskips `TestLoopSteerDuringPromptTooLongRetryInterruptsTheRetry` (`loop_retry_steer_test.go`).
- **SDK-owned invariants vs host policy**: SDK owns steered-stop detection, ack-before-arm, once-delivery of injector frames, `StopDecision` shape, panic fail-closed. Host owns continuation policy: continue (return messages) or stop (return nil).
- **Semantics**: no injector, no hook: unchanged (`StopSteered`, nil error, partial `Final`). Injector + gate non-empty: loop continues; trigger acked before the next arm; gate messages append to history; injector still drains exactly once at the next iteration top. Injector + gate nil: run stops with `StopSteered` exactly like the no-injector path; that boundary's pending drain is dropped (documented). Cancellation unchanged (`ctx` cancel is a hard fail, not a steer stop). Gate panic fails closed via `safeContinue`.
- **Tests**: SDK rewrites the injector soft-continue pins and adds gate-continue/gate-stop/gate-panic/cancel-race/nil-hook-parity pins; `make api-update` must show no drift. Host parity: the unskipped retry-steer test plus adapter pins for `StopSteered` + gate-nil -> `errSteerInterrupt`.
- **Versions**: SDK v0.7.0, host v0.2.3. Breaking tolerance precedent: 9f9a6cc.

## Progress And Remaining Work

Recorded at close of the adoption effort, after host commit 05c013c3.

### Done

- **Stage 0 (gap ledger)**: complete. Every bridge file carries a verdict in
  `docs/development/sdk-gap-ledger.md`; the field-mapping doc is reconciled.
- **Tool-call keying**: adopted. SDK b00f76e exported `ToolCallKey`; host
  1f3e4cf8 retired the four host copies. Parity pinned by
  `TestToolCallKeyParityAgainstSDKVectors`.
- **WorkBudget refunds**: adopted. SDK 9f9a6cc made `Refund` carry the failure
  cause; host 20a4ac5c settles refunds by cause. Pin test unskipped and green.
- **Steered stops**: adopted. SDK 361af35 gates steered stops on
  `ContinueOnStop`; host eff64b6b regained stop authority. Both steer skip
  tests unskipped and green.
- **Retry-time summary re-derivation**: fixed host-side in 57729e17. The retry
  re-derives omitted evidence and invalidates the memo. Skip removed.
- **Stage 1 admission**: verdict `keep` — `DecideApproval` and
  `OnToolCallError` already ride the SDK seams; `ScopeOptions.Approve` was
  rejected because it cannot represent standing decisions, resource keys, or
  the deferred path.
- **Stage 2 timeouts**: verdict `keep` — host per-call deadlines exceed a
  static `ExecutionProfile`.
- **Stage 3 spool**: verdict `keep` — correct mechanism/policy split; a later
  swap is possible only as a wrapper preserving INV-AG-10/CE-07.
- **Stage 4 events**: verdict `keep` — wire vocabulary is host contract.
- **Stage 5 layout**: verdict recorded — no split; the bridge stays in
  `internal/agent` (52 private-state references across 19 files).

### Remaining

1. **Tag SDK `v0.7.0`** on the `release/v0.7.0` branch (explicit instruction
   required; run `make verify`, `make api-update`, `check_api.py` first).
2. **Drop the `go.mod` `replace`** and bump `require` to the tagged release.
   The `replace` must never reach a host release build.
3. **Retire the local-checkout plumbing** added to `scripts/release.sh` and
   `scripts/test_release.py` in host de834456.
4. **Cut host `v0.2.3`** carrying the adoption commits.
5. **Deferred, not blocking**: fold the pinned request-0 tool union in
   `sdk_advertised.go` onto `Extensions.Surface` (low urgency); delete
   `agentloop_convert.go` only if the SDK ships a converter; `clampedMaxTokens`
   stays once-per-turn as an accepted gap.

Nothing else is open. Per the ledger, no SDK-repository candidate remains for
the `release/v0.7.0` branch.

## Immediate Next Action

Stage 0 is complete: the gap ledger is produced, all three stage-0 skips
are resolved, and both real gaps are closed (SDK 361af35, 9f9a6cc; host
57729e17, eff64b6b, 20a4ac5c). The remaining work is release mechanics:
tag the SDK `v0.7.0`, drop the host `replace` directive, and bump the
`require`. Do not move `Loop`, work-limit policy, result shaping,
`remainder.Spool`, or Mivia audit and approval behaviour into the SDK.
