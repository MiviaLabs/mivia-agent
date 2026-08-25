package agent

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
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
	// for summaryMemoKey and should not be retried across steps (on success,
	// non-retryable failure, or upon reaching maxSummaryAttemptsPerCompaction).
	summaryMemoKey       string
	summaryMemoValid     bool
	summaryMemoMessage   provider.Message
	summaryMemoHasMsg    bool
	summaryMemoReason    string
	summaryMemoAttempts  int
	summaryFailureReason string
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
	turnCompacted            bool
	turnCompactionEmitted    bool
	lastEmittedCompactionKey string
	turnBeforeTokens         int
	turnAfterTokens          int
	turnElidedMessages       int
	turnElidedBytes          int
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
	return l.runOnce(ctx, userText, opts)
}

// contextAccounting returns l.Completer's declared context-billing profile
// (provider.ContextAccountingFor's conservative zero value when the
// Completer does not declare one), so every accounting call this loop makes
// prices context the same way.
func (l *Loop) contextAccounting() provider.ContextAccountingProfile {
	return provider.ContextAccountingFor(l.Completer)
}

// teeWriter forwards live stream bytes to the real writer while keeping a copy,
// so an interrupted call can recover the text the user already saw.
//
// It also republishes each delta to opts.EventBus (Detail="delta") as it
// streams, not just the one aggregate EventAssistant the SDK completer
// wrapper emits once the whole response is ready. That aggregate is the
// only signal a cross-process observer (internal/hub's relay,
// mivia-agent-desktop) ever saw, so an external view of a plain-text reply
// showed nothing until the entire answer was already done, then the whole
// thing at once - "thinking" forever, never "streaming". Existing EventBus
// consumers are unaffected: agentEventBridgeCallback's own EventAssistant
// case only acts on Detail=="interim" (see its comment - the TUI's own
// display already gets live text via this same FinalWriter, not through
// the bus), and jsonTurnEventCallback has no EventAssistant case at all.

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

// SummaryFailureReason returns the classified reason why this turn produced no context summary,
// or empty if the summary was successfully injected or no summarizer was configured.
func (l *Loop) SummaryFailureReason() string {
	return l.summaryFailureReason
}

// TurnCompactionEmitted reports whether compaction was emitted mid-turn during step preparation.
func (l *Loop) TurnCompactionEmitted() bool {
	return l.turnCompactionEmitted
}

// streamRevoker, revokeStreamWriter, teeWriter, and the model-thinking
// heartbeat live in loop_stream.go.
