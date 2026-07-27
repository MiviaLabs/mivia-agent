package cli

import (
	"strings"
	"sync"
	"time"
)

type bridgeToolEvt struct {
	Start      bool
	ToolCallID string
	Name       string
	Detail     string
	At         time.Time
}

// streamBridge — agent goroutine → UI (coalesced, no goroutine storms)
type streamBridge struct {
	mu      sync.Mutex
	pending strings.Builder
	tools   []bridgeToolEvt
	done    bool
	doneErr error
	notify  chan struct{}
	closed  bool
	turnID  uint64 // non-zero when a turn fence is active
	// Thinking buffer: model reasoning text between tool calls.
	thinking strings.Builder
	// openToolIDs tracks open tool call IDs so queued→running restarts do not
	// double-count activeTools (each real tool is one open slot until End).
	openToolIDs  map[string]struct{}
	activeTools  int // outstanding tools for thinking dedup
	stepDetail   string
	stepDetailAt time.Time
	// anonOpen counts Start/End pairs without ToolCallID (legacy/parallel banner).
	anonOpen int
	// resetStream is set by RevokeStream; next Drain reports it so the TUI
	// clears streamBuf (content already drained before tool_calls arrived).
	resetStream bool
}

func newStreamBridge() *streamBridge {
	return &streamBridge{
		notify:      make(chan struct{}, 1),
		openToolIDs: make(map[string]struct{}),
	}
}

func (b *streamBridge) signal() {
	select {
	case b.notify <- struct{}{}:
	default:
	}
}

func (b *streamBridge) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	b.mu.Lock()
	if b.closed || (b.done && b.turnID > 0) {
		b.mu.Unlock()
		return len(p), nil
	}
	const maxPending = 512 * 1024
	if b.pending.Len()+len(p) > maxPending {
		cur := b.pending.String()
		keep := maxPending / 2
		if len(cur) > keep {
			b.pending.Reset()
			b.pending.WriteString(cur[len(cur)-keep:])
		}
	}
	b.pending.Write(p)
	b.mu.Unlock()
	b.signal()
	return len(p), nil
}

// RevokeStream clears optimistic assistant text that was streamed before
// tool_calls arrived. Returns the revoked text. Moves non-empty content into
// the thinking buffer for optional display, and flags the next Drain to clear
// any already-applied streamBuf on the TUI side.
func (b *streamBridge) RevokeStream() string {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ""
	}
	revoked := b.pending.String()
	b.pending.Reset()
	if revoked != "" {
		// Preserve intermediate preamble as thinking chrome if tools will run.
		const maxThinking = 64 * 1024
		if b.thinking.Len()+len(revoked) > maxThinking {
			cur := b.thinking.String()
			keep := maxThinking / 2
			if len(cur) > keep {
				b.thinking.Reset()
				b.thinking.WriteString(cur[len(cur)-keep:])
			}
		}
		b.thinking.WriteString(revoked)
		if !strings.HasSuffix(revoked, "\n") {
			b.thinking.WriteByte('\n')
		}
	}
	b.resetStream = true
	b.mu.Unlock()
	b.signal()
	return revoked
}

// PushThinking appends model reasoning text (EventAssistant content).
func (b *streamBridge) PushThinking(text string) {
	if text == "" {
		return
	}
	b.mu.Lock()
	if b.closed || (b.done && b.turnID > 0) || b.activeTools == 0 {
		b.mu.Unlock()
		return
	}
	const maxThinking = 64 * 1024
	if b.thinking.Len()+len(text) > maxThinking {
		cur := b.thinking.String()
		keep := maxThinking / 2
		if len(cur) > keep {
			b.thinking.Reset()
			b.thinking.WriteString(cur[len(cur)-keep:])
		}
	}
	b.thinking.WriteString(text)
	b.thinking.WriteByte('\n')
	b.mu.Unlock()
	b.signal()
}

func (b *streamBridge) PushTool(start bool, name, detail string) {
	b.PushToolWithID(start, "", name, detail)
}

func (b *streamBridge) PushToolWithID(start bool, toolCallID, name, detail string) {
	b.mu.Lock()
	if b.closed || (b.done && b.turnID > 0) {
		b.mu.Unlock()
		return
	}
	if start {
		if toolCallID != "" {
			// Lifecycle restart (queued → running) for an already-open ID: no ++.
			if _, open := b.openToolIDs[toolCallID]; !open {
				b.openToolIDs[toolCallID] = struct{}{}
				b.activeTools++
			}
		} else {
			b.anonOpen++
			b.activeTools++
		}
	} else {
		if toolCallID != "" {
			if _, open := b.openToolIDs[toolCallID]; open {
				delete(b.openToolIDs, toolCallID)
				if b.activeTools > 0 {
					b.activeTools--
				}
			}
		} else if b.anonOpen > 0 {
			b.anonOpen--
			if b.activeTools > 0 {
				b.activeTools--
			}
		} else if b.activeTools > 0 {
			b.activeTools--
		}
	}
	if len(b.tools) < 500 {
		b.tools = append(b.tools, bridgeToolEvt{
			Start: start, ToolCallID: toolCallID, Name: name, Detail: detail, At: time.Now(),
		})
	}
	b.mu.Unlock()
	b.signal()
}

func (b *streamBridge) Finish(err error) {
	b.mu.Lock()
	b.done = true
	b.doneErr = err
	b.mu.Unlock()
	b.signal()
}

// PushStep stores a heartbeat/step event detail for UI display.
func (b *streamBridge) PushStep(detail string) {
	b.mu.Lock()
	b.stepDetail = detail
	b.stepDetailAt = time.Now()
	b.mu.Unlock()
	b.signal()
}

func (b *streamBridge) Drain() (stream string, tools []bridgeToolEvt, done bool, doneErr error, thinking string, stepDetail string, stepDetailAt time.Time, resetStream bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	stream = b.pending.String()
	b.pending.Reset()
	tools = b.tools
	b.tools = nil
	done = b.done
	doneErr = b.doneErr
	if done {
		b.done = false
		b.doneErr = nil
		b.turnID = 0
	}
	thinking = b.thinking.String()
	b.thinking.Reset()
	stepDetail = b.stepDetail
	b.stepDetail = ""
	stepDetailAt = b.stepDetailAt
	resetStream = b.resetStream
	b.resetStream = false
	return
}

// SetTurnID sets the current turn fence ID without changing the done flag.
func (b *streamBridge) SetTurnID(id uint64) {
	b.mu.Lock()
	b.turnID = id
	b.mu.Unlock()
}

// FenceTurn marks the bridge as accepting events only for the given turn.
// It clears the done flag so new events can flow for this turn.
func (b *streamBridge) FenceTurn(id uint64) {
	b.mu.Lock()
	b.turnID = id
	b.done = false
	b.doneErr = nil
	b.mu.Unlock()
	b.signal()
}

func (b *streamBridge) Close() {
	b.mu.Lock()
	b.closed = true
	b.activeTools = 0
	b.anonOpen = 0
	b.openToolIDs = make(map[string]struct{})
	b.mu.Unlock()
}

// ActiveTools returns outstanding tool count (for tests).
func (b *streamBridge) ActiveTools() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.activeTools
}
