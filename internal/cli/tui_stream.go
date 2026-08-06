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
	Agent      string // producing subagent name ("" = the session's own tools)
	At         time.Time
}

// bridgeDrain is a one-shot snapshot of bridge UI state for the TUI update loop.
type bridgeDrain struct {
	Stream       string
	Tools        []bridgeToolEvt
	Done         bool
	DoneErr      error
	Thinking     string
	StepDetail   string
	StepDetailAt time.Time
	ResetStream  bool
	// Interim is user-visible assistant speech before/between tool batches
	// ("I'll search…"). Committed as ChatBlockAssistant, not thinking chrome.
	Interim string
}

// streamBridge - agent goroutine → UI (coalesced, no goroutine storms)
type streamBridge struct {
	mu      sync.Mutex
	pending strings.Builder
	tools   []bridgeToolEvt
	done    bool
	doneErr error
	notify  chan struct{}
	closed  bool
	turnID  uint64 // non-zero when a turn fence is active
	// Thinking buffer: dim reasoning chrome (optional).
	thinking strings.Builder
	// Interim buffer: user-visible multi-bubble assistant speech between tools.
	interim strings.Builder
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

// RevokeStream clears optimistic final-stream text when tool_calls arrive.
// Returns the revoked text for callers; does not treat it as thinking - the
// agent re-emits EventAssistant so the TUI commits a durable speech bubble.
func (b *streamBridge) RevokeStream() string {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ""
	}
	revoked := b.pending.String()
	b.pending.Reset()
	b.resetStream = true
	b.mu.Unlock()
	b.signal()
	return revoked
}

// PushInterim queues user-visible assistant speech for the next Drain
// (intermediate bubbles: "I'll look that up…", "Next I'll…").
// Ghost/noise text is dropped here so the bus path cannot force empty bubbles.
func (b *streamBridge) PushInterim(text string) {
	if !shouldCommitInterim(text) {
		return
	}
	b.mu.Lock()
	if b.closed || (b.done && b.turnID > 0) {
		b.mu.Unlock()
		return
	}
	const maxInterim = 64 * 1024
	if b.interim.Len()+len(text) > maxInterim {
		cur := b.interim.String()
		keep := maxInterim / 2
		if len(cur) > keep {
			b.interim.Reset()
			b.interim.WriteString(cur[len(cur)-keep:])
		}
	}
	b.interim.WriteString(text)
	b.mu.Unlock()
	b.signal()
}

// PushThinking appends model reasoning text (dim chrome, not speech bubbles).
//
// Reasoning is accepted at any point in a turn. It used to be dropped unless
// a tool was already running (activeTools > 0), which discarded exactly the
// case that matters most: the chain of thought a model streams BEFORE it
// decides to call anything.
func (b *streamBridge) PushThinking(text string) {
	if text == "" {
		return
	}
	b.mu.Lock()
	if b.closed || (b.done && b.turnID > 0) {
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

// PushCompletedBanner records a one-shot visibility row (parallel/prune) that
// is immediately completed. Never leaves an open active-tool slot.
func (b *streamBridge) PushCompletedBanner(name, detail string) {
	b.PushToolWithID(true, "", name, detail)
	b.PushToolWithID(false, "", name, "completed")
}

func (b *streamBridge) PushToolWithID(start bool, toolCallID, name, detail string) {
	b.pushToolEvt(start, toolCallID, "", name, detail)
}

// PushSubagentTool records a nested tool event attributed to a subagent, so
// the UI can badge the row with the agent that ran it.
func (b *streamBridge) PushSubagentTool(start bool, toolCallID, agentName, name, detail string) {
	b.pushToolEvt(start, toolCallID, agentName, name, detail)
}

func (b *streamBridge) pushToolEvt(start bool, toolCallID, agentName, name, detail string) {
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
			Start: start, ToolCallID: toolCallID, Name: name, Detail: detail,
			Agent: agentName, At: time.Now(),
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

// Pending reports whether the bridge still holds undrained UI content
// (stream text, tool events, thinking, interim speech, step detail, an
// unconsumed RevokeStream directive, or a Finish that Drain has not seen).
// Unlike Drain, it is non-consuming: the state is left intact so the next
// pollCmd tick can still deliver it and finish via Done. An empty or nil
// bridge counts as drained.
func (b *streamBridge) Pending() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pending.Len() > 0 ||
		len(b.tools) > 0 ||
		b.thinking.Len() > 0 ||
		b.interim.Len() > 0 ||
		b.stepDetail != "" ||
		b.resetStream ||
		b.done
}

// Drain returns and clears pending UI state.
func (b *streamBridge) Drain() bridgeDrain {
	b.mu.Lock()
	defer b.mu.Unlock()
	d := bridgeDrain{
		Stream:       b.pending.String(),
		Tools:        b.tools,
		Done:         b.done,
		DoneErr:      b.doneErr,
		Thinking:     b.thinking.String(),
		StepDetail:   b.stepDetail,
		StepDetailAt: b.stepDetailAt,
		ResetStream:  b.resetStream,
		Interim:      b.interim.String(),
	}
	b.pending.Reset()
	b.tools = nil
	b.thinking.Reset()
	b.interim.Reset()
	b.stepDetail = ""
	b.resetStream = false
	if d.Done {
		b.done = false
		b.doneErr = nil
		b.turnID = 0
	}
	return d
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
