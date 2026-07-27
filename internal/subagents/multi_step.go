package subagents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
	restrictedReg := h.restrictedRegistry()

	// Create the agent loop with restricted tools.
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

	// Start with system prompt, then user task.
	subPrompt := h.SystemPrompt
	if subPrompt == "" {
		subPrompt = "You are a focused sub-agent with access to tools. Complete the assigned task."
	}

	loop := &agent.Loop{
		Completer: h.Completer,
		Tools:     restrictedReg,
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: subPrompt},
		},
	}

	// Apply total timeout if specified.
	callCtx := ctx
	if h.TotalTimeout > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, h.TotalTimeout)
		defer cancel()
	} else if req.Timeout > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

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
