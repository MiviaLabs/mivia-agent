package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// ErrPromptBudgetExceeded means local history preparation could not fit the
// current request into the selected model's prompt budget.
var ErrPromptBudgetExceeded = errors.New("prompt exceeds model budget")

type Options struct {
	Model       string
	Temperature *float64
	MaxTokens   *int
	MaxSteps    int
	// MaxContextTokens sets the approximate token limit for the prompt context.
	// When exceeded, old messages are pruned (keeping system prompt and recent turns).
	// 0 or negative means no pruning.
	MaxContextTokens int
	// MaxToolResultChars caps each tool result stored in conversation history,
	// in BYTES despite the name (it bounds len() of the UTF-8 body; see
	// capToolResult). This prevents a single large output (e.g. read_file of
	// 256KB) from exceeding the context budget. 0 means no cap (use full
	// result); per-tool Capability.MaxResultBytes budgets still apply.
	MaxToolResultChars   int
	MaxToolCallsPerBatch int
	MaxConcurrentTools   int
	ToolTimeout          time.Duration
	RequestTimeout       time.Duration
	ParentID             string
	TurnID               string
	SessionID            string
	Role                 string
	Depth                int
	Budget               int
	Dispatcher           *runtime.Dispatcher
	OnEvent              func(Event)
	EventBus             *events.Bus // publishes agent events to extensible delivery
	// EventIdentity is a validated public identity snapshot for this turn.
	EventIdentity *events.Identity
	FinalWriter   io.Writer
	// RequireFinalText fails a turn that produced no assistant text anywhere
	// instead of reporting an empty success. Interactive surfaces set it: a turn
	// that renders as "done" with no answer is indistinguishable from the agent
	// stopping for no reason. Sub-agents leave it false, because buildResult
	// discards a task's output whenever its error is non-nil, and a task that
	// did its work through tools and then stopped without prose did succeed.
	RequireFinalText bool
	// PreparationManager is an optional root-owned preparation capability. It
	// has no checkpoint publisher and is therefore safe to pass to nested loops.
	PreparationManager contextmgr.PreparationManager
	PreparationInput   contextmgr.PrepareInput
}

type Loop struct {
	Completer provider.Completer
	Tools     *tools.Registry
	Messages  []provider.Message
	// LastPreparation is retained only after the final provider request
	// succeeds. The owning chat surface commits it; the loop never publishes.
	LastPreparation contextmgr.Preparation
	HasPreparation  bool
}

type toolExecResult struct {
	index           int // original position in ToolCalls slice
	toolCall        provider.ToolCall
	result          string
	truncated       bool // whether result was truncated for history
	err             error
	ephemeralMarker string
	// hookRuns are the lifecycle hooks that fired for this call, for display.
	hookRuns []runtime.HookRun
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
		// A canceled waiter can be the last reference, so it owes the same
		// cleanup as the release path. Keys are per file path, so skipping it
		// grows the map without bound over a long session.
		if kl.refs <= 0 && len(kl.ch) == 0 {
			delete(s.locks, key)
		}
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
	l.discardPreparation(opts)
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
			l.discardPreparation(opts)
			return lastText, fmt.Errorf("agent exceeded max_steps (%d)", opts.MaxSteps)
		}
		l.emitStep(opts, step)

		out, err := l.runStep(ctx, toolSpecs, opts)
		if err != nil {
			return lastText, err
		}
		if out.done {
			if out.text == "" {
				// A turn that produced no text anywhere is not a completed turn.
				// Reporting success here rendered as "done" with no answer, which
				// is indistinguishable from the agent stopping for no reason.
				if lastText == "" && opts.RequireFinalText {
					l.discardPreparation(opts)
					return "", fmt.Errorf("model returned no content (finish_reason=%q, step=%d)", out.finishReason, step)
				}
				out.text = lastText
			}
			return out.text, nil
		}
		if out.text != "" {
			lastText = out.text
		}
	}
}

// emitReasoning surfaces model chain of thought when the provider exposes
// it. ReasoningContent was parsed by the provider and then dropped on the
// floor - nothing consumed it, so reasoning never reached any UI.
func emitReasoning(opts Options, resp *provider.Response) {
	if resp == nil || resp.ReasoningContent == "" {
		return
	}
	emit(opts, Event{Kind: EventThinking, Content: resp.ReasoningContent})
}

func (l *Loop) emitStep(opts Options, step int) {
	d := fmt.Sprintf("%d/∞", step)
	if opts.MaxSteps > 0 {
		d = fmt.Sprintf("%d/%d", step, opts.MaxSteps)
	}
	emit(opts, Event{Kind: EventStep, Detail: d})
}

// pruneHistory trims history to the context budget and reports what went.
//
// Safe only where tool pairing is complete. runStep calls it before building a
// request, when history ends with the previous step's tool results, so dropping
// an assistant tool_call takes its results with it. Pruning while a tool_call
// is still awaiting results would drop the call and orphan the results appended
// afterwards: the request would stay valid (RepairToolPairing discards them)
// but the model would silently lose the output it asked for.
func (l *Loop) pruneHistory(opts Options) {
	beforeTokens := provider.MessagesTokens(l.Messages)
	l.Messages = provider.PruneMessagesKeepTurns(l.Messages, opts.MaxContextTokens)
	afterTokens := provider.MessagesTokens(l.Messages)
	if afterTokens >= beforeTokens {
		return
	}
	emit(opts, Event{
		Kind:   EventPrune,
		Detail: fmt.Sprintf("pruned ~%d tokens (before=%d after=%d budget=%d)", beforeTokens-afterTokens, beforeTokens, afterTokens, opts.MaxContextTokens),
	})
}

// teeWriter forwards live stream bytes to the real writer while keeping a copy,
// so an interrupted step can recover the text the user already saw. Writes come
// from the synchronous provider call on runStep's own goroutine, so no locking.
type teeWriter struct {
	w   io.Writer
	buf strings.Builder
}

func (t *teeWriter) Write(p []byte) (int, error) {
	t.buf.Write(p)
	if t.w == nil {
		return len(p), nil
	}
	return t.w.Write(p)
}

func (t *teeWriter) String() string { return t.buf.String() }

// stepOutcome is one agent step's result. text is the assistant prose the step
// produced, empty when the model said nothing renderable; finishReason is the
// upstream's own account of why it stopped, which is the only way to tell a
// deliberate empty answer from an exhausted output budget.
type stepOutcome struct {
	text         string
	done         bool
	finishReason string
}

// recordInterruptedPartial keeps, in history, the text an interrupted step had
// already streamed to the screen. Dropping it desynchronises the transcript from
// what the user read and makes the model repeat itself on the next request.
//
// Deliberately narrow to cancellation and deadlines. A truncated stream or an
// upstream error is a fragment, not a turn: admitting those would replay half an
// answer to the API as though it were complete, which is exactly what the
// provider's completion-signal guard exists to prevent.
func (l *Loop) recordInterruptedPartial(live *teeWriter) {
	if live == nil {
		return
	}
	partial := live.String()
	if strings.TrimSpace(partial) == "" {
		return
	}
	l.Messages = append(l.Messages, provider.Message{
		Role:      provider.RoleAssistant,
		Content:   partial,
		CreatedAt: time.Now(),
	})
}

// commitFinalAnswer records and surfaces a turn's closing answer. trimmed is the
// caller's emptiness predicate; the stored and written bytes stay verbatim.
//
// An assistant turn with no content and no tool calls cannot be sent back: it
// encodes to a bare {"role":"assistant"} and the API rejects the whole request.
// Never let one into history.
func (l *Loop) commitFinalAnswer(resp *provider.Response, trimmed string, stream bool, opts Options) {
	if trimmed == "" {
		return
	}
	l.Messages = append(l.Messages, provider.Message{
		Role:      provider.RoleAssistant,
		Content:   resp.Content,
		CreatedAt: time.Now(),
	})
	// When streaming, FinalWriter already received deltas - do not rewrite.
	if !stream && opts.FinalWriter != nil {
		_, _ = io.WriteString(opts.FinalWriter, resp.Content)
	}
	emit(opts, Event{Kind: EventAssistant, Content: resp.Content})
}

func (l *Loop) runStep(ctx context.Context, toolSpecs []provider.ToolSpec, opts Options) (stepOutcome, error) {
	if err := l.prepareStep(ctx, toolSpecs, opts); err != nil {
		return stepOutcome{}, err
	}

	// Stream when a FinalWriter is attached so TUI can show tokens live.
	// Content deltas go to FinalWriter; tool_calls are still assembled fully.
	stream := opts.FinalWriter != nil
	// Tee the live stream so an interrupted turn can keep exactly the text the
	// user already saw. One tee per step: on the content-then-tools path the
	// step succeeds and the tee is discarded, so revoked speech that is re-emitted
	// as an interim bubble cannot be appended twice.
	var live *teeWriter
	streamWriter := opts.FinalWriter
	if stream {
		live = &teeWriter{w: opts.FinalWriter}
		streamWriter = live
	}
	req := provider.Request{
		Model:        opts.Model,
		Messages:     l.Messages,
		Temperature:  opts.Temperature,
		MaxTokens:    opts.MaxTokens,
		Tools:        toolSpecs,
		ToolChoice:   "auto",
		Stream:       stream,
		StreamWriter: streamWriter,
		Timeout:      opts.RequestTimeout,
	}
	resp, err := l.requestStep(ctx, req, opts)
	if err != nil {
		interrupted := errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded)
		if interrupted {
			l.recordInterruptedPartial(live)
		} else {
			l.discardPreparation(opts)
		}
		return stepOutcome{}, err
	}
	// trimmed is a predicate only: it answers "did the model say anything
	// renderable". Every surface stores and writes resp.Content verbatim, because
	// trimming would strip the indentation off an answer that opens with a code
	// block and stop it rendering as one.
	emitReasoning(opts, resp)

	trimmed := strings.TrimSpace(resp.Content)
	out := stepOutcome{finishReason: resp.FinishReason}
	if trimmed != "" {
		out.text = resp.Content
	}

	if len(resp.ToolCalls) == 0 {
		out.done = true
		l.commitFinalAnswer(resp, trimmed, stream, opts)
		return out, nil
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
	if trimmed != "" {
		// Detail "interim" marks user-visible speech before tools (multi-bubble).
		emit(opts, Event{Kind: EventAssistant, Content: resp.Content, Detail: "interim"})
	}
	l.runToolBatch(ctx, resp.ToolCalls, opts)
	return out, nil
}

func (l *Loop) requestStep(ctx context.Context, req provider.Request, opts Options) (*provider.Response, error) {
	// Model-thinking progress applies only to the model call. Stop it before
	// processing tool calls so it cannot replace live tool-batch progress.
	heartbeat, heartbeatCancel := context.WithCancel(ctx)
	defer heartbeatCancel()
	go emitModelThinkingHeartbeat(heartbeat, opts)
	resp, err := l.Completer.ChatTurn(heartbeat, req)
	heartbeatCancel()
	if err == nil {
		EmitCacheUsage(opts, l.Completer.Name(), req.Model, resp.CacheUsage)
	}
	return resp, err
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
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			emit(opts, Event{
				Kind:   EventStep,
				Detail: "working",
			})
		case <-ctx.Done():
			return
		}
	}
}
