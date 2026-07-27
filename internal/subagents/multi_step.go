package subagents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// MultiStepHandler implements runtime.Handler by creating a mini agent.Loop
// with tool access. The sub-agent gets all tools EXCEPT "delegate" and
// "dispatch_tasks" to prevent infinite recursion.
type MultiStepHandler struct {
	// Completer is the LLM provider used by the sub-agent loop.
	Completer provider.Completer
	// FullRegistry is the parent's complete tool registry.
	// The handler creates a restricted copy (minus delegation tools).
	FullRegistry *tools.Registry
	// Dispatcher preserves the parent's policy, lifecycle, and event sink for
	// nested tool execution while the exposed registry remains restricted.
	Dispatcher *runtime.Dispatcher
	// Model is the model name to use.
	Model string
	// SystemPrompt is the system prompt for the sub-agent.
	SystemPrompt string
	// MaxSteps is the maximum number of LLM turns.
	MaxSteps int
	// ToolTimeout is the per-tool-call timeout.
	ToolTimeout time.Duration
	// TotalTimeout is the maximum wall-clock time for the entire sub-agent.
	TotalTimeout time.Duration
	// MaxTokens is the max tokens per LLM response.
	MaxTokens int
	// OnEvent is called for sub-agent tool events (optional, for TUI).
	OnEvent func(agent.Event)
}

// Invoke creates a restricted agent loop and runs the assigned task.
func (h *MultiStepHandler) Invoke(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
	if ctx.Err() != nil {
		return nil, fmt.Errorf("multi-step subagent %q: %w", req.Name, ctx.Err())
	}

	var taskPrompt string
	if err := json.Unmarshal(req.Input, &taskPrompt); err != nil {
		return nil, fmt.Errorf("multi-step subagent %q: invalid task input: %w", req.Name, err)
	}
	if strings.TrimSpace(taskPrompt) == "" {
		return nil, fmt.Errorf("multi-step subagent %q: empty task prompt", req.Name)
	}

	return h.run(ctx, taskPrompt, req)
}

func (h *MultiStepHandler) run(ctx context.Context, taskPrompt string, req runtime.Request) (json.RawMessage, error) {
	loop, steps, maxTokens, toolTimeout := h.setupAgentLoop(req)
	subPrompt := h.SystemPrompt
	if subPrompt == "" {
		subPrompt = "You are a focused sub-agent with access to tools. Complete the assigned task."
	}
	loop.Messages = []provider.Message{
		{Role: provider.RoleSystem, Content: subPrompt},
	}

	// Apply total timeout if specified — but only if it's tighter than parent.
	// Never extend beyond parent deadline (that's the orchestrator's call).
	callCtx, cancel := h.timeoutContext(ctx, req)
	defer cancel()

	opts := agent.Options{
		Model:       h.Model,
		MaxSteps:    steps,
		MaxTokens:   &maxTokens,
		ToolTimeout: toolTimeout,
		Dispatcher:  h.Dispatcher,
		ParentID:    req.ID,
		TurnID:      req.TurnID,
		Depth:       req.Depth + 1,
		Budget:      req.Budget,
		OnEvent:     h.OnEvent,
	}

	// Start heartbeat goroutine for long-running visibility.
	// Emits periodic events so the orchestrator/TUI can show progress.
	heartbeatCtx, heartbeatStop := context.WithCancel(callCtx)
	var stepCount atomic.Int64
	defer heartbeatStop()
	go emitHeartbeat(heartbeatCtx, h.OnEvent, &stepCount)

	// Wrap OnEvent to count steps.
	origOnEvent := opts.OnEvent
	opts.OnEvent = func(e agent.Event) {
		if e.Kind == agent.EventStep {
			stepCount.Add(1)
		}
		if origOnEvent != nil {
			origOnEvent(e)
		}
	}

	reply, err := loop.Run(callCtx, taskPrompt, opts)

	result := map[string]any{
		"output": reply,
		"steps":  len(loop.Messages) / 2,
	}
	if err != nil {
		result["status"] = "error"
		result["error"] = err.Error()
	} else {
		result["status"] = "completed"
	}

	payload, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return nil, marshalErr
	}
	if err != nil {
		return payload, err
	}
	return payload, nil
}

// timeoutContext derives a context with timeout, but only if the requested
// timeout is tighter than the parent's remaining deadline. Never extends
// beyond parent — the orchestrator controls the outer bound.
// Returns the derived context and a cleanup func (caller must defer it).
func (h *MultiStepHandler) timeoutContext(ctx context.Context, req runtime.Request) (context.Context, func()) {
	if h.TotalTimeout > 0 {
		if parentDeadline, ok := ctx.Deadline(); !ok || h.TotalTimeout < time.Until(parentDeadline) {
			return context.WithTimeout(ctx, h.TotalTimeout)
		}
	} else if req.Timeout > 0 {
		if parentDeadline, ok := ctx.Deadline(); !ok || req.Timeout < time.Until(parentDeadline) {
			return context.WithTimeout(ctx, req.Timeout)
		}
	}
	return ctx, func() {}
}

// emitHeartbeat runs in a goroutine, emitting periodic heartbeat events
// so the orchestrator/TUI can see that a subagent is still alive.
// Stops when ctx is canceled.
func emitHeartbeat(ctx context.Context, onEvent func(agent.Event), stepCount *atomic.Int64) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	start := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if onEvent != nil {
				onEvent(agent.Event{
					Kind:   agent.EventSubagentHeartbeat,
					Detail: fmt.Sprintf("elapsed=%s steps=%d", time.Since(start).Round(time.Second), stepCount.Load()),
				})
			}
		}
	}
}

// setupAgentLoop creates the agent loop with restricted tools and default
// values. Returns the loop, max steps, max tokens, and per-tool timeout.
func (h *MultiStepHandler) setupAgentLoop(req runtime.Request) (*agent.Loop, int, int, time.Duration) {
	steps := h.MaxSteps
	if steps <= 0 {
		steps = 8
	}
	maxTokens := h.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	toolTimeout := h.ToolTimeout
	if toolTimeout <= 0 {
		toolTimeout = 60 * time.Second
	}
	return &agent.Loop{
		Completer: h.Completer,
		Tools:     h.restrictedRegistry(),
	}, steps, maxTokens, toolTimeout
}

// restrictedRegistry returns a tool registry with delegation tools removed.
func (h *MultiStepHandler) restrictedRegistry() *tools.Registry {
	reg := tools.NewRegistry()
	blocked := map[string]bool{"delegate": true, "dispatch_tasks": true}
	for _, t := range h.FullRegistry.List() {
		if !blocked[t.Name()] {
			reg.Register(t)
		}
	}
	return reg
}

// Ensure MultiStepHandler implements runtime.Handler at compile time.
var _ runtime.Handler = (*MultiStepHandler)(nil)
