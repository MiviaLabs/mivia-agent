package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

type EventKind string

const (
	EventAssistant         EventKind = "assistant"
	EventToolStart         EventKind = "tool_start"
	EventToolEnd           EventKind = "tool_end"
	EventStep              EventKind = "step"
	EventPrune             EventKind = "prune"
	EventToolParallel      EventKind = "tool_parallel"
	EventSubagentStart     EventKind = "subagent_start"
	EventSubagentEnd       EventKind = "subagent_end"
	EventSubagentHeartbeat EventKind = "subagent_heartbeat"
)

type Event struct {
	Kind       EventKind
	ToolCallID string // stable correlation key for tool lifecycle events
	Name       string
	Detail     string
	Content    string
	Input      string // bounded, redacted tool input preview
	Output     string // bounded, redacted tool output preview
}

type Options struct {
	Model       string
	Temperature *float64
	MaxTokens   *int
	MaxSteps    int
	// MaxContextTokens sets the approximate token limit for the prompt context.
	// When exceeded, old messages are pruned (keeping system prompt and recent turns).
	// 0 or negative means no pruning.
	MaxContextTokens int
	// MaxToolResultChars caps each tool result stored in conversation history.
	// This prevents a single large output (e.g. read_file of 256KB) from
	// exceeding the context budget. 0 means no cap (use full result).
	MaxToolResultChars      int
	MaxToolCallsPerBatch    int
	MaxToolBatchResultChars int
	MaxConcurrentTools      int
	ToolTimeout             time.Duration
	RequestTimeout          time.Duration
	ParentID                string
	TurnID                  string
	SessionID               string
	Role                    string
	Depth                   int
	Budget                  int
	Dispatcher              *runtime.Dispatcher
	OnEvent                 func(Event)
	EventBus                *events.Bus // publishes agent events to extensible delivery
	FinalWriter             io.Writer
}

type Loop struct {
	Completer provider.Completer
	Tools     *tools.Registry
	Messages  []provider.Message
}

type toolExecResult struct {
	index     int // original position in ToolCalls slice
	toolCall  provider.ToolCall
	result    string
	truncated bool // whether result was truncated for history
	err       error
}

type toolTask struct {
	call       provider.ToolCall
	raw        json.RawMessage
	capability tools.Capability
	timeout    time.Duration
	callCtx    context.Context
	cancel     context.CancelFunc
}

type toolScheduler struct {
	limit chan struct{}
	mu    sync.Mutex
	locks map[string]*keyLock
}

// keyLock wraps a per-key mutex channel with a reference count so the
// scheduler can clean up entries that are no longer in use, preventing
// unbounded map growth over long sessions.
type keyLock struct {
	ch   chan struct{}
	refs int32
}

func newToolScheduler(limit int) *toolScheduler {
	if limit <= 0 {
		limit = 4
	}
	return &toolScheduler{limit: make(chan struct{}, limit), locks: make(map[string]*keyLock)}
}
func (s *toolScheduler) acquire(ctx context.Context, key string) (func(), error) {
	select {
	case s.limit <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if key == "" {
		return func() { <-s.limit }, nil
	}
	s.mu.Lock()
	kl := s.locks[key]
	if kl == nil {
		kl = &keyLock{ch: make(chan struct{}, 1)}
		s.locks[key] = kl
	}
	kl.refs++
	s.mu.Unlock()
	select {
	case kl.ch <- struct{}{}:
		return func() {
			<-kl.ch
			s.mu.Lock()
			kl.refs--
			// Only clean up when this goroutine was the last reference
			// AND no one is waiting on the channel.
			if kl.refs <= 0 && len(kl.ch) == 0 {
				delete(s.locks, key)
			}
			s.mu.Unlock()
			<-s.limit
		}, nil
	case <-ctx.Done():
		s.mu.Lock()
		kl.refs--
		s.mu.Unlock()
		<-s.limit
		return nil, ctx.Err()
	}
}
func (l *Loop) Run(ctx context.Context, userText string, opts Options) (string, error) {
	if l.Completer == nil {
		return "", fmt.Errorf("nil completer")
	}
	if l.Tools == nil {
		return "", fmt.Errorf("nil tools")
	}
	if opts.SessionID == "" {
		opts.SessionID = runtime.NewSessionID()
	}
	l.Messages = append(l.Messages, provider.Message{
		Role:      provider.RoleUser,
		Content:   userText,
		CreatedAt: time.Now(),
	})
	toolSpecs := l.Tools.OpenAITools()
	var lastText string
	for step := 1; ; step++ {
		if opts.MaxSteps > 0 && step > opts.MaxSteps {
			return lastText, fmt.Errorf("agent exceeded max_steps (%d)", opts.MaxSteps)
		}
		l.emitStep(opts, step)

		text, done, err := l.runStep(ctx, toolSpecs, opts)
		if err != nil {
			return lastText, err
		}
		if done {
			return text, nil
		}
		if text != "" {
			lastText = text
		}
	}
}
func (l *Loop) emitStep(opts Options, step int) {
	d := fmt.Sprintf("%d/∞", step)
	if opts.MaxSteps > 0 {
		d = fmt.Sprintf("%d/%d", step, opts.MaxSteps)
	}
	emit(opts, Event{Kind: EventStep, Detail: d})
}
func (l *Loop) runStep(ctx context.Context, toolSpecs []provider.ToolSpec, opts Options) (text string, done bool, err error) {
	beforeTokens := provider.MessagesTokens(l.Messages)
	l.Messages = provider.PruneMessagesKeepTurns(l.Messages, opts.MaxContextTokens)
	afterTokens := provider.MessagesTokens(l.Messages)
	if afterTokens < beforeTokens {
		emit(opts, Event{
			Kind:   EventPrune,
			Detail: fmt.Sprintf("pruned ~%d tokens (before=%d after=%d budget=%d)", beforeTokens-afterTokens, beforeTokens, afterTokens, opts.MaxContextTokens),
		})
	}

	// Stream when a FinalWriter is attached so TUI can show tokens live.
	// Content deltas go to FinalWriter; tool_calls are still assembled fully.
	stream := opts.FinalWriter != nil
	req := provider.Request{
		Model:        opts.Model,
		Messages:     l.Messages,
		Temperature:  opts.Temperature,
		MaxTokens:    opts.MaxTokens,
		Tools:        toolSpecs,
		ToolChoice:   "auto",
		Stream:       stream,
		StreamWriter: opts.FinalWriter,
		Timeout:      opts.RequestTimeout,
	}

	// Emit periodic "still thinking" heartbeat during the model call.
	heartbeat, heartbeatCancel := context.WithCancel(ctx)
	defer heartbeatCancel()
	go emitModelThinkingHeartbeat(heartbeat, opts)
	resp, err := l.Completer.ChatTurn(heartbeat, req)
	if err != nil {
		return "", false, err
	}

	if len(resp.ToolCalls) == 0 {
		l.Messages = append(l.Messages, provider.Message{
			Role:      provider.RoleAssistant,
			Content:   resp.Content,
			CreatedAt: time.Now(),
		})
		// When streaming, FinalWriter already received deltas — do not rewrite.
		if !stream && opts.FinalWriter != nil && resp.Content != "" {
			_, _ = io.WriteString(opts.FinalWriter, resp.Content)
		}
		if resp.Content != "" {
			emit(opts, Event{Kind: EventAssistant, Content: resp.Content})
		}
		return resp.Content, true, nil
	}

	// Content-then-tools: clear optimistic final-stream tokens, then re-emit
	// speech as an intermediate assistant bubble (Detail=interim).
	if stream {
		revokeStreamWriter(opts.FinalWriter)
	}

	l.Messages = append(l.Messages, provider.Message{
		Role:      provider.RoleAssistant,
		Content:   resp.Content,
		ToolCalls: resp.ToolCalls,
		CreatedAt: time.Now(),
	})
	if resp.Content != "" {
		// Detail "interim" marks user-visible speech before tools (multi-bubble).
		emit(opts, Event{Kind: EventAssistant, Content: resp.Content, Detail: "interim"})
	}
	l.runToolBatch(ctx, resp.ToolCalls, opts)
	return resp.Content, false, nil
}

// streamRevoker is implemented by the TUI streamBridge to clear optimistic
// content that was streamed before tool_calls arrived.
type streamRevoker interface {
	RevokeStream() string
}

func revokeStreamWriter(w io.Writer) {
	if w == nil {
		return
	}
	if r, ok := w.(streamRevoker); ok {
		_ = r.RevokeStream()
	}
}

func emitModelThinkingHeartbeat(ctx context.Context, opts Options) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	started := time.Now()
	for {
		select {
		case <-ticker.C:
			elapsed := time.Since(started)
			emit(opts, Event{
				Kind:   EventStep,
				Detail: fmt.Sprintf("model thinking (%d s)", int(elapsed.Seconds())),
			})
		case <-ctx.Done():
			return
		}
	}
}
