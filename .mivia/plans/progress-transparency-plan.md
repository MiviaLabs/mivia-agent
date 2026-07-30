# System-Level Progress Transparency Plan

## Problem

When the agent goes silent between tool call batches (e.g., waiting for the
LLM), the TUI shows nothing changing. The user perceives the agent as
"stopped". Currently, progress signals are:

- **Agent loop heartbeats**: 10s interval for model thinking, 2s for tool
  batches — too sparse.
- **`stepDetail`**: Only set by explicit `EventStep`; silent gaps between
  steps.
- **No auto-generated status**: The model must choose to say "I'm reading file
  X" — there is no system-level fallback.

## Design Constraints

1. **No overengineering**: Use existing `stepDetail`/`stepDetailAt`/`stalledWarning`
   infrastructure.
2. **System-level, not model-level**: The host injects status automatically,
   does not wait for the model to speak.
3. **Minimal surface area**: Small changes in 2-3 files, no new services, no
   new goroutines.

---

## Change A — Auto-inject "working" heartbeat at 2s instead of 10s

**File:** `internal/agent/loop.go` — function `emitModelThinkingHeartbeat`

**Current:**
```go
ticker := time.NewTicker(10 * time.Second)
```

**Change to:**
```go
ticker := time.NewTicker(2 * time.Second)
```

**Rationale:** 10 seconds is an eternity in a terminal UI. 2 seconds matches
the tool batch heartbeat interval and means the user never waits more than 2s
without seeing *something* change in the status bar.

**Risk:** None. The heartbeat just emits `EventStep` with a duration string.
More frequent emission is harmless.

---

## Change B — Show `stepDetail` in the status bar as a live progress field

**File:** `internal/cli/chatblock_status.go` — function `renderStatusBar`

**Current status bar layout:**
```
[logo glyph] [model name] [phase indicator] [timer] [tool counts] [pending queue count] [message count]
```

**Add:** A `stepDetail` field right after the timer, but only when
`waiting == true` and `stepDetail != ""`. Render it in `tuiDimStyle` so it
does not compete with the main status.

**Desired appearance:**
```
◉ claude-sonnet  →  thinking · 12s  step 3/5  tools 2/4 done
```

The existing `stepDetail` string already contains the text we need
(e.g. `"tools 2/4 done · 12s"`, `"step 3/5"`, or `"thinking · 15s"` from
the heartbeat). It just needs to be displayed.

**Lines of change:** ~5 lines in `renderStatusBar` — check
`waiting && stepDetail != ""`, append to the parts slice.

---

## Change C — Decrease the TUI "stalled" threshold and show it faster

**File:** `internal/cli/tui_layout.go` — function `updateFromDrain`

**Current stalled check:**
```go
const stallQuiet = 15 * time.Second
```

**Change to:**
```go
const stallQuiet = 8 * time.Second
```

And add a fallback message when stalled warning activates:

```go
if m.stalledWarning && m.stepDetail == "" {
    m.stepDetail = fmt.Sprintf("working · %s", formatDuration(time.Since(m.turnStart)))
}
```

**Rationale:** 15s is too long to sit with a blank status. 8s is enough for
the user to feel the system is *aware* of the delay. The fallback message
"working · 34s" at least tells the user the system has not crashed.

---

## Total Diff

| File | Change | Lines |
|------|--------|-------|
| `internal/agent/loop.go` | 10s → 2s heartbeat ticker | 1 |
| `internal/cli/chatblock_status.go` | Show `stepDetail` in status bar when waiting | ~5 |
| `internal/cli/tui_layout.go` | 15s → 8s stall threshold + fallback message | ~5 |
| **Total** | | **~11 lines** |

---

## What This Achieves

1. **Every 2 seconds**: The status bar updates with elapsed thinking time
   (heartbeat).
2. **During tools**: "tools 2/4 done · 5s" appears live in the header
   (already works, just needs display).
3. **During silent gaps**: "working · 34s" appears after 8s of no activity.
4. **Zero model cooperation required**: All signals are host-generated.
5. **Backward compatible**: Existing `stepDetail` field reused, no new fields,
   no new goroutines.
