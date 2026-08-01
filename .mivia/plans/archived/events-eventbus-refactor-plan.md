# mivia Event Bus & UI Refresh Architecture Plan

**Status:** ✅ **IMPLEMENTED (Phases 1–3) - superseded by the shipped tree. Phase 4 (OTEL) was
always optional and is NOT built.** Verified at HEAD on 2026-07-30:
- **Phase 1 (poll chain)** - all three root causes in §1 are fixed: `Init()` returns
  `tea.Batch(..., m.pollCmd())` (`internal/cli/tui.go:205`), `tuiTickMsg` has a handler, and
  `renderToolPanelWindow` is called from the live render path (`internal/cli/tui_layout.go:252`),
  which §1's "bonus bug" said it never was. Pinned by INV-TUI-1 and INV-TUI-2.
- **Phase 2 (bus infrastructure)** - `internal/events/bus.go` (`type Bus`), plus `adapter.go`,
  `handler.go`, `metrics.go`.
- **Phase 3 (agent loop → bus)** - `agent.Options.EventBus` (`internal/agent/loop.go:68`) and
  `internal/agent/emit.go:15` publishing; `chat.Session.EventBus`; `UIAdapter`
  (`internal/cli/ui_adapter.go:31`); `SetGlobalBus` (`internal/cli/subagent_progress.go:79`).
- **Phase 4 (OTEL)** - not built. `grep -rni 'otel|opentelemetry'` over `internal/`, `cmd/` and
  `go.mod` returns nothing.

**Do not implement from this document.** It is 1713 lines describing a tree that has moved on, and
its line anchors and diff sketches are stale. If OTEL export is wanted, write a short new plan for
that one adapter against the `events.Bus` that now exists; do not resurrect this RFC.

**Original status:** RFC
**Author:** mivia self-work session
**Date:** 2025-07

---

## Table of Contents

1. [Executive Summary](#1-executive-summary)
2. [Current Architecture & Root Cause Analysis](#2-current-architecture--root-cause-analysis)
3. [Direct Answer: Messages, Tools & Thinking in Chat History](#3-direct-answer-will-this-fix-messages-tools--thinking-in-chat-history)
4. [Design Goals & Non-Goals](#4-design-goals--non-goals)
5. [Proposed Architecture](#5-proposed-architecture)
6. [Component Details](#6-component-details)
7. [Migration Phases](#7-migration-phases)
8. [Phase 1: Fix the Poll Chain (Immediate)](#8-phase-1-fix-the-poll-chain-immediate)
9. [Phase 2: EventBus Infrastructure](#9-phase-2-eventbus-infrastructure)
10. [Phase 3: Wire Agent Loop → EventBus](#10-phase-3-wire-agent-loop--eventbus)
11. [Phase 4: OTEL Adapter (Not Implemented)](#11-phase-4-otel-adapter-optional--not-implemented-yet)
12. [Validation & Questions](#12-validation--questions)
13. [Orchestrator Prompt](#13-orchestrator-prompt-implement-this-plan-end-to-end)
14. [Appendix: Code Diff Sketches](#14-appendix-code-diff-sketches)

---

## 1. Executive Summary

**The bug:** The TUI does not refresh its stats, status bar, tool list, or thinking indicator unless the user types a key or clicks a mouse. During "LLM thinking" periods between tool calls, the UI freezes.

**Three things broken by this bug:**

| What | Why it's invisible |
|---|---|
| **Streaming tokens** (assistant messages appearing live) | `bridge.Write()` fills `pending` buffer, signals `notify` channel. With dead poll chain, no one reads it. Buffer accumulates until user interacts. |
| **Tool calls** (names, statuses, results appearing live and in history) | `bridge.PushToolWithID()` stores events in `tools` slice. They're drained into `m.toolRows` only when poll chain fires. And `finishStream()` - which converts tool rows into permanent `ChatBlockTool` blocks - only fires when `done=true` is drained from the bridge. Dead poll = no finishStream = no tool blocks in history. |
| **Thinking** (model reasoning blocks) | Same as tools: `bridge.PushThinking()` buffers content. Only drained and converted to `ChatBlockThinking` blocks inside `finishStream()`. Dead poll = no thinking blocks. |

**Three root causes:**

1. **Missing `pollCmd()` from `Init()`** - no polling ever starts without user interaction.
2. **`pollCmd()` breaks its chain** - it's a one-shot `tea.Cmd` that only re-schedules itself when drain returns data. During quiet periods (model computing, no tokens yet), the chain dies.
3. **`tuiTickMsg` has no explicit `case` handler** - even if fired, it falls through the switch with no dedicated re-render/re-poll logic.

**Bonus bug - tool panel never renders during streaming:**
Even when `renderStreamVP()` IS called, it only renders blocks + stream buffer + thinking buffer. Individual tool rows (`m.toolRows`) are **never** injected into the viewport content during live streaming. They are only converted to `ChatBlockTool` blocks by `finishStream()` after the turn ends. This means tool names/statuses are invisible during the entire agent run - you only see a count like "3/5 tools" in the status bar. The `renderToolPanelWindow()` function exists but is never called from the main rendering path.

**The deeper problem:** All events flow through a single callback chain: `AgentLoop → OnEvent → agentEventBridgeCallback → streamBridge → TUI`. There is no event bus, no pub/sub, no way to add OTEL or external API adapters without rewriting the entire pipe.

**This plan fixes both:** the immediate refresh bug (Phase 1) and the architectural debt (Phase 2-4), with backward compatibility at every step.

---

## 2. Current Architecture & Root Cause Analysis

### 2.1 Current Event Flow

```
agent.Loop.Run()
  ↓ opts.OnEvent(agent.Event{...})
agentEventBridgeCallback(bridge)     [tui_events.go]
  ↓ switch Kind → PushTool / PushThinking / PushStep
streamBridge                         [tui_stream.go]
  ↓ mu + notify chan (non-blocking signal)
pollCmd()                            [tui.go:154]
  ↓ select { case <-notify; case <-80ms }
tuiTickMsg{bridge}
  ↓
updateMessageImpl()                  [tui_message.go]
  └ switch msg.(type):
      case tea.WindowSizeMsg:  ✓
      case logoTickMsg:        ✓  (early return)
      case tea.KeyMsg:         ✓  (may early return)
      case tea.MouseMsg:       ✓  (no early return)
      case tuiTickMsg:         ✗  MISSING - falls through!
  └ textarea.Update(msg)  - no-op for unknown msg
  └ viewport.Update(msg)  - no-op for unknown msg
  └ if modeChat → bridge.Drain()
      ├ tools>0 → applyToolEvents + layout + renderStreamVP + pollCmd()
      ├ stream/done → renderStreamVP + pollCmd()
      └ nothing → ⚠ NO RE-POLL → chain dies
```

### 2.2 Root Cause #1: Poll Chain Starts Dead

`Init()` returns only `spinner.Tick`, `EnterAltScreen`, `logoTickCmd()`. No `pollCmd()`.

- The `spinner.Tick` is a one-shot cmd from `bubbles/spinner`. It fires once, returns a spinner frame. The spinner model produces a new `Tick()` cmd from its `Update` method - but `tui_message.go:29` does `return m, logoTickCmd()` for `logoTickMsg`, so **spinner.Tick never gets a chance to be re-queued** (because there's no explicit spinner handling, and `logoTickMsg` returns early).

- Result: after Init, only `logoTickCmd` keeps looping (animating the logo glyph on welcome screen). In chat mode, `logoTickMsg` also increments `logoFrame` - **but the logo frame is only read in `renderStatusBar` which is called from `View()`, and `View()` is only called after `Update()` processes a message**. No pollCmd = no Update = no View = frozen UI.

### 2.3 Root Cause #2: Poll Chain Breaks When Idle

`pollCmd()` is one-shot. It fires once, returns a `tuiTickMsg`. After drain:

```go
if len(tools) > 0 {
    // ... re-queue pollCmd()  ← only when tools
}
if stream != "" || done || doneErr != nil {
    // ... re-queue pollCmd()  ← only when stream or done
}
// nothing → no re-queue → DEAD
```

When the LLM is computing (no tokens yet, no tool calls), `Drain()` returns empty. No re-poll is scheduled. The UI freezes until the user types/clicks (any message triggers the drain block again).

### 2.4 Root Cause #3: tuiTickMsg Unhandled

Even if a stray `tuiTickMsg` arrives, it hits no `case tuiTickMsg:` in the switch. It falls through:

1. `textarea.Update(msg)` - Bubble Tea passes unknown msgs to the model; likely no-op
2. `viewport.Update(msg)` - same, no-op
3. `bridge.Drain()` - runs, but if empty, no re-poll

There is no code path that says: *"a tick arrived, re-render the status bar + re-schedule another tick."*

### 2.5 Architectural Issue: No Event Bus

```
AgentLoop → OnEvent → agentEventBridgeCallback → streamBridge → TUI
                                                                    ↑
                                                            (only consumer)
```

- **No pub/sub** - only 1 callback consumer. Session's `OnAgentEvent` is silently overridden by SendUserWithEvent.
- **No correlation IDs** - Event has `ToolCallID` but no trace ID, session ID, or parent span ID.
- **No async processing** - events are processed inline in the bridge; dropped when `notify` channel is full.
- **No extensibility** - adding OTEL tracing or external API forwarding requires rewriting the whole pipe.

---

## 3. Direct Answer: Will This Fix Messages, Tools & Thinking in Chat History?

**Yes - all three are fixed by Phase 1 alone.** Here's the precise trace for each:

### Streaming tokens (assistant messages appearing live)

| Step | Current (broken) | After Phase 1 |
|---|---|---|
| 1 | `bridge.Write()` appends to `pending`, calls `signal()` | Same |
| 2 | `pollCmd()` not running → nobody reads `notify` channel | `pollCmd()` fires every ~80ms from `tuiTickMsg` handler |
| 3 | Bridge data sits in buffer indefinitely | `tuiTickMsg` drains bridge → `streamBuf.WriteString(stream)` |
| 4 | `renderStreamVP()` never called | `renderStreamVP()` called → appends `streamBuf` to viewport → `viewport.SetContent()` |
| 5 | User must type/click to force a drain cycle | Viewport auto-updates every 80ms |

### Tool calls (names, statuses, appearing in history)

| Phase | Current (broken) | After Phase 1 |
|---|---|---|
| **Live** (tool rows in viewport) | `m.toolRows` populated via `applyToolEvents()` but never rendered in viewport - only count shown in status bar | Phase 1 §7.5 adds `renderToolPanelWindow()` call inside `renderStreamVP()` |
| **Final** (tool blocks in chat history) | `finishStream()` never fires because `bridge.done` is never drained | `tuiTickMsg` drains bridge → when `done=true`, calls `finishStream()` → `appendToolBlocks()` → permanent `ChatBlockTool` in history |

### Thinking blocks (model reasoning in history)

| Phase | Current (broken) | After Phase 1 |
|---|---|---|
| **Live** (thinking in viewport) | `bridge.thinking` buffered but only rendered when drain block runs | `tuiTickMsg` drains thinking → `m.thinkingBuf` → rendered by `renderStreamVP()` |
| **Final** (thinking block in history) | `finishStream()` never fires | `finishStream()` converts `m.thinkingBuf` → `ChatBlockThinking` in history |

### Summary

```
┌──────────────────────────────────────────────────────────────────┐
│  Before Phase 1:                                                 │
│                                                                  │
│  pollCmd() [DEAD] → no Update → no View → frozen screen         │
│       ↓                                                          │
│  bridge.Drain() never called → stream/tools/thinking/done        │
│       accumulate in bridge buffers forever                       │
│       ↓                                                          │
│  finishStream() never fires → no ChatBlocks in history           │
│       ↓                                                          │
│  View() returns stale content → user sees nothing                 │
│                                                                  │
│  After Phase 1:                                                  │
│                                                                  │
│  Init() → pollCmd() → tuiTickMsg handler → drain → apply →       │
│  renderStreamVP() → re-queue pollCmd() → [loop every 80ms]       │
│       ↓                                                          │
│  stream tokens → streamBuf → viewport content ✓                  │
│  tool events → toolRows → renderToolPanelWindow() → viewport ✓   │
│  thinking → thinkingBuf → renderStreamVP() → viewport ✓          │
│  done → finishStream() → ChatBlocks in history ✓                 │
└──────────────────────────────────────────────────────────────────┘
```

---

## 4. Design Goals & Non-Goals

### Goals

1. **Fix the UI freeze** - status bar, tool list, thinking indicator, elapsed time must update automatically.
2. **Event bus infrastructure** - typed, generic pub/sub for all system events.
3. **Adapter pattern** - UI adapter, OTEL adapter (future), External API adapter (future).
4. **Backward compatibility** - existing `agent.Event` and `streamBridge` keep working during migration.
5. **Extensibility** - new event kinds and new adapters can be added without touching existing ones.
6. **Async non-blocking** - event publishers never block; slow adapters drop or buffer.

### Non-Goals

- Replace Bubble Tea's `tea.Msg` / `tea.Cmd` model - the UI **adapts** events into `tea.Cmd` messages, not the other way around.
- Rewrite `agent.Loop` - the loop stays; its `OnEvent` hook publishes to the bus.
- Full OTEL implementation - only the interface and no-op adapter in Phase 2-4.
- External API adapter - designed for but not implemented.

---

## 5. Proposed Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                        EventBus (internal/events)                    │
│                                                                      │
│  ┌─────────┐   ┌────────────┐   ┌────────────┐   ┌──────────────┐  │
│  │ Publish  │──▶│ Subscriber │   │ Subscriber │   │ Subscriber   │  │
│  │ (sync)   │   │ Map        │   │ Map        │   │ Map          │  │
│  └─────────┘   │ (per-kind)  │   │ (per-kind) │   │ (per-kind)   │  │
│                └─────┬───────┘   └──────┬──────┘   └──────┬───────┘  │
│                      │                  │                  │          │
└──────────────────────┼──────────────────┼──────────────────┼──────────┘
                       ▼                  ▼                  ▼
                ┌────────────┐    ┌──────────────┐   ┌──────────────┐
                │ UIAdapter  │    │ OTELAdapter  │   │ ExtAPIAdapter│
                │ (internal  │    │ (future)     │   │ (future)     │
                │  /cli)     │    │              │   │              │
                └──────┬─────┘    └──────────────┘   └──────────────┘
                       │
                       ▼
                ┌────────────┐
                │ streamBridge│  (legacy - kept for compat)
                └──────┬─────┘
                       │
                       ▼
                ┌────────────┐
                │ pollCmd()   │  (Phase 1 fix: self-perpetuating)
                │ + case      │
                │ tuiTickMsg  │
                └────────────┘
```

### Event Bus - pub/sub in the middle

- Anyone publishes: `agent.Loop`, `chat.Session`, tools, TUI itself (user input, resize).
- Anyone subscribes: UI adapter, OTEL adapter, external API adapter.
- Wire format: `events.Event` - superset of `agent.Event` with additional system events, timestamps, correlation IDs.

---

## 6. Component Details

### 5.1 `internal/events/` - Event Bus Package

```
internal/events/
  bus.go          - Bus interface + implementation
  event.go        - EventKind, Event struct, SystemEventKinds
  handler.go      - Handler interface
  adapter.go      - Adapter base helpers
```

**`event.go`** - Extended event type:

```go
package events

import "time"

type Kind string

const (
    // Agent loop events (mirror agent.EventKind for compat)
    KindAssistant         Kind = "assistant"
    KindToolStart         Kind = "tool_start"
    KindToolEnd           Kind = "tool_end"
    KindStep              Kind = "step"
    KindPrune             Kind = "prune"
    KindToolParallel      Kind = "tool_parallel"
    KindSubagentStart     Kind = "subagent_start"
    KindSubagentEnd       Kind = "subagent_end"
    KindSubagentHeartbeat Kind = "subagent_heartbeat"

    // System / session lifecycle events
    KindSessionStart      Kind = "session_start"
    KindSessionEnd        Kind = "session_end"
    KindTurnStart         Kind = "turn_start"
    KindTurnEnd           Kind = "turn_end"

    // UI events
    KindUIResize          Kind = "ui_resize"
    KindUserInput         Kind = "user_input"
    KindUIReady           Kind = "ui_ready"

    // System events
    KindConfigChange      Kind = "config_change"
    KindError             Kind = "error"
)

type Event struct {
    Kind       Kind
    Timestamp  time.Time
    SessionID  string
    TurnID     string
    ToolCallID string
    Name       string
    Detail     string
    Content    string
    Input      string
    Output     string
    Metadata   map[string]string
    Err        error
}
```

**`bus.go`** - Generic, lock-free pub/sub:

```go
type Handler interface {
    HandleEvent(ctx context.Context, ev Event)
}

type Bus struct {
    mu     sync.RWMutex
    subs   map[Kind][]Handler
    closed bool
}

func New() *Bus { ... }
func (b *Bus) Publish(ev Event) {
    // Must be non-blocking for publishers
    // Spawn handlers in goroutines OR use buffered adapter channels
}
func (b *Bus) Subscribe(kinds []Kind, h Handler) { ... }
func (b *Bus) Unsubscribe(kinds []Kind, h Handler) { ... }
func (b *Bus) Close() { ... }
```

### 5.2 `UIAdapter` - Bridge between EventBus and Bubble Tea

```go
// internal/cli/ui_adapter.go

type UIAdapter struct {
    bus      *events.Bus
    bridge   *streamBridge          // kept for backward compat
    evChan   chan events.Event      // buffered, async
    pollDur  time.Duration          // 80ms default
}

func NewUIAdapter(bus *events.Bus, bridge *streamBridge) *UIAdapter {
    a := &UIAdapter{
        bus:     bus,
        bridge:  bridge,
        evChan:  make(chan events.Event, 256),
        pollDur: 80 * time.Millisecond,
    }
    bus.Subscribe(allEventKinds(), a)
    return a
}

func (a *UIAdapter) HandleEvent(ctx context.Context, ev events.Event) {
    select {
    case a.evChan <- ev:
    default:
        // drop if channel full (backpressure for slow UI)
    }
}

// PollCmd returns a self-perpetuating tea.Cmd for the Bubble Tea event loop.
func (a *UIAdapter) PollCmd() tea.Cmd {
    return func() tea.Msg {
        select {
        case ev := <-a.evChan:
            return uiEventMsg{event: ev}
        case <-time.After(a.pollDur):
            return uiTickMsg{}   // periodic heartbeat even when idle
        }
    }
}
```

**Key differences from current `pollCmd`:**
- Reads from a dedicated event channel, not shared bridge
- Returns `uiEventMsg{event}` for new events + `uiTickMsg{}` for heartbeat
- Always re-scheduled (self-perpetuating) - see Phase 1 fix

### 5.3 Wire adapter connections

```
AgentLoop Run():
    opts.OnEvent = func(e agent.Event) {
        bus.Publish(adaptAgentEvent(e))  // primary
        // Also call legacy bridge for backward compat during migration
    }

tuiModel Update():
    case uiEventMsg:
        // Apply event to model state directly (no bridge drain needed)
        m.applyEvent(ev)
        m.renderStreamVP()
        return m, m.uiAdapter.PollCmd()  // always re-schedule
    case uiTickMsg:
        // Periodic heartbeat - re-render status bar, check stalled
        return m, m.uiAdapter.PollCmd()  // always re-schedule
```

### 5.4 `OTELAdapter` - Interface (future)

```go
type OTELAdapter struct {
    bus    *events.Bus
    tracer trace.Tracer
}

func (a *OTELAdapter) HandleEvent(ctx context.Context, ev events.Event) {
    switch ev.Kind {
    case KindToolStart:
        _, span := a.tracer.Start(ctx, "tool."+ev.Name)
        span.SetAttributes(...)
        // store span for end event
    case KindToolEnd:
        // retrieve span, record result, end
    }
}
```

### 5.5 `ExtAPIAdapter` - Design sketch (future)

```go
type ExtAPIAdapter struct {
    bus     *events.Bus
    client  *http.Client
    endpoint string
}

func (a *ExtAPIAdapter) HandleEvent(ctx context.Context, ev events.Event) {
    // POST ev as JSON to external endpoint
    // Non-blocking; errors go to log, not event bus (avoid loops)
}
```

---

## 7. Migration Phases

### Phase 1 - Fix the Poll Chain (Immediate, high priority)
*Files: `internal/cli/tui.go`, `internal/cli/tui_message.go`*

### Phase 2 - EventBus Infrastructure (Medium)
*New: `internal/events/` package*

### Phase 3 - Wire Agent Loop → EventBus (Medium)
*Files: `internal/agent/loop.go`, `internal/cli/tui_events.go`, new `internal/cli/ui_adapter.go`*

### Phase 4 - OTEL Adapter (Optional, Low)
*New: `internal/events/otel_adapter.go`*

---

## 8. Phase 1: Fix the Poll Chain (Immediate)

This is the minimal, surgical fix that unblocks the UI refresh bug. It touches only 2 files.

### 8.1 Add `pollCmd()` to `Init()`

```diff
 func (m *tuiModel) Init() tea.Cmd {
-    return tea.Batch(m.spinner.Tick, tea.EnterAltScreen, logoTickCmd())
+    return tea.Batch(m.spinner.Tick, tea.EnterAltScreen, logoTickCmd(), m.pollCmd())
 }
```

### 8.2 Make `pollCmd()` self-perpetuating

The current `pollCmd()` is one-shot. Make it always re-queue itself:

```go
func (m *tuiModel) pollCmd() tea.Cmd {
    return func() tea.Msg {
        m.mu.Lock()
        bridge := m.bridge
        m.mu.Unlock()
        if bridge == nil {
            return nil
        }
        select {
        case <-bridge.notify:
            return tuiTickMsg{bridge: bridge}
        case <-time.After(80 * time.Millisecond):
            return tuiTickMsg{bridge: bridge}
        }
    }
}
```

**Change:** The command itself remains one-shot, but its *consumer* always re-queues it. See next section.

### 8.3 Add `case tuiTickMsg:` + always re-queue

In `updateMessageImpl`:

```diff
+   case tuiTickMsg:
+       if m.mode == modeChat {
+           // Drain the bridge (tools, stream, done, thinking, stepDetail)
+           m.mu.Lock()
+           stream, tools, done, doneErr, thinking, stepDetail, stepDetailAt := m.bridge.Drain()
+           m.mu.Unlock()
+           m.updateFromDrain(stream, tools, done, doneErr, thinking, stepDetail, stepDetailAt)
+       }
+       cmds = append(cmds, m.pollCmd())  // always re-schedule
```

And consolidate the drain handling into `updateFromDrain()`:

```go
func (m *tuiModel) updateFromDrain(stream string, tools []bridgeToolEvt, done bool, doneErr error, thinking string, stepDetail string, stepDetailAt time.Time) {
    m.stepDetail = stepDetail
    if !stepDetailAt.IsZero() {
        m.stepDetailAt = stepDetailAt
    }
    if len(tools) > 0 {
        m.applyToolEvents(tools)
        if m.waiting && !m.stalledWarning {
            m.layout()
            m.renderStreamVP()
        }
    }
    if stream != "" || done || doneErr != nil {
        if stream != "" {
            m.streamBuf.WriteString(stream)
        }
        if done || doneErr != nil {
            // finishStream will handle
        }
        if !done {
            m.renderStreamVP()
        }
    }
    if thinking != "" {
        m.thinkingBuf.WriteString(thinking)
        if !done {
            m.renderStreamVP()
        }
    }
    // Also check stalled warning
    if m.waiting && time.Since(m.turnStart) > 5*time.Second && !m.stalledWarning {
        // set stalled warning
    }
}
```

**Key insight:** The bridge drain block for `KeyMsg` / `MouseMsg` must also be kept (to catch data on user interaction), but the `tuiTickMsg` case is the **primary continuous poll path**.

### 8.4 Remove data-dependent poll re-queuing from drain block

In the existing drain block (lines 130-160), remove the conditional `pollCmd()` calls since `tuiTickMsg` always re-queues:

```diff
-       if len(tools) > 0 {
-           ...
-           cmds = append(cmds, m.pollCmd())
-       }
-       if stream != "" || done || doneErr != nil {
-           ...
-           cmds = append(cmds, m.pollCmd())
-       }
+       // pollCmd is always re-queued by the caller (tuiTickMsg handler)
```

**But careful:** the existing drain block also runs on `KeyMsg` and `MouseMsg`. Those no longer need to re-queue `pollCmd()` because `tuiTickMsg` runs continuously.

### 8.5 Add tool panel rows to `renderStreamVP()`

Currently `renderStreamVP()` only shows blocks + stream buffer + thinking. Tool rows are invisible. Fix:

```diff
 func (m *tuiModel) renderStreamVP() {
     m.hitMap.invalidate()
     content := m.buildViewportContent()
+    // Insert live tool panel between blocks and stream content
+    if len(m.toolRows) > 0 {
++       _, doneTools, _ := countTools(m.toolRows)
++       openTools := len(m.toolRows) - doneTools
+        toolContent, toolLines, _ := renderToolPanelWindow(
+            m.toolRows, m.width, time.Now(), m.toolPanel,
+            m.logoFrame,
+            deriveBrandPhase(m.waiting, openTools, m.streamBuf.Len(), len(m.pendingQueue), false),
+            toolMaxVisibleRows,
+            visualLineCount(content),
+        )
+        if toolContent != "" {
+            if content != "" {
+                content += "\n"
+            }
+            content += toolContent
+        }
+    }
     if m.streamBuf.Len() > 0 {
         ...
```

**Key detail:** `countTools()` returns `(open, done, total)` - there is NO `countToolsDone()` function. Must derive `openTools = total - doneTools`.

**YBase parameter:** The 8th arg to `renderToolPanelWindow()` is `yBase int` - the absolute screen Y of the first tool row (for mouse hit detection). Using `visualLineCount(content)` gives an approximate position. This is acceptable - mouse hits in the tool panel during streaming are best-effort; exact Y offsets are corrected on the next full `renderVP()` after turn end.

This is the minimal change - it calls the existing `renderToolPanelWindow()` which already formats tool rows correctly and handles scrolling/expansion. The key is just **calling it** from the live render path.

### 8.6 Phase 1 Validation

**Test checklist:**

1. **Cold start:** Launch TUI, send a message. Status bar shows elapsed time updating every ~80ms without any key/mouse interaction.
2. **Between tool calls:** During "LLM thinking" (no tokens, no tool calls), the status bar, queue count, and elapsed time update every 80ms.
3. **Streaming:** When tokens arrive, content appears in viewport continuously.
4. **Tool events:** Tool starts and ends appear in the tool strip without user interaction.
5. **Stalled warning:** If agent stalls >5s, stalled warning appears automatically.
6. **User interaction still works:** Typing, mouse scroll, clicks, queuing - all existing behavior preserved.

```go
// Test: pollCmd is returned from Init
func TestInitReturnsPollCmd(t *testing.T) {
    m := newTUIModel(...)
    cmd := m.Init()
    // cmd is a batch; verify pollCmd is in it
}

// Test: tuiTickMsg handler always re-queues pollCmd
func TestTuiTickMsgRequeuesPoll(t *testing.T) {
    m := newTUIModel(...)
    m.mode = modeChat
    m.bridge = newStreamBridge()
    _, cmd := m.Update(tuiTickMsg{bridge: m.bridge})
    // cmd must be non-nil (pollCmd always re-queued)
}

// Test: status bar updates without interaction
func TestStatusBarUpdatesWithoutInput(t *testing.T) {
    // Set up model with a running agent
    // Fire multiple tuiTickMsg without any key/mouse events
    // Verify stepDetail, elapsed time propagate to View()
}
```

---

## 9. Phase 2: EventBus Infrastructure

### 9.1 New package: `internal/events/`

**`internal/events/event.go`:**

```go
package events

import "time"

type Kind string

const (
    KindAssistant         Kind = "assistant"
    KindToolStart         Kind = "tool_start"
    KindToolEnd           Kind = "tool_end"
    KindStep              Kind = "step"
    KindPrune             Kind = "prune"
    KindToolParallel      Kind = "tool_parallel"
    KindSubagentStart     Kind = "subagent_start"
    KindSubagentEnd       Kind = "subagent_end"
    KindSubagentHeartbeat Kind = "subagent_heartbeat"

    KindSessionStart      Kind = "session_start"
    KindSessionEnd        Kind = "session_end"
    KindTurnStart         Kind = "turn_start"
    KindTurnEnd           Kind = "turn_end"
    KindUIResize          Kind = "ui_resize"
    KindUserInput         Kind = "user_input"
    KindUIReady           Kind = "ui_ready"
    KindConfigChange      Kind = "config_change"
    KindError             Kind = "error"
)

type Event struct {
    Kind       Kind
    Timestamp  time.Time
    SessionID  string
    TurnID     string
    ToolCallID string
    Name       string
    Detail     string
    Content    string
    Input      string
    Output     string
    Metadata   map[string]string
    Err        error
}

func NewEvent(kind Kind) Event {
    return Event{Kind: kind, Timestamp: time.Now()}
}
```

**`internal/events/handler.go`:**

```go
package events

import "context"

type Handler interface {
    HandleEvent(ctx context.Context, ev Event)
}

type HandlerFunc func(ctx context.Context, ev Event)

func (f HandlerFunc) HandleEvent(ctx context.Context, ev Event) {
    f(ctx, ev)
}
```

**`internal/events/bus.go`:**

```go
package events

import (
    "context"
    "sync"
)

type Bus struct {
    mu   sync.RWMutex
    subs map[Kind][]Handler
}

func New() *Bus {
    return &Bus{
        subs: make(map[Kind][]Handler),
    }
}

func (b *Bus) Publish(ev Event) {
    b.mu.RLock()
    handlers := b.subs[ev.Kind]
    b.mu.RUnlock()
    for _, h := range handlers {
        h.HandleEvent(context.Background(), ev)
    }
}

func (b *Bus) Subscribe(kind Kind, h Handler) {
    b.mu.Lock()
    defer b.mu.Unlock()
    b.subs[kind] = append(b.subs[kind], h)
}

func (b *Bus) SubscribeMany(kinds []Kind, h Handler) {
    for _, k := range kinds {
        b.Subscribe(k, h)
    }
}

func (b *Bus) Unsubscribe(kind Kind, target Handler) {
    b.mu.Lock()
    defer b.mu.Unlock()
    handlers := b.subs[kind]
    for i, h := range handlers {
        // Use comparable wrapper or pointer identity
        if h == target {  // Note: simple pointer comparison
            b.subs[kind] = append(handlers[:i], handlers[i+1:]...)
            return
        }
    }
}
```

**Design decision: sync or async?**

**Sync** (as shown): `Publish` calls handlers inline. This is simpler and avoids goroutine lifecycle management. The cost is that a slow handler blocks the publisher - **but** adapters should buffer internally:

```go
// UIAdapter buffers via chan
func (a *UIAdapter) HandleEvent(ctx context.Context, ev events.Event) {
    select {
    case a.evChan <- ev:
    default: // drop
    }
}
```

**Async alternative** (spawn goroutine per handler per event): adds complexity, goroutine leaks on unsubscribe, ordering issues. Not recommended for v1.

### 9.2 Testing

```go
func TestBusPublishSubscribe(t *testing.T) {
    bus := New()
    got := make(chan Event, 1)
    bus.Subscribe(KindToolStart, HandlerFunc(func(ctx context.Context, ev Event) {
        got <- ev
    }))
    bus.Publish(NewEvent(KindToolStart))
    select {
    case ev := <-got:
        if ev.Kind != KindToolStart {
            t.Fatalf("expected KindToolStart, got %s", ev.Kind)
        }
    case <-time.After(time.Second):
        t.Fatal("timeout waiting for event")
    }
}

func TestBusMultipleHandlers(t *testing.T) {
    // Verify all handlers are called
}
```

---

## 10. Phase 3: Wire Agent Loop → EventBus

### 10.1 Create UIAdapter

**`internal/cli/ui_adapter.go`:**

```go
package cli

import (
    "context"
    "time"
    "github.com/MiviaLabs/mivia-agent/internal/events"
    tea "github.com/charmbracelet/bubbletea"
)

// uiEventMsg carries a single event from the EventBus to the TUI.
type uiEventMsg struct {
    event events.Event
}

// uiTickMsg is a periodic heartbeat when no events arrive.
type uiTickMsg struct{}

// UIAdapter bridges the EventBus to the Bubble Tea TUI.
// It subscribes to all event kinds and forwards them to the TUI
// via a buffered channel consumed by PollCmd().
type UIAdapter struct {
    bus     *events.Bus
    bridge  *streamBridge    // kept for backward compat (Phase 3 migration)
    evChan  chan events.Event
    pollDur time.Duration
}

func NewUIAdapter(bus *events.Bus, bridge *streamBridge) *UIAdapter {
    a := &UIAdapter{
        bus:     bus,
        bridge:  bridge,
        evChan:  make(chan events.Event, 512),
        pollDur: 80 * time.Millisecond,
    }
    // Subscribe to all agent and system events
    allKinds := []events.Kind{
        events.KindAssistant, events.KindToolStart, events.KindToolEnd,
        events.KindStep, events.KindPrune, events.KindToolParallel,
        events.KindSubagentStart, events.KindSubagentEnd, events.KindSubagentHeartbeat,
        events.KindTurnStart, events.KindTurnEnd,
        events.KindUIResize, events.KindUserInput, events.KindError,
    }
    bus.SubscribeMany(allKinds, a)
    return a
}

func (a *UIAdapter) HandleEvent(ctx context.Context, ev events.Event) {
    select {
    case a.evChan <- ev:
    default:
        // Backpressure: drop if channel full
        // In future: log drop counter
    }
}

// PollCmd returns a self-perpetuating tea.Cmd that either:
// - Returns the next event from the channel as uiEventMsg, or
// - Returns uiTickMsg after pollDur timeout (heartbeat)
func (a *UIAdapter) PollCmd() tea.Cmd {
    return func() tea.Msg {
        select {
        case ev := <-a.evChan:
            return uiEventMsg{event: ev}
        case <-time.After(a.pollDur):
            return uiTickMsg{}
        }
    }
}
```

### 10.2 Wire EventBus into runTUI

```diff
 func runTUI(sess *chat.Session, res *config.Resolved, toolsOn bool) error {
     ...
     model := newTUIModel(sess, res, toolsOn)
+    // Create the global event bus for this session
+    bus := events.New()
+    model.eventBus = bus
+    model.uiAdapter = NewUIAdapter(bus, model.bridge)
     p := tea.NewProgram(model, tea.WithAltScreen())
     ...
 }
```

### 10.3 Agent Loop publishes to EventBus

**`internal/agent/loop.go`** - Add parallel event bus publishing:

```go
type Options struct {
    ...
    OnEvent     func(Event)       // legacy callback
    EventBus    *events.Bus       // NEW: event bus for extensible delivery
}

// In Run(), where events are emitted:
if opts.OnEvent != nil {
    opts.OnEvent(e)
}
if opts.EventBus != nil {
    opts.EventBus.Publish(adaptToEvent(e))
}
```

**`internal/events/adapter.go`** - Helper to convert `agent.Event` → `events.Event`:

```go
func FromAgentEvent(e agent.Event, sessionID, turnID string) events.Event {
    return events.Event{
        Kind:       events.Kind(e.Kind),
        Timestamp:  time.Now(),
        SessionID:  sessionID,
        TurnID:     turnID,
        ToolCallID: e.ToolCallID,
        Name:       e.Name,
        Detail:     e.Detail,
        Content:    e.Content,
        Input:      e.Input,
        Output:     e.Output,
    }
}
```

### 10.4 replace streamBridge Drain with adapter PollCmd in TUI

```diff
 func (m *tuiModel) updateMessage(msg tea.Msg) (tea.Model, tea.Cmd) {
     switch msg := msg.(type) {
+    case uiEventMsg:
+        m.applyEvent(msg.event)
+        if m.mode == modeChat {
+            m.renderStreamVP()
+        }
+        return m, m.uiAdapter.PollCmd()  // always re-schedule
+    case uiTickMsg:
+        // Periodic heartbeat: re-render status bar, check stalled
+        if m.mode == modeChat && m.waiting {
+            // View() will read m.stepDetail, m.turnStart, etc.
+            m.checkStalled()
+        }
+        return m, m.uiAdapter.PollCmd()  // always re-schedule
     case tuiTickMsg:
         // LEGACY - backward compat during migration
-        // ... old drain code (removed in Phase 3)
+        // Delegate to adapter path
+        return m, m.uiAdapter.PollCmd()
```

### 10.5 Session lifecycle events

```diff
 // In startAI or SendUserWithEvent:
+eventBus.Publish(events.Event{
+    Kind:      events.KindTurnStart,
+    Timestamp: time.Now(),
+    SessionID: sessionID,
+    TurnID:    fmt.Sprintf("turn:%d", turnID),
+    Detail:    userText,
+})
```

### 10.6 Remove bridge Drain from KeyMsg/MouseMsg (Phase 3 final)

Once the adapter path is stable, the bridge drain in the `KeyMsg`/`MouseMsg` fallthrough can be removed. All event data arrives via `uiEventMsg` from the adapter.

### 10.7 Migrate SetSubagentProgress global → EventBus (Phase 3)

The current `SetSubagentProgress` global (`internal/cli/subagent_progress.go`) is a global function pointer sink for subagent events. It must be migrated to the EventBus:

```diff
 // Emit subagent event to the registered parent handler.
 func emitSubagentProgress(e agent.Event) {
     subagentProgress.mu.RLock()
     fn := subagentProgress.fn
     subagentProgress.mu.RUnlock()
     if fn != nil {
         fn(e)
     }
+    // ALSO publish to EventBus in Phase 3
+    if globalBus != nil {
+        globalBus.Publish(FromAgentEvent(e, "", ""))
+    }
 }
```

**Migration plan:**
1. Phase 3 adds EventBus publishing in `emitSubagentProgress()` alongside the existing callback
2. Remove `subagentProgress` global + `SetSubagentProgress()` when all consumers have migrated to EventBus
3. The `globalBus` variable can be set once in `runTUI()` during initialization

**Design note:** `SetSubagentProgress` is set to `nil` after each turn (see `startAI` in `tui.go:369-370`). With EventBus, the subscription is persistent - no need to nil/reset. This simplifies lifecycle management.

---

## 11. Phase 4: OTEL Adapter (Optional - Not Implemented Yet)

This phase adds OpenTelemetry tracing support. The adapter is **designed but not implemented** - the interface, subscription, and stub code are ready, but the actual OpenTelemetry calls are `// NOT IMPLEMENTED` so the code compiles and runs as a no-op.

### 11.1 `internal/events/otel_adapter.go`

```go
// Package events - OpenTelemetry adapter (NOT IMPLEMENTED).
// This file defines the OTELAdapter type and subscribes to the event bus,
// but all tracing calls are stubbed. To activate: uncomment the
// go.opentelemetry.io/otel imports and replace the // NOT IMPLEMENTED
// stubs with real trace/span calls.
package events

import (
    "context"
    "sync"
)

// OTELAdapter subscribes to the event bus and creates OpenTelemetry spans
// for agent loop events (tool calls, subagent invocations, turns).
//
// Current status: NOT IMPLEMENTED. All methods compile but produce no spans.
// See phase-4-todo checklist below.
type OTELAdapter struct {
    bus     *Bus
    // NOT IMPLEMENTED: tracer trace.Tracer
    // NOT IMPLEMENTED: spanMap sync.Map  // ToolCallID → trace.Span
}

// NewOTELAdapter creates an OTELAdapter and subscribes it to the event bus.
// The adapter subscribes to tool lifecycle and turn events.
// It is a no-op until OTEL dependencies are added and stubs are replaced.
func NewOTELAdapter(bus *Bus) *OTELAdapter {
    a := &OTELAdapter{bus: bus}
    bus.SubscribeMany([]Kind{
        KindToolStart, KindToolEnd,
        KindSubagentStart, KindSubagentEnd,
        KindTurnStart, KindTurnEnd,
        KindError,
    }, a)
    return a
}

// HandleEvent processes an event and (in future) creates/ends OpenTelemetry spans.
// Currently: NOT IMPLEMENTED - no-op.
func (a *OTELAdapter) HandleEvent(ctx context.Context, ev Event) {
    // NOT IMPLEMENTED: switch ev.Kind {
    // NOT IMPLEMENTED: case KindToolStart:
    // NOT IMPLEMENTED:     _, span := a.tracer.Start(ctx, "tool."+ev.Name,
    // NOT IMPLEMENTED:         trace.WithAttributes(...))
    // NOT IMPLEMENTED:     a.spanMap.Store(ev.ToolCallID, span)
    // NOT IMPLEMENTED: case KindToolEnd:
    // NOT IMPLEMENTED:     if span, ok := a.spanMap.Load(ev.ToolCallID); ok {
    // NOT IMPLEMENTED:         span.(trace.Span).End()
    // NOT IMPLEMENTED:         a.spanMap.Delete(ev.ToolCallID)
    // NOT IMPLEMENTED:     }
    // NOT IMPLEMENTED: }
    _ = ctx
    _ = ev
}

// Close is a no-op placeholder.
func (a *OTELAdapter) Close() error {
    // NOT IMPLEMENTED: flush pending spans
    return nil
}
```

### Phase 4 TODO checklist (when OTEL is activated)

- [ ] Add `go.opentelemetry.io/otel` dependency to `go.mod`
- [ ] Replace `// NOT IMPLEMENTED` comments with real trace/span calls in `otel_adapter.go`
- [ ] Wire tracer from session context or config
- [ ] Add `OTELAdapter` to `runTUI()` alongside `UIAdapter`
- [ ] Test with OTEL exporter (stdout, jaeger, etc.)
- [ ] Add span attributes: `tool.call_id`, `tool.input` (truncated), `tool.output` (truncated), `tool.error`
- [ ] Add span events for key lifecycle transitions (queued → running → completed)
- [ ] Ensure OTEL adapter never blocks event bus (buffer drops on overflow)

### 11.2 Design invariants (keep these when implementing)

- **Non-blocking**: Adapter's `HandleEvent` must never block the event bus. Use a buffered channel with drop-on-overflow if spans must be flushed asynchronously.
- **Resource cleanup**: `Close()` must flush remaining spans and unsubscribe from the bus.
- **Privacy**: Tool inputs/outputs must be truncated (max 1024 chars) before storing as span attributes.
- **No event bus loops**: The adapter must never publish events to the bus it subscribes to.
- **Zero dependency at compile time**: The adapter must be in a separate file guarded by build tags OR behind a nil check in `runTUI()` so the main binary doesn't require OTEL.

---

## 12. Validation & Questions

### 12.1 Audit Validation Results

The plan was audited by 3 sub-agents. Below are the findings, challenged and verified against the actual codebase.

| # | Audit Finding | Verified? | Impact on Plan |
|---|---|---|---|
| 1 | `countToolsDone()` doesn't exist - plan references nonexistent function | ✅ **CONFIRMED** | **Fixed** in §7.5 - replaced with `countTools()` + manual `open = len - done` |
| 2 | `renderToolPanelWindow()` never called from main rendering path | ✅ **CONFIRMED** | Confirms §7.5 is needed; only callers are test/bench files |
| 3 | `tuiTickMsg` has no `case` handler currently | ✅ **CONFIRMED** | Confirms §7.3 addition is correct |
| 4 | Dual drain risk: tuiTickMsg drains + fallthrough also drains | ❌ **CHALLENGED - no risk** | `case tuiTickMsg:` returns early (before fallthrough). On KeyMsg/MouseMsg, only fallthrough runs. `Bridge.Drain()` is atomic (under mutex) - first drain clears all data, second drain gets nothing. |
| 5 | Race condition on bridge swap in `startAI` | ❌ **CHALLENGED - no race** | `pollCmd()` reads `m.bridge` under `m.mu.Lock()`. `startAI()` writes `m.bridge` under `m.mu.Lock()`. Writes to closed bridge are silently dropped. Safe. |
| 6 | `logoTickMsg` early return conflicts with `tuiTickMsg` | ❌ **CHALLENGED - no conflict** | `case logoTickMsg:` returns early with `logoTickCmd()`. `case tuiTickMsg:` (new) returns early with `pollCmd()`. Both run independently. |
| 7 | OTELAdapter stub won't compile | ❌ **CHALLENGED - will compile** | All OTEL types are in `// NOT IMPLEMENTED` comments. `_ = ctx`, `_ = ev` suppress unused errors. `Close()` returns nil. Valid Go. |
| 8 | Status bar doesn't update without tool events | ⚠️ **PARTIALLY CONFIRMED** | `stepDetail` only changes on event data. But `time.Since(m.turnStart)`, queue count, and active tools count are recomputed in `View()` every tick cycle. Elapsed timer always updates. Acceptable. |
| 9 | SetSubagentProgress global should go through EventBus | ✅ **CONFIRMED - needs design note** | Added to Phase 3 migration notes. Subagent progress must be routed through EventBus, not the global callback. |
| 10 | `session.OnAgentEvent` silently overridden | ✅ **CONFIRMED** | The plan handles this correctly: EventBus is publish-only (no override). Both callback AND bus fire in Phase 3. |
| 11 | No way to detect agent is running vs idle - `m.mode == modeChat` is not enough | ⚠️ **PARTIALLY CONFIRMED** | The `modeChat` guard works for Phase 1 (welcome screen vs active chat). For Phase 2-3, an explicit `isAgentRunning` field may be cleaner, but is not critical. |

### 12.2 Self-Critique: Challenging This Plan

| Question | Answer |
|---|---|
| **Is an EventBus overkill for a CLI tool?** | No - the current callback chain is already brittle (one consumer, no metadata, silent override). The bus adds ~200 LOC and replaces ~50 LOC of hand-rolled plumbing. The extensibility (OTEL, API) justifies it. |
| **Does `bus.Publish` in agent loop add latency?** | Sync dispatch to N handlers is O(N×handlers). With 2 handlers (UI + OTEL) and very fast handler dispatch, overhead is <1µs per event. Agent loop generates ~10-100 events per turn; this is negligible vs LLM latency (seconds). |
| **Channel buffering vs dropping?** | 512-entry buffer for UI adapter is generous. At 80ms poll, that's ~40 seconds of events. If the UI is backlogged, dropping is better than blocking the agent loop. |
| **Why keep `streamBridge` during Phase 3?** | Backward compatibility and incremental migration. `streamBridge` still serves as the `io.Writer` for streaming tokens. The goal is to eventually replace `Drain()` with the adapter's event channel. |
| **What about `agentEventBridgeCallback`?** | It becomes redundant once `EventBus` is the primary path. It's kept during Phase 2-3 for the legacy bridge, then removed in Phase 3 final. |
| **Thread safety of Bus.Publish?** | Yes - `Publish` holds read lock, iterates handlers, calls each. Handlers must be non-blocking. The UI adapter is non-blocking (channel send). |
| **How do we test the fix?** | Unit tests for `pollCmd` always re-queuing, `tuiTickMsg` handler, and integration test that fires multiple ticks without user input and asserts viewport/status bar update. |

### 12.3 Risks & Mitigations

| Risk | Mitigation |
|---|---|
| **PollCmd flood:** 80ms ticks always re-queued, even when agent is idle (welcome screen). | Guard with `if m.mode == modeChat` before re-rendering. On welcome screen, tick is cheap (no-op). |
| **EventBus becomes bottleneck:** 10 adapters, 1000 events/sec. | Sync dispatch is fine for <100 handlers. Monitor in profiling. If needed, switch to concurrent dispatch (goroutine per handler). |
| **Migration complexity:** 3 phases touch same code paths. | Explicit phase gates: Phase 1 is a 2-file change. Phase 2 is pure addition. Phase 3 wires without removing Phase 1 until stable. |
| **Double event delivery** during Phase 3: both bridge.Drain and adapter path process the same event. | In Phase 3, disable bridge.Drain in tuiTickMsg handler (it becomes adapter-only). KeyMsg/MouseMsg fallthrough path still drains bridge for safety, but events are idempotent. |

### 12.4 Open Questions

1. **Should Bus.Publish be async (goroutine per handler)?** Sync is simpler and fast enough. Benchmark before deciding.
2. **Should the OTEL adapter live in `internal/events/` or `internal/otel/`?** `internal/events/` - co-located with the bus interface for cohesive evolution.
3. **Should `sessionID` and `turnID` be injected via context or event fields?** Fields on the event - simpler to trace, no context-wrangling in the agent loop.
4. **What about `ChatBlock` rendering?** The UI adapter should handle `KindAssistant` events by appending to `streamBuf`, not rebuilding all blocks. The `View()` function handles full block rendering. Keep the rendering path unchanged.

---

## 13. Orchestrator Prompt: Implement This Plan End-to-End (TDD)

Copy the block below and dispatch it to a new agent to implement the full plan. The agent will execute phases sequentially, with **test-driven development** (TDD), bug-audit loops, gap detection, and DOD validation gates between each phase.

---

<task>
You are implementing the event bus and UI refresh architecture plan at `.mivia/events-eventbus-refactor-plan.md`.

**Read the full plan now.** You will execute Phases 1 through 4 sequentially. **Every phase follows strict TDD: write the test FIRST, see it fail (RED), implement the code, see it pass (GREEN), then refactor.** Do not skip ahead. Do not merge phases.

---

## TDD Non-Negotiables (apply to every phase)

```
For EVERY code change (new function, new type, new method, modified logic):
  1. RED:   Write the test(s) first. Run them. They FAIL with the expected error/panic/compile-time message.
  2. GREEN: Implement the minimal code to make the tests pass. Run tests. They PASS.
  3. REFACTOR: Clean up. Run tests again. They still PASS.
  4. VERIFY: Run ALL existing tests (`go test ./...`). Zero regressions.

For NEW packages/files:
  1. Write the _test.go file FIRST with the interface/API you expect.
  2. The test file must compile (even if the non-test code doesn't exist yet) - use type stubs if needed.
  3. When stubs satisfy compilation, run tests → RED (expected failures).
  4. Fill in real implementation → run tests → GREEN.
  5. Add edge case tests (nil, empty, concurrent, lifecycle boundaries).
  6. Run with `-race` to catch data races.

For MODIFIED existing code:
  1. Write a test that DEMONSTRATES the current bug or missing behaviour.
  2. Run it. It FAILS (or demonstrates the gap).
  3. Implement the fix.
  4. Run it. It PASSES.
  5. Run `go test ./...` - zero regressions.

NEVER:
  - Write implementation before a test exists for it.
  - Skip the RED step (you didn't TDD).
  - Ignore a failing test to "fix it later".
  - Add untested helper functions.
  - Leave unused imports, variables, or dead code.
```

---

## Phase 1: Fix the Poll Chain

**Files to change:**
- `internal/cli/tui.go` - add `pollCmd()` to `Init()`
- `internal/cli/tui_message.go` - add `case tuiTickMsg:` handler that drains + always re-queues `pollCmd()`
- `internal/cli/tui_layout.go` - add `updateFromDrain()` helper; add tool panel rendering to `renderStreamVP()`

### TDD Steps (Phase 1)

1. **Write tests FIRST** - Create or update tests in `internal/cli/`:
   - `TestInitReturnsPollCmd` - verify `Init()` returns a cmd that includes `pollCmd()` (use `tea.Batch` inspection or detect `tuiTickMsg` production)
   - `TestTuiTickMsgAlwaysRequeuesPoll` - send `tuiTickMsg{bridge: ...}` to `Update()`, verify returned cmd re-queues `pollCmd()` (non-nil batch containing a func)
   - `TestTuiTickMsgDoesNotDependOnData` - with empty bridge, tick should still re-queue (test the always-re-queue invariant)
   - `TestStreamVPIncludesToolPanel` - populate `m.toolRows`, call `renderStreamVP()`, verify output contains tool row content (check for `toolMaxVisibleRows` or tool row text patterns)
   - `TestBridgeDrainNotDoubleProcessed` - verify that after bridge drain in tuiTickMsg, the fallthrough path (KeyMsg/MouseMsg) does NOT call `Drain()` again or re-queue `pollCmd()`

2. **Run tests → RED** - they fail (expected, the code doesn't exist yet)

3. **Implement:**
   - `Init()` adds `m.pollCmd()` to the batch
   - `updateMessageImpl` gets `case tuiTickMsg:` before all other cases, drains bridge, calls `updateFromDrain()`, always re-queues `m.pollCmd()`, returns early
   - `updateFromDrain()` helper in `tui_layout.go`: sets `stepDetail`, applies tool events, writes stream/thinking buffers, renders `renderStreamVP()`
   - `renderStreamVP()` calls `renderToolPanelWindow()` to inject live tool rows

4. **Run tests → GREEN**

5. **Bug-Audit** - Read every changed line and verify:
   - Is `pollCmd()` returned from `Init()`?
   - Does `case tuiTickMsg:` always call `m.pollCmd()` before returning? (never conditionally)
   - Does the tuiTickMsg handler drain the bridge EARLY in the switch (before textarea/viewport updates)? (yes - it returns early)
   - In the fallthrough path (KeyMsg/MouseMsg), are conditional `pollCmd()` re-queues REMOVED? (they're redundant now)
   - Does `renderStreamVP()` call `renderToolPanelWindow()` to show live tool rows?
   - Is `countTools()` used correctly (not `countToolsDone()` which doesn't exist)?
   - Is the bridge access (`m.bridge`) always under `m.mu.Lock()`?
   - Are all new functions/methods covered by the tests you wrote in step 1?

6. **If any issue found** → fix it, re-run tests, re-audit.

7. **Write `.mivia/phase1-complete.md`** summarizing changes, test coverage, and any deviations.

### DOD Gate (Phase 1 → Phase 2)
- [ ] TDD was followed: tests written FIRST (RED), then implementation (GREEN)
- [ ] `go build ./cmd/mivia/` succeeds
- [ ] `go test ./internal/cli/...` passes (zero failures)
- [ ] `go test ./internal/agent/...` passes (zero regressions)
- [ ] `go test ./...` passes (full suite)
- [ ] `go vet ./...` is clean
- [ ] `countToolsDone` does NOT appear anywhere in the codebase (grep for it)
- [ ] `case tuiTickMsg:` exists in `updateMessageImpl` and always re-queues `pollCmd()`
- [ ] `pollCmd()` is returned from `Init()`
- [ ] `renderToolPanelWindow()` is called from `renderStreamVP()`
- [ ] At least 3 new tests were added for Phase 1 changes
- [ ] All new tests follow deterministic patterns (no `time.Sleep`, no global state races)

---

## Phase 2: EventBus Infrastructure

**New files:**
- `internal/events/event.go` - `Kind` type, `Event` struct with all 16+ kinds, `NewEvent()` constructor
- `internal/events/handler.go` - `Handler` interface, `HandlerFunc` adapter
- `internal/events/bus.go` - `Bus` struct with `New()`, `Publish()`, `Subscribe()`, `SubscribeMany()`, `Close()`
- `internal/events/adapter.go` - `FromAgentEvent()` conversion helper
- `internal/events/bus_test.go` - unit tests (this is the TEST file, write it FIRST)

### TDD Steps (Phase 2)

1. **Write `internal/events/bus_test.go` FIRST** - Define the tests that prove the EventBus works correctly. Include:
   - `TestBusPublishDeliversToSubscriber` - subscribe, publish, verify handler receives event
   - `TestBusMultipleSubscribers` - 2 handlers on same kind, both receive the event
   - `TestBusKindFiltering` - subscribe to KindA, publish KindB, handler NOT called
   - `TestBusUnsubscribe` - subscribe then unsubscribe, handler NOT called after
   - `TestBusCloseMultipleTimes` - `Close()` called twice, no panic (use `sync.Once`)
   - `TestBusSubscribeMany` - subscribe to 3 kinds with one call, all receive
   - `TestHandlerFuncAdapter` - `HandlerFunc` wrapper works
   - `TestEventConstruction` - `NewEvent()` sets `Kind` and non-zero `Timestamp`
   - `TestFromAgentEvent_AllKinds` - convert every `agent.EventKind` value, verify no data loss

2. **Run `go build ./internal/events/`** - tests reference types that don't exist yet. Create **minimal type stubs** in `event.go`, `handler.go`, `bus.go`, `adapter.go` so the test file compiles (RED - tests fail because stubs don't implement the behaviour).

3. **Implement the real types:**
   - `event.go` - `Kind` (type string), constants for all 16+ kinds, `Event` struct, `NewEvent()`, `NewEventFromAgent()`
   - `handler.go` - `Handler` interface, `HandlerFunc` adapter
   - `bus.go` - `Bus` with `sync.RWMutex`, `map[Kind][]Handler`, goroutine-safe `Publish`/`Subscribe`/`SubscribeMany`/`Close`
   - `adapter.go` - `FromAgentEvent()` converting `agent.Event` → `events.Event`

4. **Run tests → GREEN**

5. **Run with `-race`** - `go test -race ./internal/events/...` - must pass (no data races)

6. **Bug-Audit:**
   - Does `Bus.Publish()` acquire a read lock (not write lock)?
   - Does `Bus.Subscribe()` acquire a write lock?
   - Are handlers called with `context.Background()`?
   - Does `FromAgentEvent()` handle ALL 9 agent.EventKind values? (use a switch or map)
   - Is `Bus.Close()` safe to call multiple times? (use `sync.Once`)
   - Are there no import cycles? (`events` must NOT import `agent`, `cli`, or `chat`)
   - Does every test cover at least one edge case (nil, empty, concurrent)?
   - Is there at least one concurrent test (`go test -race`) that proves the Bus is goroutine-safe?

7. **Write `.mivia/phase2-complete.md`**

### DOD Gate (Phase 2 → Phase 3)
- [ ] TDD was followed: `bus_test.go` written first, then implementation
- [ ] `go build ./internal/events/` succeeds
- [ ] `go test -race ./internal/events/...` passes
- [ ] No import cycles (check with `go vet ./...`)
- [ ] `go test ./...` passes (no regressions in other packages)
- [ ] `Bus` has `Publish`, `Subscribe`, `SubscribeMany`, `Close`
- [ ] `Handler` interface has `HandleEvent(ctx, Event)`
- [ ] `FromAgentEvent()` converts all 9 agent.EventKind values
- [ ] Package does NOT import `agent`, `cli`, or `chat`
- [ ] At least 10 unit tests exist in `internal/events/`
- [ ] At least 1 concurrent test (`t.Parallel()` or goroutine publish) with `-race`
- [ ] All tests deterministic: no `time.Sleep`, no global state

---

## Phase 3: Wire Agent Loop → EventBus

**New/modified files:**
- New: `internal/cli/ui_adapter.go` - `UIAdapter` struct, `NewUIAdapter()`, `PollCmd()`
- New: `internal/cli/ui_adapter_test.go` - tests for UIAdapter (write FIRST)
- Modified: `internal/agent/loop.go` - add `EventBus` field to `Options`, publish in parallel
- Modified: `internal/agent/loop_test.go` - add test proving EventBus receives events
- Modified: `internal/cli/tui.go` - wire EventBus + UIAdapter into `runTUI()`, `startAI()` publishes turn events
- Modified: `internal/cli/subagent_progress.go` - publish to EventBus alongside global

### TDD Steps (Phase 3)

1. **Write tests FIRST** - before any implementation:
   - `internal/cli/ui_adapter_test.go`:
     - `TestUIAdapterPollCmdReturnsMsg` - PollCmd returns `uiEventMsg` or `uiTickMsg` (not nil)
     - `TestUIAdapterPollCmdSelfPerpetuates` - calling PollCmd multiple times always returns a non-nil tea.Cmd
     - `TestUIAdapterHandlesEvent` - publish on bus, poll cmd returns `uiEventMsg` with correct event
     - `TestUIAdapterDropsOnFullChannel` - fill channel to overflow, verify no block (backpressure test)
   - `internal/cli/tui_test.go` or extend existing journey test:
     - `TestStartAIPublishesTurnStart` - mock bus, call `startAI()`, verify `KindTurnStart` published
     - `TestAgentLoopPublishesToBus` - set `Options.EventBus`, run loop with a tool call, verify events arrive on bus
   - `internal/cli/subagent_progress_test.go`:
     - `TestEmitSubagentProgressPublishesToBus` - set global bus, call `emitSubagentProgress()`, verify event on bus

2. **Run tests → RED** - they fail (implementation doesn't exist yet)

3. **Implement:**
   - `internal/cli/ui_adapter.go`:
     - `UIAdapter` struct with `bus`, `evChan` (buffered 512), `pollDur`
     - `NewUIAdapter(bus, bridge)` subscribes to all agent/system kinds
     - `HandleEvent` sends to `evChan` (non-blocking, drop on full)
     - `PollCmd()` returns `tea.Cmd` that selects on `evChan` or `time.After(pollDur)`
   - `internal/agent/loop.go`:
     - Add `EventBus *events.Bus` to `Options`
     - After each event, call BOTH `opts.OnEvent(e)` AND `opts.EventBus.Publish(events.FromAgentEvent(e, ...))`
   - `internal/agent/loop_test.go`:
     - Test that both paths receive events (test with mock OnEvent + bus subscriber)
   - `internal/cli/tui.go`:
     - `runTUI()` creates EventBus and UIAdapter, wires into model
     - `startAI()` publishes `KindTurnStart`, worker goroutine publishes `KindTurnEnd`
   - `internal/cli/subagent_progress.go`:
     - Add `globalBus *events.Bus` package var, set from `runTUI()`
     - `emitSubagentProgress()` publishes to bus alongside existing global callback

4. **Run tests → GREEN**

5. **Run ALL tests + race** - `go test -race ./...`

6. **Bug-Audit:**
   - Does `agent.Loop.Run()` fire BOTH `OnEvent` AND `EventBus.Publish()`? (not one or the other)
   - Does `UIAdapter.PollCmd()` return a tea.Cmd that NEVER returns nil (always produces a message)?
   - Does `startAI()` publish `KindTurnStart` to the bus?
   - Does `emitSubagentProgress()` publish to the bus?
   - Is the old `streamBridge` still working for backward compat? (existing tests verify this)
   - Are there any double-processed events? (tuiTickMsg drains bridge, adapter drains channel - they're independent data paths)
   - Is the `bus.Close()` called on program exit? (in `runTUI()` defer)
   - Are ALL new functions/methods covered by the tests you wrote in step 1?
   - Is the `globalBus` variable safe (`sync.RWMutex` or set-once pattern)?

7. **Write `.mivia/phase3-complete.md`**

### DOD Gate (Phase 3 → Phase 4)
- [ ] TDD was followed: tests written FIRST, then implementation
- [ ] `go build ./cmd/mivia/` succeeds
- [ ] `go test -race ./...` passes
- [ ] `go vet ./...` is clean
- [ ] Agent loop publishes to EventBus in parallel with OnEvent callback (both fire)
- [ ] `startAI()` publishes `KindTurnStart` and `KindTurnEnd` (verify via test)
- [ ] `emitSubagentProgress()` publishes to EventBus
- [ ] UIAdapter exists with `NewUIAdapter()` and `PollCmd()`
- [ ] Legacy streamBridge still works (backward compat - existing CLI tests pass)
- [ ] At least 3 new test files/functions added for Phase 3
- [ ] All tests deterministic: no `time.Sleep`, no global state races in adapter tests
- [ ] No data races (`go test -race ./...` passes)

---

## Phase 4: OTEL Adapter (Stubbed)

**New file:**
- `internal/events/otel_adapter.go` - `OTELAdapter` struct, `NewOTELAdapter()`, `HandleEvent()` (no-op)
- `internal/events/otel_adapter_test.go` - verify stub behaviour

### TDD Steps (Phase 4)

1. **Write `internal/events/otel_adapter_test.go` FIRST:**
   - `TestOTELAdapterNewSubscribes` - create adapter with a bus, publish a tool event, adapter's HandleEvent is called (use a sentinel pattern or just verify bus doesn't panic)
   - `TestOTELAdapterHandleEventNoop` - call `HandleEvent` with each kind, verify no panic, no deadlock
   - `TestOTELAdapterCloseSafe` - call `Close()` twice, no panic

2. **Run tests → they should at least compile with stubs** - create minimal stub so tests compile (RED: tests fail because no OTEL adapter exists).

3. **Implement `internal/events/otel_adapter.go`:**
   ```go
   package events

   import "context"

   type OTELAdapter struct {
       bus *Bus
       // NOT IMPLEMENTED: tracer trace.Tracer
       // NOT IMPLEMENTED: spanMap sync.Map
   }

   func NewOTELAdapter(bus *Bus) *OTELAdapter {
       a := &OTELAdapter{bus: bus}
       bus.SubscribeMany([]Kind{
           KindToolStart, KindToolEnd,
           KindSubagentStart, KindSubagentEnd,
           KindTurnStart, KindTurnEnd,
           KindError,
       }, a)
       return a
   }

   func (a *OTELAdapter) HandleEvent(ctx context.Context, ev Event) {
       // NOT IMPLEMENTED - no-op until OTEL dependencies are added.
       // When implementing, switch on ev.Kind and create/end spans.
       _ = ctx
       _ = ev
   }

   func (a *OTELAdapter) Close() error {
       // NOT IMPLEMENTED - flush pending spans.
       return nil
   }
   ```

4. **Run tests → GREEN**

5. **Bug-Audit:**
   - Does `NewOTELAdapter()` subscribe to the correct kinds? (matches plan §11.1)
   - Are all real OTEL types commented out with `// NOT IMPLEMENTED`? (no import of otel package)
   - Does `HandleEvent` compile as a no-op with `_ = ctx; _ = ev`?
   - Is `Close()` safe to call multiple times? (returns nil each time)
   - Does `go build ./cmd/mivia/` succeed WITHOUT any OTEL dependency in go.mod?

6. **Write `.mivia/phase4-complete.md`**

### DOD Gate (Phase 4 → Final)
- [ ] TDD was followed: test written FIRST, then implementation
- [ ] `go build ./cmd/mivia/` succeeds (no OTEL dependency)
- [ ] `go test -race ./internal/events/...` passes
- [ ] `go test -race ./...` passes (full suite, zero regressions)
- [ ] `go vet ./...` is clean
- [ ] OTELAdapter compiles as no-op with zero OTEL imports
- [ ] All 4 phase-complete files exist

---

## Validation: Full Integration Smoke Test

After all phases are complete, verify the end-to-end behaviour:

1. **Build:** `go build -o mivia ./cmd/mivia/` - must succeed
2. **Run:** `./mivia --model <model> --provider <provider>` - must start TUI
3. **Smoke test:**
   - Type a message, press Enter
   - **Observe:** status bar updates elapsed time, phase label, tool counts every ~80ms without any key/mouse input
   - **Observe:** streaming tokens appear in viewport continuously
   - **Observe:** tool calls appear in viewport (tool panel rows) as they start/end
   - **Observe:** thinking content appears (if provider supports it)
   - **Observe:** after agent finishes, tool rows and thinking are committed as ChatBlocks in history
   - Type another message while agent is busy → queued messages work
   - Ctrl+C cancels in-flight agent
4. **Edge cases:**
   - Welcome screen: logo animates, pollCmd does not cause errors or floods
   - Session list: mouse/keyboard navigation works
   - Resize terminal: layout adapts
   - `/clear`, `/model`, `/help` slash commands work
5. If any issue found, file a bug-audit ticket and fix before declaring done.

---

## Rules
- **TDD is not optional.** Every code change must follow RED → GREEN → REFACTOR. If you didn't write the test first, you didn't do TDD - undo and redo.
- Read each file before editing it. Do not rely on memory.
- After each change, run `go build ./cmd/mivia/` AND `go test ./...` AND `go vet ./...`.
- If you hit a compilation error, fix it immediately.
- If you hit a test failure, investigate and fix before continuing.
- Do not skip phases. Do not merge phases.
- Write a brief `phase-N-complete.md` after each phase documenting what changed, what tests were added, and any deviations.
- The final DOD validation requires the manual smoke test to pass.
- **Never leave technical debt:** if you notice a gap or improvement outside the phase scope, file it as a separate ticket - do not derail the phase.
</task>
---

## 14. Appendix: Code Diff Sketches

### Phase 1 - `tui.go` Init + pollCmd

**`internal/cli/tui.go`:**

```diff
 func (m *tuiModel) Init() tea.Cmd {
-    return tea.Batch(m.spinner.Tick, tea.EnterAltScreen, logoTickCmd())
+    return tea.Batch(m.spinner.Tick, tea.EnterAltScreen, logoTickCmd(), m.pollCmd())
 }

+// pollCmd returns a one-shot tea.Cmd that fires on bridge notify or 80ms timeout.
+// It is always re-scheduled by the tuiTickMsg handler (see tui_message.go).
 func (m *tuiModel) pollCmd() tea.Cmd {
     return func() tea.Msg {
         m.mu.Lock()
```

### Phase 1 - `tui_message.go` add `case tuiTickMsg:`

**`internal/cli/tui_message.go`:**

```diff
 func updateMessageImpl(m *tuiModel, msg tea.Msg) (tea.Model, tea.Cmd) {
     var cmds []tea.Cmd
     skipTextarea := false
     switch msg := msg.(type) {
+    case tuiTickMsg:
+        if m.mode == modeChat {
+            m.mu.Lock()
+            stream, tools, done, doneErr, thinking, stepDetail, stepDetailAt := m.bridge.Drain()
+            m.mu.Unlock()
+            m.updateFromDrain(stream, tools, done, doneErr, thinking, stepDetail, stepDetailAt)
+        }
+        cmds = append(cmds, m.pollCmd())
+        return m, tea.Batch(cmds...)
     case tea.WindowSizeMsg:
```

**Remove conditional poll re-queues from existing drain block:**

```diff
-    if len(tools) > 0 {
-        m.applyToolEvents(tools)
-        if m.waiting && !m.stalledWarning {
-            m.layout()
-            m.renderStreamVP()
-        }
-        cmds = append(cmds, m.pollCmd())  // <-- REMOVE
-    }
-    if stream != "" || done || doneErr != nil {
-        ...
-        cmds = append(cmds, m.pollCmd())   // <-- REMOVE
-    }
+    // pollCmd is always re-queued by tuiTickMsg handler above.
+    // This block only applies state changes; re-queuing is centralized.
```

### Phase 1 - New helper `updateFromDrain`

**`internal/cli/tui_layout.go`** (or `tui_update.go`):

```go
func (m *tuiModel) updateFromDrain(stream string, tools []bridgeToolEvt, done bool, doneErr error, thinking string, stepDetail string, stepDetailAt time.Time) {
    m.stepDetail = stepDetail
    if !stepDetailAt.IsZero() {
        m.stepDetailAt = stepDetailAt
    }
    if len(tools) > 0 {
        m.applyToolEvents(tools)
        if m.waiting && !m.stalledWarning {
            m.layout()
            m.renderStreamVP()
        }
    }
    if stream != "" || done || doneErr != nil {
        if stream != "" {
            m.streamBuf.WriteString(stream)
        }
        if done || doneErr != nil {
            // finishStream handles the rest (called separately)
        }
        if !done {
            m.renderStreamVP()
        }
    }
    if thinking != "" {
        m.thinkingBuf.WriteString(thinking)
        if !done {
            m.renderStreamVP()
        }
    }
    // Check stalled: no new data for 5+ seconds while waiting
    if m.waiting && stream == "" && len(tools) == 0 && thinking == "" && !done {
        elapsed := time.Since(m.turnStart)
        if elapsed > 5*time.Second && !m.stalledWarning {
            m.stalledWarning = true
        }
    }
}
```

### Phase 2 - `internal/events/` package skeleton

```go
// internal/events/bus.go
package events

import (
    "context"
    "sync"
)

type Bus struct {
    mu   sync.RWMutex
    subs map[Kind][]Handler
}

func New() *Bus { return &Bus{subs: make(map[Kind][]Handler)} }

func (b *Bus) Publish(ev Event) {
    b.mu.RLock()
    handlers := b.subs[ev.Kind]
    b.mu.RUnlock()
    for _, h := range handlers {
        h.HandleEvent(context.Background(), ev)
    }
}

func (b *Bus) Subscribe(kind Kind, h Handler) {
    b.mu.Lock()
    defer b.mu.Unlock()
    b.subs[kind] = append(b.subs[kind], h)
}
```

### Phase 3 - `runTUI` with EventBus

```diff
 func runTUI(sess *chat.Session, res *config.Resolved, toolsOn bool) error {
     ...
     model := newTUIModel(sess, res, toolsOn)
+    bus := events.New()
+    model.eventBus = bus
+    model.uiAdapter = NewUIAdapter(bus, model.bridge)
     p := tea.NewProgram(model, tea.WithAltScreen())
     _, err := p.Run()
     ...
+    bus.Close()
 }
```

### Phase 3 - `startAI` publishes turn events

```diff
 func (m *tuiModel) startAI(userText string) {
     ...
     m.workerWG.Add(1)
     SetSubagentProgress(agentEventBridgeCallback(bridge))
+    if m.eventBus != nil {
+        m.eventBus.Publish(events.Event{
+            Kind:      events.KindTurnStart,
+            Timestamp: time.Now(),
+            TurnID:    fmt.Sprintf("turn:%d", m.session.UserTurns()+1),
+            Detail:    userText,
+        })
+    }
     go func() {
         defer m.workerWG.Done()
         defer SetSubagentProgress(nil)
         _, err := m.session.SendUserWithEvent(ctx, userText, bridge, agentEventBridgeCallback(bridge))
         ...
+        if m.eventBus != nil {
+            m.eventBus.Publish(events.Event{
+                Kind:      events.KindTurnEnd,
+                Timestamp: time.Now(),
+                TurnID:    fmt.Sprintf("turn:%d", m.session.UserTurns()),
+            })
+        }
     }()
 }
```

---

## Summary of Changes by File

| File | Phase | Change |
|---|---|---|
| `internal/cli/tui.go` | 1 | Add `pollCmd()` to `Init()` |
| `internal/cli/tui_message.go` | 1 | Add `case tuiTickMsg:` handler, always re-queue pollCmd |
| `internal/cli/tui_layout.go` | 1 | Add `updateFromDrain()` helper |
| `internal/events/` (new) | 2 | Event bus, event types, handler interface |
| `internal/cli/ui_adapter.go` (new) | 3 | UIAdapter: EventBus → Bubble Tea bridge |
| `internal/cli/tui.go` | 3 | Wire EventBus + UIAdapter into runTUI |
| `internal/agent/loop.go` | 3 | Publish to EventBus in parallel with OnEvent |
| `internal/events/adapter.go` | 3 | Convert agent.Event → events.Event |
| `internal/events/otel_adapter.go` (new) | 4 | OTEL tracing adapter (optional) |
