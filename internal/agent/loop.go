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
	// preCompactSource holds the pre-compaction history of the CURRENT
	// compacted preparation, so the summary request can quote the dropped
	// messages' real content. Set only when a prepare compacts, cleared at
	// the next prepare start; the prompt-too-long retry sets it from the
	// pre-prune history.
	preCompactSource []provider.Message
	// injectedSummary is the summary message the run last injected into a
	// provider request, exposed through InjectedSummary. It is deliberately
	// NOT part of l.Messages: the request injection stays ephemeral, and the
	// owning surface is what carries this into the turn's committed active
	// context so it outlives the turn.
	injectedSummary    provider.Message
	hasInjectedSummary bool
	// Summary memo: one Summarize attempt per compaction event, keyed by the
	// compacting preparation's identity (turnCompactionKey). Later steps of
	// the turn reuse the memoized RENDERED message byte-for-byte instead of
	// re-summarizing: a fresh Summarize per step is a fresh LLM request with a
	// unique prefix (near-zero provider prompt-cache hit) that injects
	// nondeterministic bytes. summaryMemoValid records that an attempt ran
	// for summaryMemoKey even when it failed, so a failed attempt is not
	// retried on every later step of the same compaction.
	summaryMemoKey     string
	summaryMemoValid   bool
	summaryMemoMessage provider.Message
	summaryMemoHasMsg  bool
	// turnCompactionKey identifies the newest REAL compaction of this turn
	// (recordPreparation sets it from the raw preparation, before the
	// turn-level Compacted overlay). A new compaction later in the turn
	// changes the key, which invalidates the memo and permits exactly one
	// fresh Summarize.
	turnCompactionKey string
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
	// turnElidedReasoningMessages/Bytes mirror the tool-result pair for the
	// reasoning-elision counters, so a later non-compacting step cannot
	// erase an earlier step's reasoning accounting.
	turnElidedReasoningMessages int
	turnElidedReasoningBytes    int
	// TurnState accumulates bounded, content-free host facts (omitted-message
	// evidence, tool names, changed surfaces, risks, latest assistant state)
	// for the summary envelope of the current run. Reset at Run start; it
	// never leaves the loop and is never consulted by planning, commit, or
	// checkpoint fingerprinting.
	TurnState *contextmgr.TurnState
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
	l.TurnState = contextmgr.NewTurnState()
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
	toolSpecs := l.initialToolSpecs(opts)
	var lastText string
	for step := 1; ; step++ {
		if opts.MaxSteps > 0 && step > opts.MaxSteps {
			l.discardPreparation(opts)
			return lastText, fmt.Errorf("agent exceeded max_steps (%d)", opts.MaxSteps)
		}
		// The surface hook is the host's mid-turn publication point: it runs at
		// the top of every step after the first and may replace this step's
		// registry, dispatcher, specs, and spool (applySurfaceHook).
		l.applySurfaceHook(&opts, &toolSpecs, step)
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

// Step emission (emitReasoning, emitStep) and the per-step surface refresh
// (applySurfaceHook) live in loop_step.go.

// pruneHistory trims history to the context budget and reports what went.
//
// Safe only where tool pairing is complete. runStep calls it before building a
// request, when history ends with the previous step's tool results, so dropping
// an assistant tool_call takes its results with it. Pruning while a tool_call
// is still awaiting results would drop the call and orphan the results appended
// afterwards: the request would stay valid (RepairToolPairing discards them)
// but the model would silently lose the output it asked for.
// contextAccounting returns l.Completer's declared context-billing profile
// (provider.ContextAccountingFor's conservative zero value when the
// Completer does not declare one), so every accounting call this loop makes
// - pruning, budget rejection, and the calibration estimate in loop_request.go
// - prices context the same way.
func (l *Loop) contextAccounting() provider.ContextAccountingProfile {
	return provider.ContextAccountingFor(l.Completer)
}

func (l *Loop) pruneHistory(opts Options, toolSpecs []provider.ToolSpec) {
	profile := l.contextAccounting()
	beforeTokens := provider.MessagesTokens(l.Messages, profile)
	if opts.MaxContextTokens > 0 {
		schemaCost := 0
		if len(toolSpecs) > 0 {
			if cost, err := provider.EstimateToolSchemaCost(toolSpecs); err == nil {
				schemaCost = cost
			}
		}
		// Hysteresis mirrors contextmgr.Plan (trigger 80%, target 50% of the
		// budget): pruning only once the estimated request cost crosses the
		// trigger, and down past it to the target when it does, means the
		// front-dropped prefix - and the provider prompt-cache miss it costs -
		// happens once per many steps instead of once per step at the boundary.
		trigger := contextmgr.PercentFloor(opts.MaxContextTokens, 4, 5)
		totalCost := provider.EstimateMessagesPromptCost(l.Messages, schemaCost, profile)
		if totalCost >= trigger {
			budget := contextmgr.PercentFloor(opts.MaxContextTokens, 1, 2)
			// The rejection check (promptBudgetErrorWithTools) prices content,
			// message frames, and tool schemas, while PruneMessagesKeepTurns
			// accounts content only. Prune with the same accounting so a history
			// at the boundary is trimmed instead of rejected: pass 1 drops old
			// turns by content minus schema cost, pass 2 trims to the exact
			// frame- and schema-aware target of the remaining set.
			pass1 := budget - schemaCost
			if pass1 < 1 {
				pass1 = 1
			}
			l.Messages = provider.PruneMessagesKeepTurns(l.Messages, pass1, profile)
			overhead := provider.EstimateMessagesPromptCost(l.Messages, 0, profile) - provider.MessagesTokens(l.Messages, profile)
			target := budget - schemaCost - overhead
			if target < 1 {
				target = 1
			}
			if provider.MessagesTokens(l.Messages, profile) > target {
				l.Messages = provider.PruneMessagesKeepTurns(l.Messages, target, profile)
			}
		}
	}
	afterTokens := provider.MessagesTokens(l.Messages, profile)
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
//
// It also republishes each delta to opts.EventBus (Detail="delta") as it
// streams, not just the one aggregate EventAssistant commitFinalAnswer emits
// once the whole response is ready. That aggregate is the only signal a
// cross-process observer (internal/hub's relay, mivia-agent-desktop) ever
// saw, so an external view of a plain-text reply showed nothing until the
// entire answer was already done, then the whole thing at once - "thinking"
// forever, never "streaming". Existing EventBus consumers are unaffected:
// agentEventBridgeCallback's own EventAssistant case only acts on
// Detail=="interim" (see its comment - the TUI's own display already gets
// live text via this same FinalWriter, not through the bus), and
// jsonTurnEventCallback has no EventAssistant case at all.
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
	l.recordAssistantState(resp.Content)
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
		live = &teeWriter{w: opts.FinalWriter, opts: opts}
		streamWriter = live
	}
	req, err := l.stepRequest(ctx, toolSpecs, opts, stream, streamWriter)
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

func (l *Loop) stepRequest(ctx context.Context, toolSpecs []provider.ToolSpec, opts Options, stream bool, streamWriter io.Writer) (provider.Request, error) {
	maxTokens, err := l.workLimits.outputCap(opts.MaxTokens)
	if err != nil {
		return provider.Request{}, err
	}
	messages := l.Messages
	// Phase 2 summary injection: when the current preparation compacted and a
	// Summarizer is wired, build the request from an EPHEMERAL clone carrying
	// the validated summary APPENDED after the latest user objective, so the
	// structural messages keep their indices and the provider prompt-cache
	// prefix stays valid (append extends the prefix; a mid-history insert
	// splits it). l.Messages stays
	// structural, so planning, idempotency, BaseDigest, and checkpoint bytes
	// are untouched. On any failure the request falls back structural-only.
	if opts.SummaryConfig.Summarizer != nil && l.HasPreparation && l.LastPreparation.Compacted {
		messages = l.injectSummary(ctx, opts)
	}
	// Soft conclude: when a work bound (deadline, output budget, tool-call
	// budget, or step budget) is close, tell the model to wrap up so it
	// returns its best valid result instead of the bound hard-aborting the run
	// mid-work. The instruction is appended to an EPHEMERAL copy — never to
	// l.Messages — so history, checkpoints, and replays stay untouched. Order
	// is pinned: structural messages, then the injected summary, then this
	// nudge last.
	if conclude := l.concludeInstruction(ctx, opts); conclude != "" {
		cp := make([]provider.Message, 0, len(messages)+1)
		cp = append(cp, messages...)
		// The Name marks this as a host injection: the DeepSeek reject gate
		// (internal/provider terminalToolExchange) trims trailing NAMED user
		// messages before it decides whether the current tool exchange is
		// terminal, so this nudge cannot cause the exchange and its tool
		// results to be dropped from the wire.
		cp = append(cp, provider.Message{Role: provider.RoleUser, Content: conclude, Name: ConcludeNudgeMessageName, CreatedAt: time.Now()})
		messages = cp
		emit(opts, Event{Kind: EventWorkLimit, Detail: "conclude: approaching a work bound"})
	}
	return provider.Request{
		Model: opts.Model, Messages: messages, Temperature: opts.Temperature,
		MaxTokens: maxTokens, Tools: toolSpecs, ToolChoice: "auto", Stream: stream,
		StreamWriter: streamWriter, Timeout: opts.RequestTimeout,
		ReasoningLevel: opts.Reasoning.Level, ReasoningDialect: opts.Reasoning.Dialect,
		DisableProviderReplay: opts.DisableProviderReplay,
		SessionID:             opts.SessionID,
	}, nil
}

// emitTurnUsage surfaces the cache hit and token drift of a completed
// provider call. The ratio emitted with this turn's drift must be the
// calibration in effect for THIS turn (what the planner budgeted against),
// not the post-update EWMA. Update() runs after capturing it, so the event
// reads "estimate X vs actual Y (ratio R)" with R the applied correction -
// the raw per-turn ratio is Y/X. A zero ratio is the zero-value calibrator
// meaning unity, so display it as 1.00 rather than a misleading 0.00.
func (l *Loop) emitTurnUsage(ctx context.Context, opts Options, req provider.Request, resp *provider.Response, estimatedTokens int) {
	EmitCacheUsage(ctx, opts, l.Completer.Name(), req.Model, resp.CacheUsage)
	ratio := l.Calibration.Ratio
	if ratio <= 0 {
		ratio = 1
	}
	if resp.TokenUsage.Reported && estimatedTokens > 0 && resp.TokenUsage.InputTokens > 0 {
		l.Calibration.Update(estimatedTokens, resp.TokenUsage.InputTokens)
	}
	EmitTokenUsage(ctx, opts, l.Completer.Name(), req.Model, resp.TokenUsage, estimatedTokens, ratio)
}

// streamRevoker, revokeStreamWriter, teeWriter, and the model-thinking
// heartbeat live in loop_stream.go.
