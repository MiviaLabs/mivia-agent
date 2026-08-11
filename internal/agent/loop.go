package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// ErrPromptBudgetExceeded means local history preparation could not fit the
// current request into the selected model's prompt budget.
var ErrPromptBudgetExceeded = errors.New("prompt exceeds model budget")

type Loop struct {
	Completer provider.Completer
	Tools     *tools.Registry
	Messages  []provider.Message
	// LastPreparation is retained only after the final provider request
	// succeeds. The owning chat surface commits it; the loop never publishes.
	LastPreparation contextmgr.Preparation
	HasPreparation  bool
	// PreparationErr records an interrupted recovery failure so the session can
	// surface the real cause instead of misreporting a checkpoint conflict.
	PreparationErr error
	// LastFinishReason is the provider finish reason of the last successfully
	// completed step of the most recent Run: "stop", "tool_calls", "length",
	// or another provider vocabulary value. Empty when no step completed. The
	// schema-repair loop reads it to distinguish a reply truncated by the
	// output budget (finish_reason "length") from ordinary invalid JSON, so it
	// can say so honestly instead of re-prompting with the same budget.
	LastFinishReason string
	// Calibration tracks the rolling EWMA correction ratio between estimated
	// and provider-reported token usage. It is updated after every successful
	// provider response that reports usage, and its Ratio is passed to
	// context planning and token usage events. The zero value (Ratio=0) is
	// safe: applyCalibration treats 0 as 1.0 (no correction).
	Calibration contextmgr.Calibration
	// Turn-level compaction accounting. A mid-turn step may elide enough to
	// fit before the final step that commits; emit uses the first compacting
	// BeforeTokens, the last compacting AfterTokens, and summed elision.
	turnCompacted      bool
	turnBeforeTokens   int
	turnAfterTokens    int
	turnElidedMessages int
	turnElidedBytes    int
	// softInterruptAt is the unix-nano timestamp of the last soft interrupt
	// (plan 54). It backs the cross-call SoftInterruptCooldown; watcher
	// goroutines write it and later calls' watchers read it, so it must be
	// atomic.
	softInterruptAt atomic.Int64
	workLimits      *workLimitMeter
}

func (l *Loop) Run(ctx context.Context, userText string, opts Options) (string, error) {
	if l.Completer == nil {
		return "", fmt.Errorf("nil completer")
	}
	if l.Tools == nil {
		return "", fmt.Errorf("nil tools")
	}
	l.discardPreparation(opts)
	l.resetTurnCompaction()
	if l.workLimits == nil || !opts.PreserveWorkLimits || l.workLimits.limits != opts.WorkLimits {
		l.workLimits = &workLimitMeter{limits: opts.WorkLimits}
	}
	if limit := opts.WorkLimits.MaxTurns; limit > 0 && (opts.MaxSteps <= 0 || limit < opts.MaxSteps) {
		opts.MaxSteps = limit
	}
	if deadline := opts.WorkLimits.DeadlineAt; !deadline.IsZero() {
		if parent, ok := ctx.Deadline(); !ok || deadline.Before(parent) {
			var cancel context.CancelFunc
			ctx, cancel = context.WithDeadline(ctx, deadline)
			defer cancel()
		}
	}
	if opts.SessionID == "" {
		opts.SessionID = runtime.NewSessionID()
	}
	// Each Run owns its finish-reason report: a previous run's reason must
	// never leak into the next caller's read.
	l.LastFinishReason = ""
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

		out, err := l.runStep(ctx, toolSpecs, opts, step)
		if err != nil {
			return lastText, err
		}
		// The final completed step's reason is what the caller reads: every
		// successful step overwrites the field, so a tool_calls step followed
		// by the closing text step reports the closing step's reason.
		if out.finishReason != "" {
			l.LastFinishReason = out.finishReason
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
// it. The event sink gets a redacted copy: reasoning is operator-facing, so it
// passes through the workspace's redaction policy before reaching OnEvent
// consumers (redact.Text is an identity when no policy is installed).
// Persistence into host history is separate and stays verbatim
// (commitFinalAnswer / processToolCalls copy resp.ReasoningContent onto the
// assistant Message), because the provider that produced the reasoning needs
// the raw bytes back on replay.
func emitReasoning(opts Options, resp *provider.Response) {
	if resp == nil || resp.ReasoningContent == "" {
		return
	}
	emit(opts, Event{Kind: EventThinking, Content: redact.Text(resp.ReasoningContent)})
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
func (l *Loop) pruneHistory(opts Options, toolSpecs []provider.ToolSpec) {
	beforeTokens := provider.MessagesTokens(l.Messages)
	if opts.MaxContextTokens > 0 {
		schemaCost := 0
		if len(toolSpecs) > 0 {
			if cost, err := provider.EstimateToolSchemaCost(toolSpecs); err == nil {
				schemaCost = cost
			}
		}
		// The rejection check (promptBudgetErrorWithTools) prices content,
		// message frames, and tool schemas, while PruneMessagesKeepTurns
		// accounts content only. Prune with the same accounting so a history
		// at the boundary is trimmed instead of rejected: pass 1 drops old
		// turns by content minus schema cost, pass 2 trims to the exact
		// frame- and schema-aware target of the remaining set.
		pass1 := opts.MaxContextTokens - schemaCost
		if pass1 < 1 {
			pass1 = 1
		}
		l.Messages = provider.PruneMessagesKeepTurns(l.Messages, pass1)
		overhead := provider.EstimateMessagesPromptCost(l.Messages, 0) - provider.MessagesTokens(l.Messages)
		target := opts.MaxContextTokens - schemaCost - overhead
		if target < 1 {
			target = 1
		}
		if provider.MessagesTokens(l.Messages) > target {
			l.Messages = provider.PruneMessagesKeepTurns(l.Messages, target)
		}
	}
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
		Role:             provider.RoleAssistant,
		Content:          resp.Content,
		ReasoningContent: resp.ReasoningContent,
		CreatedAt:        time.Now(),
	})
	// When streaming, FinalWriter already received deltas - do not rewrite.
	if !stream && opts.FinalWriter != nil {
		_, _ = io.WriteString(opts.FinalWriter, resp.Content)
	}
	emit(opts, Event{Kind: EventAssistant, Content: resp.Content})
}

func (l *Loop) runStep(ctx context.Context, toolSpecs []provider.ToolSpec, opts Options, step int) (stepOutcome, error) {
	// Stamp the step on the loop's own copy before any tool call is
	// dispatched: the caller's Options is never mutated, and the runtime
	// dispatcher keys per-turn dedup by (TurnID, ParentID, Step) so an
	// identical call re-issued in a LATER step re-runs (step-scoped dedup).
	opts.Step = step
	if err := l.prepareStep(ctx, toolSpecs, opts); err != nil {
		return stepOutcome{}, err
	}

	// Stream when a FinalWriter is attached so TUI can show tokens live.
	// Content deltas go to FinalWriter; tool_calls are still assembled fully.
	stream := opts.FinalWriter != nil
	// Tee the live stream so an interrupted turn can retain displayed text.
	var live *teeWriter
	streamWriter := opts.FinalWriter
	if stream {
		live = &teeWriter{w: opts.FinalWriter}
		streamWriter = live
	}
	req, err := l.stepRequest(toolSpecs, opts, stream, streamWriter)
	if err != nil {
		return stepOutcome{}, err
	}
	resp, err := l.requestStep(ctx, req, opts)
	if err != nil {
		if out, soft := l.steerInterruptOutcome(err, live, ctx); soft {
			return out, nil
		}
		// The sentinel with the turn ctx already canceled is a hard cancel
		// racing the steer fire: surface the real cause, never the sentinel.
		if errors.Is(err, errSteerInterrupt) {
			return stepOutcome{}, ctx.Err()
		}
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

	return l.processToolCalls(ctx, resp, trimmed, opts)
}

func (l *Loop) stepRequest(toolSpecs []provider.ToolSpec, opts Options, stream bool, streamWriter io.Writer) (provider.Request, error) {
	maxTokens, err := l.workLimits.outputCap(opts.MaxTokens)
	if err != nil {
		return provider.Request{}, err
	}
	return provider.Request{
		Model: opts.Model, Messages: l.Messages, Temperature: opts.Temperature,
		MaxTokens: maxTokens, Tools: toolSpecs, ToolChoice: "auto", Stream: stream,
		StreamWriter: streamWriter, Timeout: opts.RequestTimeout,
		ReasoningLevel: opts.Reasoning.Level, ReasoningDialect: opts.Reasoning.Dialect,
		DisableProviderReplay: opts.DisableProviderReplay,
	}, nil
}

func (l *Loop) requestStep(ctx context.Context, req provider.Request, opts Options) (*provider.Response, error) {
	// Model-thinking progress applies only to the model call. Stop it before
	// processing tool calls so it cannot replace live tool-batch progress.
	heartbeat, heartbeatCancel := context.WithCancel(ctx)
	defer heartbeatCancel()
	// Capture the cadence before spawning to prevent a concurrent test override.
	go emitModelThinkingHeartbeatAt(heartbeat, opts, modelThinkingHeartbeatInterval)

	// Soft-interrupt scope (plan 54 §4.3): a steer cancels ONLY the LLM call.
	// llmCtx is cancelable by the watcher below; the turn ctx (and any tool
	// batch running on it) is never canceled by a steer. The deferred
	// llmCancel() closes llmCtx when this call returns, waking the watcher via
	// llmCtx.Done() — it can never outlive the call.
	llmCtx, llmCancel := context.WithCancel(ctx)
	defer llmCancel()
	// The watcher is inert without an interrupt channel or a watchdog
	// interval: pending gates alone must not spawn it (PERF-1) - a pending
	// check is a gate, never a cancel source.
	var steerFired atomic.Bool
	if opts.InterruptCh != nil || opts.WatchdogInterval > 0 {
		go l.steerWatcher(ctx, llmCtx, llmCancel, opts, &steerFired)
	}

	estimatedTokens, _ := provider.EstimatePromptCost(req.Messages, req.Tools)
	if err := l.workLimits.reserveProvider(estimatedTokens, requestOutputReserve(req)); err != nil {
		return nil, err
	}
	resp, err := l.Completer.ChatTurn(llmCtx, req)
	heartbeatCancel()
	// Prompt-too-long recovery: compact and retry exactly once
	// (retryAfterPromptTooLong); a second rejection propagates unchanged.
	retried := false
	if err != nil && !opts.DisableProviderReplay && errors.Is(err, provider.ErrPromptTooLong) && ctx.Err() == nil {
		// Retry on a LIVE context: a steer may have canceled llmCtx (DC-8).
		retryCtx, retryCancel := context.WithCancel(ctx)
		defer retryCancel()
		resp, estimatedTokens, err = l.retryAfterPromptTooLong(req, opts, retryCtx, estimatedTokens)
		retried = true
	}
	// R2: a successful retry replaced l.Messages with the pruned history (plus
	// the compaction notice), so the preparation prepareStep recorded points at
	// the rejected, never-sent history. Committing it would fingerprint a
	// BaseDigest over bytes the checkpoint does not hold. Discard the stale
	// preparation and re-Prepare on what the retry actually sent; a re-Prepare
	// failure fails the turn honestly rather than committing a stale digest.
	if err == nil && retried {
		if err := l.refreshPreparationAfterRetry(ctx, req, opts); err != nil {
			return nil, err
		}
	}
	if err != nil && steerFired.Load() && errors.Is(err, context.Canceled) && ctx.Err() == nil {
		// Map to the sentinel ONLY when this call's own watcher canceled llmCtx
		// (the error is the llmCtx cancel) and the turn ctx is still alive. A
		// genuine provider error (500/timeout) that merely coincides with a
		// steer fire - or a hard turn-ctx cancel - propagates unchanged: the
		// sentinel must never mask real failures.
		return nil, errSteerInterrupt
	}
	if err == nil {
		EmitCacheUsage(opts, l.Completer.Name(), req.Model, resp.CacheUsage)
		// The ratio emitted with this turn's drift must be the calibration in
		// effect for THIS turn (what the planner budgeted against), not the
		// post-update EWMA. Update() runs after capturing it, so the event
		// reads "estimate X vs actual Y (ratio R)" with R the applied
		// correction - the raw per-turn ratio is Y/X. A zero ratio is the
		// zero-value calibrator meaning unity, so display it as 1.00 rather
		// than a misleading 0.00.
		ratio := l.Calibration.Ratio
		if ratio <= 0 {
			ratio = 1
		}
		if resp.TokenUsage.Reported && estimatedTokens > 0 && resp.TokenUsage.InputTokens > 0 {
			l.Calibration.Update(estimatedTokens, resp.TokenUsage.InputTokens)
		}
		EmitTokenUsage(opts, l.Completer.Name(), req.Model, resp.TokenUsage, estimatedTokens, ratio)
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

// modelThinkingHeartbeatInterval is the UI progress cadence while a provider
// request is in flight. Overridable in tests.
var modelThinkingHeartbeatInterval = 2 * time.Second

// emitModelThinkingHeartbeat runs the model-thinking progress heartbeat at the
// current package-level cadence. It exists for tests that override the interval
// before calling; production uses emitModelThinkingHeartbeatAt so the read
// happens before the goroutine spawns.
func emitModelThinkingHeartbeat(ctx context.Context, opts Options) {
	emitModelThinkingHeartbeatAt(ctx, opts, modelThinkingHeartbeatInterval)
}

// emitModelThinkingHeartbeatAt is the heartbeat loop. interval is captured by
// the caller so the package-level override variable is never read inside the
// goroutine (data-race-free under -race with concurrent test overrides).
func emitModelThinkingHeartbeatAt(ctx context.Context, opts Options, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			emit(opts, Event{
				Kind:   EventHeartbeat,
				Detail: "working",
			})
		case <-ctx.Done():
			return
		}
	}
}
