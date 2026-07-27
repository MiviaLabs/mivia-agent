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
	// Thinking buffer: model reasoning text between tool calls.
	thinking     strings.Builder
	activeTools  int    // tracks outstanding tool calls for thinking dedup
	stepDetail   string // latest heartbeat event detail
	stepDetailAt time.Time
}

func newStreamBridge() *streamBridge {
	return &streamBridge{notify: make(chan struct{}, 1)}
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
	if b.closed {
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

// PushThinking appends model reasoning text (EventAssistant content).
func (b *streamBridge) PushThinking(text string) {
	if text == "" {
		return
	}
	b.mu.Lock()
	if b.closed || b.activeTools == 0 {
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
	if b.closed {
		b.mu.Unlock()
		return
	}
	if start {
		b.activeTools++
	} else if b.activeTools > 0 {
		b.activeTools--
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

func (b *streamBridge) Drain() (stream string, tools []bridgeToolEvt, done bool, doneErr error, thinking string, stepDetail string, stepDetailAt time.Time) {
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
	}
	thinking = b.thinking.String()
	b.thinking.Reset()
	stepDetail = b.stepDetail
	b.stepDetail = ""
	stepDetailAt = b.stepDetailAt
	return
}

func (b *streamBridge) Close() {
	b.mu.Lock()
	b.closed = true
	b.activeTools = 0
	b.mu.Unlock()
}
