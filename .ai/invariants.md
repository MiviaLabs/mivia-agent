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
| INV-TUI-2 | Liveness | pollCmd always re-queues itself regardless of data availability (no chain death) | `TestTuiTickMsgAlwaysRequeuesPoll`, `TestPollCmdUsesBridgeNotAdapterOnly`, `TestTuiTickMsgStressRequeuesPoll`, `TestTuiTickMsgStressWithBridgeData` | |
| INV-TUI-3 | Safety | finishStream is idempotent — calling it twice does not produce duplicate blocks | `TestFinishStreamIdempotent`, `TestBridgeDrainNotDoubleProcessed` | |
| INV-TUI-4 | Safety | uiEventMsg always re-queues pollCmd in chat mode | `TestUIEventMsgStepUpdatesDetail`, `TestUIEventMsgErrorSetsStalled` | |
| INV-TUI-5 | Safety | Smoke journey end-to-end completes without panic | `TestTUISmoke_FullJourney` | |
| INV-TUI-6 | Liveness | Tool progress events are visible in TUI during parallel execution (tools don't look hung) | `TestStreamBridgeQueuedRunningDoesNotDoubleCountActiveTools`, `TestStreamBridgeConcurrentDispatchCompleteness`, `TestStreamBridgeConcurrentDispatchAndTUIApply`, `TestBridgeConcurrentWriteAndDrainRace`, `TestBridgeConcurrentFinishAndDrainRace`, `TestStreamBridgeConcurrentActiveToolsNoDeadlock` | |
| INV-TUI-7 | Safety | Empty-Content tool turns get honest status (never blank); no fake assistant speech | `TestEmptyContentToolsGetStatusLine`, `TestShortInterimRejectedUsesStatus`, `TestToolStatusLine_RedactsSecrets` | |
| INV-TUI-8 | Safety | Interim quality gate rejects ghost bubbles; real prose still commits | `TestInterimRejectedWhenTooShort`, `TestInterimAcceptedWhenRealProse`, `TestPushInterimGatesGhosts`, `TestInterimAssistantBecomesChatBubble` | |
| INV-TUI-9 | Safety | Cancel preserves partial story (interim/tools) + cancelled footer | `TestCancelKeepsInterimAndToolsInHistory`, `TestCancelBeforeFirstActivity` | |
| INV-TUI-10 | Safety | Scroll follow helper + awaiting planning affordance | `TestShouldFollowOutput`, `TestAwaitingFirstActivityPlanning` | |
| INV-TUI-11 | Safety | Follow mode preserves YOffset on content growth; jump-to-latest restores bottom | `TestFollowPreservesOffsetWhenContentGrows`, `TestNoteUserScrolledUpThenPollDoesNotYank`, `TestJumpToLatestKeyPath` | |
| INV-TUI-12 | Safety | Cancel then bus TurnEnd does not duplicate cancelled footer | `TestCancelThenTurnEndDoesNotDuplicateFooter` | |
| INV-TUI-13 | Safety | View-only hydrate reconstructs empty-speech status; pure hydrate unchanged | `TestReconstructStatus_EmptyContentTools`, `TestReconstructStatus_DoesNotMutateMessages` | |
| INV-TUI-14 | Safety | Classic REPL interim gates + no final double-print | `TestClassicUI_InterimPrintedWhenNoStreamBytes`, `TestClassicUI_InterimSkippedWhenAlreadyStreamed`, `TestClassicUI_FinalEventNotPrinted` | |
| INV-TUI-15 | Safety | History work groups collapse dense tools; final assistant outside | `TestWorkGroupAutoCollapseAt4`, `TestWorkGroupFinalAssistantOutside`, `TestFindWorkGroups_SplitsOnInterim` | |
| INV-TUI-16 | Safety | Model-level scroll acceptance (Update paths) | `TestScrollAccept_MouseWheelUpUnfollowsAndStreamDoesNotYank`, `TestScrollAccept_EndKeyJumpToLatestViaUpdate`, `TestScrollAccept_ConcurrentTicksWhileScrolledUp` | |
| INV-TUI-17 | Liveness | tea.Program event-loop scroll (Send + live pollCmd) preserves follow/YOffset | `TestScrollProg_WheelUpUnfollow_PollDoesNotYank`, `TestScrollProg_EndKeyJumpToLatest`, `TestScrollProg_ConcurrentPollWhileScrolledUp` | |
| INV-TUI-18 | Liveness | Linux PTY CSI keys drive scroll follow (End/PgUp) under tea.Program | `TestScrollPTY_EndKeyViaBytes`, `TestScrollPTY_PgUpViaBytesUnfollows` | |
| INV-TUI-19 | Safety | Mouse auto-enables when available; MIVIA_MOUSE override | `TestMouseAvailable_EnvOverride`, `TestMouseAvailable_DumbTERM`, `TestNewTUIModel_MouseFollowsAvailability` | |
| INV-TUI-20 | Liveness | Linux PTY SGR mouse wheel unfollow/refollow | `TestScrollPTY_CSIMouseWheelUnfollows`, `TestScrollPTY_CSIMouseWheelDownRefollows` | |
| INV-TUI-21 | Safety | Paint frame shows latest when following; glyph chrome bounded | `TestScrollProg_PaintFollowShowsLatestMarker`, `TestScrollIndicator_GlyphWidthBounded` | |
| INV-TUI-22 | Safety | Raster cell-grid paint timing: marker in cols×rows bitmap within budget | `TestPaintRaster_TimingBudgetToMarker`, `TestPaintRaster_UnfollowFrameDoesNotExceedCellBudget`, `TestPaintRaster_UnitRasterize` | |

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

| ID | Gap | Mitigation | Feasibility |
|----|-----|------------|-------------|
| INV-TUI-2 | Unit tests verify the pollCmd returns a command but cannot prove the chain never starves under real I/O scheduling | **Phase 3:** Added `TestTuiTickMsgStressRequeuesPoll` (500 rapid ticks, each verifies non-nil cmd) + `TestTuiTickMsgStressWithBridgeData` (100 ticks with concurrent bridge writes). Full integration with simulated slow bridge + real TTY requires bubbletea test framework (deferred). | **Stress test: feasible.** Integration: deferred. Residual risk: kernel scheduler edge cases require production monitoring. |
| INV-TUI-6 | Unit test verifies events are published but cannot prove the TUI paints them within 100ms under parallel dispatch | **Phase 3:** Added concurrent stress tests (`TestStreamBridgeConcurrentDispatchCompleteness`, `TestStreamBridgeConcurrentDispatchAndTUIApply`, `TestBridgeConcurrentWriteAndDrainRace`, `TestBridgeConcurrentFinishAndDrainRace`, `TestStreamBridgeConcurrentActiveToolsNoDeadlock`). These verify event completeness, TUI apply without deadlock, and concurrent bridge safety. A visual/acceptance regression suite is deferred. | **Stress test: feasible.** Visual timing: deferred (requires acceptance framework). |

## Maintenance

- **Test renames:** If a test listed above is renamed or moved, update this manifest and ensure the renamed test still verifies the same invariant.
- **Validation:** Run `make validate-invariants` before committing manifest changes to confirm all referenced tests exist in the codebase.
- **Additions:** When adding a new invariant, include the test name(s) and category. Prefer Safety over Liveness where possible.
