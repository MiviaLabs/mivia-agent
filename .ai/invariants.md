# System Invariants

These are **non-negotiable** properties of the mivia agent that any change must preserve.
Before committing changes that touch the relevant area, run the corresponding invariant
test(s) and confirm they pass.

## Categories

- **Safety** — property that can be verified in a unit test on a finite state set.
  A failing test proves the invariant is violated.
- **Liveness** — property that asserts something *eventually* happens. Unit tests
  cannot fully prove liveness; they can only verify partial behavior. Treat liveness
  invariants as requiring integration/stress tests or accepted residual risk.

## TUI / Rendering

| ID | Category | Invariant | Test(s) | Last Verified |
|----|----------|-----------|---------|---------------|
| INV-TUI-1 | Safety | Bridge drain is the exclusive runtime content source of truth for the TUI | `TestBridgePathAssistantToolsAndFinish` | |
| INV-TUI-2 | Liveness | pollCmd always re-queues itself regardless of data availability (no chain death) | `TestTuiTickMsgAlwaysRequeuesPoll`, `TestPollCmdUsesBridgeNotAdapterOnly` | |
| INV-TUI-3 | Safety | finishStream is idempotent — calling it twice does not produce duplicate blocks | `TestFinishStreamIdempotent`, `TestBridgeDrainNotDoubleProcessed` | |
| INV-TUI-4 | Safety | uiEventMsg always re-queues pollCmd in chat mode | `TestUIEventMsgStepUpdatesDetail`, `TestUIEventMsgErrorSetsStalled` | |
| INV-TUI-5 | Safety | Smoke journey end-to-end completes without panic | `TestTUISmoke_FullJourney` | |
| INV-TUI-6 | Liveness | Tool progress events are visible in TUI during parallel execution (tools don't look hung) | `TestStreamBridgeQueuedRunningDoesNotDoubleCountActiveTools` | |

## Agent Loop

| ID | Category | Invariant | Test(s) | Last Verified |
|----|----------|-----------|---------|---------------|
| INV-AG-1 | Safety | Tool surface Description() and schema JSON validate against OpenAI schema | `TestSearchOpenAISchema` | |
| INV-AG-2 | Safety | run_command presents as last-resort (argv, not shell) | `TestToolSurfacePreferFilesystemOverRunCommand` | |
| INV-AG-3 | Safety | Session.SendUser is not data-racy on Messages field | `TestSessionMessagesConcurrentReadWrite`, `TestSessionMessagesConcurrentLoadMore`, `TestSessionMessagesRaceDetector` | |
| INV-AG-4 | Safety | Multi-step subagent gets tool access; one-shot does not | `TestDelegateToolMultiStepTrue`, `TestDelegateToolMultiStepFalse` | |
| INV-AG-5 | Safety | Tool argument redaction is opt-in, default shows args | `TestRedactToolInputDefaultShowsArgs` | |
| INV-AG-6 | Safety | Multi-step subagent uses tools when handler is multi_step | `TestMultiStepHandlerToolAccess` | |

## Security / Privacy

| ID | Category | Invariant | Test(s) | Last Verified |
|----|----------|-----------|---------|---------------|
| INV-SEC-1 | Safety | Local search skips well-known secret paths (`.env`, `.ssh/`, etc.) | `TestSearchLocalSkipsSecretPaths` | |
| INV-SEC-2 | Safety | Privacy redaction of tool args is off by default | `TestPrivacyRedactToolArgsDefaultOff` | |

## Liveness Gap Notes

| ID | Gap | Mitigation |
|----|-----|------------|
| INV-TUI-2 | Unit tests verify the pollCmd returns a command but cannot prove the chain never starves under real I/O scheduling | Add integration test with simulated slow bridge; monitor for tick starvation |
| INV-TUI-6 | Unit test verifies events are published but cannot prove the TUI paints them within 100ms under parallel dispatch | Add visual/acceptance regression suite (Phase 3 stretch) |

## Maintenance

- **Test renames:** If a test listed above is renamed or moved, update this manifest and ensure the renamed test still verifies the same invariant.
- **Validation:** Run `make validate-invariants` before committing manifest changes to confirm all referenced tests exist in the codebase.
- **Additions:** When adding a new invariant, include the test name(s) and category. Prefer Safety over Liveness where possible.
