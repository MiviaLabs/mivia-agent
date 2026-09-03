package chat

import (
	"fmt"
	"io"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TurnOptions supplies an invocation-local capability surface. It never
// mutates the session-owned registry or binding, which keeps scoped tools from
// leaking into ordinary or concurrent turns. Cleanup runs after history has
// been scrubbed and committed.
type TurnOptions struct {
	Tools      *tools.Registry
	Dispatcher *runtime.Dispatcher
	Cleanup    func()
	// OnToolCancelReady, when non-nil, forwards to agent.Options's hook of
	// the same name for this turn (see its doc there). Only the agent-turn
	// path (sendAgent) wires it through; the plain path never runs the SDK
	// loop, so this is a no-op there.
	OnToolCancelReady func(agent.ToolCanceler)
}

// effectiveRequestTimeout normalizes a snapshot's per-request deadline: a
// zero value (a session built from a hand-built Resolved with no [chat]
// request_timeout_seconds resolution) falls back to DefaultRequestTimeout,
// never to "unbounded". Both turn shapes - agent (buildAgentTurnOptions)
// and plain (sendPlainLegacy / sendPlainContext) - share this one rule.
// The deadline bounds one LLM request only: Prepare, the summarizer call,
// and the durable commit keep their own bounds.
func effectiveRequestTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return DefaultRequestTimeout
	}
	return d
}

func (s *Session) buildAgentTurnOptions(snapshot agentTurnSnapshot, userText string, w io.Writer, turnDispatcher *runtime.Dispatcher, turn *TurnOptions) agent.Options {
	requestTimeout := effectiveRequestTimeout(snapshot.requestTimeout)
	opts := agent.Options{
		Model: snapshot.binding.Model, Temperature: snapshot.temperature, MaxTokens: snapshot.maxTokens,
		Reasoning: config.ModelReasoning(snapshot.binding.Profile),
		MaxSteps:  snapshot.maxSteps, MaxContextTokens: snapshot.contextBudget,
		MaxUnactedContinuations: snapshot.maxUnactedContinuations,
		MaxToolResultChars:      snapshot.maxToolResult,
		BatchResultBudgetBytes:  snapshot.batchResultBudget,
		RefOnlyTools:            snapshot.refOnlyTools,
		RemainderSpool:          snapshot.remainderSpool,
		RequestTimeout:          requestTimeout,
		ToolTimeout:             snapshot.toolTimeout,
		ToolRunTimeout:          snapshot.toolRunTimeout,
		ParentID:                "session",
		TurnID:                  fmt.Sprintf("turn:%d", snapshot.myTurn), SessionID: snapshot.sessionID,
		ApprovalGate:     snapshot.approvalGate,
		ApprovalStanding: snapshot.approvalStanding,
		ApprovalPolicy:   snapshot.approvalPolicy,
		FinalWriter:      w, OnEvent: snapshot.onEvent, EventBus: snapshot.eventBus, EventIdentity: snapshot.identity,
		RequireFinalText: true,
		// Step 1 has no Surface hook call (applySurfaceHook skips it), so the
		// turn's very first request must already carry the pinned snapshot;
		// safe to read fresh here since an active turn blocks /agent and
		// /model from republishing it mid-turn (BeginSurfaceSwitch).
		AdvertisedToolSpecs: s.AdvertisedToolSpecs(),
	}
	if snapshot.context.manager != nil {
		input := prepareInputForContext(snapshot.messages, snapshot.contextBudget, snapshot.maxTokens, snapshot.binding, snapshot.context.principal, snapshot.context.policy, snapshot.context.worktree)
		input.Revision = snapshot.context.revision
		input.CurrentObjective = userText
		opts.PreparationManager = snapshot.context.manager.PreparationManager
		opts.UsageWriter = snapshot.context.manager.UsageWriter
		opts.PreparationInput = input
		opts.SummaryConfig = agent.SummaryConfig{
			Summarizer:        snapshot.context.summarizer,
			UnavailableReason: snapshot.context.manager.SummaryUnavailableReason,
			Redaction:         snapshot.context.redaction,
		}
	}
	if turnDispatcher != nil {
		opts.Dispatcher = turnDispatcher
	}
	if turn != nil && turn.OnToolCancelReady != nil {
		opts.OnToolCancelReady = turn.OnToolCancelReady
	}
	// Mid-turn admission publication (w2a/w2d): a tool staged by load_tools
	// becomes callable from the next step via the loop's Surface hook, and a
	// deferred stage reports the reason instead of the unknown-tool denial.
	s.wireStepBoundaryAdmission(&opts, turn)
	return opts
}

// surfaceForTurnStart publishes any stage an earlier boundary could not
// (guarded boundary, failed save) at the earliest safe point of this turn: no
// batch is running, so closing the previous dispatcher is safe (R2-1), and the
// returned surface carries the staged tool so it is callable from the first
// step (DC-9: staged tools become callable from the next STEP, and this
// turn-start publication remains the cross-turn path for a stage that no step
// boundary could publish). A stage owned by this not-yet-run turn stays
// deferred for its own boundary (D7). Turns with nothing pending keep the
// snapshot path byte-for-byte.
//
// The returned token is the turn's operation fence RE-CAPTURED after the
// start-of-turn publication. That publication swaps the binding surface and
// bumps the operation fence (TryPublishAgentSurface -> invalidateLocked),
// which would fence this turn's own beginAgentTurn token out of
// commitPreparedTurn (chat-turnstart-admission-fences-own-turn). Re-capturing
// pins the fence to the post-publication epoch/revision/binding and to this
// turn's own id, so the loop's commit runs under the fence it actually
// executes on. When the publication deferred (no bump), the re-capture is a
// no-op re-read. Genuine supersession still refuses: a superseding turn
// advances s.turnID, and sameFence compares TurnID.
func (s *Session) surfaceForTurnStart(snapshot agentTurnSnapshot, turn *TurnOptions) (*tools.Registry, *runtime.Dispatcher, OperationToken, []provider.Message) {
	toolRegistry, turnDispatcher := resolveTurnExecutionSurface(snapshot.toolRegistry, snapshot.binding.Dispatcher, turn)
	if !snapshot.pendingAdmission {
		return toolRegistry, turnDispatcher, snapshot.token, snapshot.messages
	}
	s.PublishPendingAdmissionAtTurnStart()
	// The snapshot predates the start-of-turn publication. Read the live
	// surface once so the loop's registry and dispatcher carry the staged tool
	// and stay in agreement (INV-AG-29); a later mid-turn switch still cannot
	// change what this turn captured.
	s.mu.RLock()
	liveTools := s.Tools
	liveDispatcher := s.binding.Dispatcher
	// The publication can rewrite s.Messages (setMemoryMessageLocked places or
	// replaces the core-memory frame). The snapshot's message clone predates
	// that, so running the loop on it - and later committing the loop's
	// history - would stomp the just-published frame. Re-read the live history
	// under the same lock so the turn runs on, and commits on top of, the
	// post-publication messages.
	liveMessages := cloneContextMessages(s.Messages)
	s.mu.RUnlock()
	toolRegistry, turnDispatcher = resolveTurnExecutionSurface(liveTools, liveDispatcher, turn)
	return toolRegistry, turnDispatcher, s.captureTurnToken(snapshot.myTurn), liveMessages
}
