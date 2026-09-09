package subagents

import (
	"context"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// softInterruptCooldown is the default minimum spacing between soft interrupts
// of an in-flight LLM call (plan 54 §4.3). The loop treats 0 as off, which
// tests use to disable the cap.
const softInterruptCooldown = 5 * time.Second

// scopedLoop pairs an agent loop with the dispatcher built from the same
// restricted registry. The pairing is the nested-agent authorization boundary.
type scopedLoop struct {
	loop       *agent.Loop
	dispatcher *runtime.Dispatcher
}

func (h *MultiStepHandler) newScopedLoop() (*scopedLoop, error) {
	reg := h.restrictedRegistry()
	dispatcher, err := runtime.NewToolDispatcher(reg, h.parentPolicy())
	if err != nil {
		return nil, fmt.Errorf("scoped tool dispatcher: %w", err)
	}
	return &scopedLoop{
		loop:       &agent.Loop{Completer: h.Completer, Tools: reg},
		dispatcher: dispatcher,
	}, nil
}

func (h *MultiStepHandler) parentPolicy() runtime.Policy {
	if h.Dispatcher == nil {
		return runtime.Policy{}
	}
	return h.Dispatcher.Policy()
}

// setupAgentLoop returns the defaults for a scoped agent loop.
// MaxSteps=0 means unlimited (no step cap).
// maxTokens is nil when unset (MaxTokens<=0), letting the provider use its
// model default. A hardcoded 4096 cap truncated comprehensive subagent
// reports mid-sentence at the LLM API level.
func (h *MultiStepHandler) setupAgentLoop() (int, *int, time.Duration) {
	steps := h.MaxSteps
	var maxTokens *int
	if h.MaxTokens > 0 {
		mt := h.MaxTokens
		maxTokens = &mt
	}
	toolTimeout := h.ToolTimeout
	if toolTimeout <= 0 {
		toolTimeout = 900 * time.Second
	}
	return steps, maxTokens, toolTimeout
}

// restrictedRegistry returns a fresh spawned-scope registry from FullRegistry.
// Filtering is delegated to tools.ScopedRegistry so object markers and the
// mandatory denylist stay consistent with agent-definition policy.
func (h *MultiStepHandler) restrictedRegistry() *tools.Registry {
	return tools.ScopedRegistry(h.FullRegistry, tools.ScopeOptions{Mode: tools.ScopeSpawned})
}

// seedMessages builds the loop's starting history: the system prompt at
// index 0 and, when present, the core-memory frame at index 1, before the
// task prompt the loop appends. The frame is background context and must
// precede the real objective; RoleSystem is only valid at index 0, so the
// frame is a sentinel-named user-role message.
func (h *MultiStepHandler) seedMessages() []provider.Message {
	subPrompt := h.SystemPrompt
	if subPrompt == "" {
		subPrompt = "You are a focused sub-agent with access to tools. Complete the assigned task."
	}
	messages := []provider.Message{{Role: provider.RoleSystem, Content: subPrompt}}
	if h.MemoryContext != "" {
		messages = append(messages, provider.Message{Role: provider.RoleUser, Name: chat.MemoryContextMessageName, Content: h.MemoryContext})
	}
	return messages
}

// loopOptions builds the nested loop's options. OnEvent is left to the caller,
// which owns the step counter and the origin-stamped sink.
func (h *MultiStepHandler) loopOptions(scoped *scopedLoop, steps int, maxTokens *int, toolTimeout time.Duration, req runtime.Request, taskPrompt string) agent.Options {
	opts := agent.Options{
		// The nested loop wires a BeforeStep mailbox drain
		// (applyMailboxAccess). It maps onto the SDK path through the
		// Steer injector installed in RunAgentLoopOnce: the SDK
		// drains the injector at the top of every iteration and at
		// every steered-stop downgrade point, growing history with
		// the framed parent message and downgrading a pending
		// StopSteered when the drain is non-empty. No Backend override
		// here: the SDK is the default and the BeforeStep carrier
		// lives there now.
		Model:            h.Model,
		Reasoning:        h.dial(),
		MaxSteps:         steps,
		MaxTokens:        maxTokens,
		MaxContextTokens: h.contextBudget(),
		// Same operator knob as the interactive loop; 0 = uncapped.
		MaxToolResultChars:     h.MaxToolResultChars,
		BatchResultBudgetBytes: h.BatchResultBudgetBytes,
		RefOnlyTools:           h.RefOnlyTools,
		RemainderSpool:         h.RemainderSpool,
		ToolTimeout:            toolTimeout,
		ToolRunTimeout:         h.ToolRunTimeout,
		RequestTimeout:         h.RequestTimeout,
		Dispatcher:             scoped.dispatcher,
		ParentID:               req.ID,
		TurnID:                 req.TurnID,
		SessionID:              req.SessionID,
		Role:                   req.Role,
		Depth:                  req.Depth + 1,
		Budget:                 req.Budget,
		WorkLimits:             runtime.LowestPositiveWorkLimits(h.WorkLimits, req.WorkLimits),
		DisableProviderReplay:  h.DisableProviderReplay || req.DisableProviderReplay,
		WireStreamTransport:    h.WireStreamTransport,
	}
	// The operator's approval wiring, read HERE rather than captured: see the
	// Approval field. Without it a delegated write tool ran unprompted while
	// the same call on the root path was gated.
	if h.Approval != nil {
		deps := h.Approval()
		opts.ApprovalGate = deps.Gate
		opts.ApprovalStanding = deps.Standing
		opts.ApprovalPolicy = deps.Policy
	}
	if h.ContextPreparationManager != nil {
		input := h.ContextPreparationInput
		if budget := h.contextBudget(); budget > 0 {
			input.Budget = budget
		}
		input.CurrentObjective = taskPrompt
		opts.PreparationManager = h.ContextPreparationManager
		opts.PreparationInput = input
	}
	return opts
}

// applyMailboxAccess wires the parent→child mailbox bundle (plan 54) into the
// nested loop options when the coordinator stamped one on ctx: the step-boundary
// drain, the soft-interrupt channel, the pending gate, the watchdog interval,
// and the interrupt cooldown. A context without a bundle leaves opts untouched
// (all steer machinery off).
func (h *MultiStepHandler) applyMailboxAccess(ctx context.Context, opts *agent.Options) {
	access, ok := runtime.MailboxAccessFrom(ctx)
	if !ok {
		return
	}
	opts.BeforeStep = parentMessageBeforeStep(access.Drain)
	opts.InterruptCh = access.Interrupt
	opts.MailboxPending = access.Pending
	opts.MailboxPendingInterrupt = access.PendingInterrupt
	opts.WatchdogInterval = h.SteerWatchdog
	opts.SoftInterruptCooldown = softInterruptCooldown
}

func (h *MultiStepHandler) discardPreparation(loop *agent.Loop) {
	if loop == nil || !loop.HasPreparation || h.ContextPreparationManager == nil {
		return
	}
	h.ContextPreparationManager.Discard(loop.LastPreparation)
	loop.HasPreparation = false
}

// dial is the reasoning setting this invocation's loop sends.
func (h *MultiStepHandler) dial() reasoning.Setting {
	if h.ReasoningFunc != nil {
		return h.ReasoningFunc()
	}
	return h.Reasoning
}

func (h *MultiStepHandler) contextBudget() int {
	if h.MaxContextTokensFunc != nil {
		return h.MaxContextTokensFunc()
	}
	return h.MaxContextTokens
}

// budgetContext derives a context bounded by the tightest of total (the
// whole-run budget), reqTimeout (the per-task timeout, which wins when
// tighter), and the parent deadline. A value <= 0 adds no bound. The parent
// deadline is never extended - the caller above owns the outer bound.
// Returns the derived context and a cleanup func (caller must defer it).
// Shared by MultiStepHandler.timeoutContext and OneShotHandler.Invoke so both
// handler families apply one identical clamp.
func budgetContext(ctx context.Context, total, reqTimeout time.Duration) (context.Context, func()) {
	if reqTimeout > 0 && (total <= 0 || reqTimeout < total) {
		total = reqTimeout
	}
	if total > 0 {
		if parentDeadline, ok := ctx.Deadline(); !ok || total < time.Until(parentDeadline) {
			return context.WithTimeout(ctx, total)
		}
	}
	return ctx, func() {}
}

// timeoutContext derives a context with timeout, but only if the requested
// timeout is tighter than the parent's remaining deadline. Never extends
// beyond parent - the orchestrator controls the outer bound.
// Returns the derived context and a cleanup func (caller must defer it).
func (h *MultiStepHandler) timeoutContext(ctx context.Context, req runtime.Request) (context.Context, func()) {
	return budgetContext(ctx, h.TotalTimeout, req.Timeout)
}
