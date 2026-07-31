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
// with tool access. Sub-agents never receive delegation or orchestration
// control tools; only the root orchestrator may create or control runs.
type MultiStepHandler struct {
	// Completer is the LLM provider used by the sub-agent loop.
	Completer provider.Completer
	// FullRegistry is the parent's complete tool registry.
	// The handler creates a restricted copy (minus delegation tools).
	FullRegistry *tools.Registry
	// Dispatcher is the parent session dispatcher. It is used only as a policy
	// source; nested tool execution uses a dispatcher built from the restricted
	// registry.
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
	// MaxContextTokens is the local prompt budget for every nested request.
	MaxContextTokens int
	// MaxContextTokensFunc reads a session-owned budget at invocation time.
	// It supersedes MaxContextTokens when present.
	MaxContextTokensFunc func() int
	// MaxToolResultChars caps each tool result stored in the nested loop's
	// history, in bytes. 0 means uncapped. Set from the same
	// [tools] max_tool_result_bytes knob as the interactive session loop.
	MaxToolResultChars int
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
	scoped, err := h.newScopedLoop()
	if err != nil {
		return nil, err
	}
	defer scoped.dispatcher.Close()
	loop := scoped.loop
	steps, maxTokens, toolTimeout := h.setupAgentLoop()
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

	// Every event this loop emits — including heartbeats — is stamped with
	// the run's identity so the parent UI can attribute it. Without the
	// stamp, parallel subagents are indistinguishable downstream.
	stamped := StampEventOrigin(h.OnEvent, agent.EventOrigin{
		TaskID: req.ID,
		Agent:  req.Name,
		Depth:  req.Depth + 1,
	})

	opts := agent.Options{
		Model:            h.Model,
		MaxSteps:         steps,
		MaxTokens:        &maxTokens,
		MaxContextTokens: h.contextBudget(),
		// Same operator knob as the interactive loop; 0 = uncapped.
		MaxToolResultChars: h.MaxToolResultChars,
		ToolTimeout:        toolTimeout,
		Dispatcher:         scoped.dispatcher,
		ParentID:           req.ID,
		TurnID:             req.TurnID,
		SessionID:          req.SessionID,
		Role:               req.Role,
		Depth:              req.Depth + 1,
		Budget:             req.Budget,
	}

	// Start heartbeat goroutine for long-running visibility.
	// Emits periodic events so the orchestrator/TUI can show progress.
	heartbeatCtx, heartbeatStop := context.WithCancel(callCtx)
	var stepCount atomic.Int64
	taskStart := time.Now()
	defer heartbeatStop()
	go emitHeartbeat(heartbeatCtx, stamped, &stepCount)

	// Count steps, then hand off to the origin-stamped sink.
	opts.OnEvent = func(e agent.Event) {
		if e.Kind == agent.EventStep {
			stepCount.Add(1)
		}
		if stamped != nil {
			stamped(e)
		}
	}

	reply, err := loop.Run(callCtx, taskPrompt, opts)
	elapsed := time.Since(taskStart)
	return buildResult(reply, len(loop.Messages), elapsed, stepCount.Load(), err)
}

func (h *MultiStepHandler) contextBudget() int {
	if h.MaxContextTokensFunc != nil {
		return h.MaxContextTokensFunc()
	}
	return h.MaxContextTokens
}

func buildResult(reply string, messageCount int, elapsed time.Duration, stepCount int64, err error) (json.RawMessage, error) {
	result := map[string]any{
		"output":     reply,
		"steps":      messageCount / 2,
		"elapsed":    elapsed.Round(time.Millisecond).String(),
		"step_count": stepCount,
	}
	if err != nil {
		result["status"] = "error"
		// No content reference is emitted here. This layer has no repository, so
		// nothing stores the error or partial reply bytes under any key, and a
		// reference whose bytes nothing holds is worse than none: it hands the
		// model a pointer that cannot resolve. The resolvable reference for this
		// same task already exists on the correct path — the coordinator mints
		// and stores it from subagents.Result.Output/.Err.
		delete(result, "output")
	} else {
		result["status"] = "completed"
		// A subagent that did all its work via tool calls (grep, read_file)
		// can finish with empty reply text. Without a fallback the parent
		// sees "completed" with no output at all. Synthesize a minimal
		// summary so the result is never silently empty.
		if reply == "" && stepCount > 0 {
			result["output"] = fmt.Sprintf("(subagent completed %d steps with no final text reply)", stepCount)
		}
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
func (h *MultiStepHandler) setupAgentLoop() (int, int, time.Duration) {
	steps := h.MaxSteps
	// 0 is unlimited — the agent loop handles it.
	maxTokens := h.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	toolTimeout := h.ToolTimeout
	if toolTimeout <= 0 {
		toolTimeout = 300 * time.Second
	}
	return steps, maxTokens, toolTimeout
}

// restrictedRegistry returns a tool registry with delegation tools removed.
func (h *MultiStepHandler) restrictedRegistry() *tools.Registry {
	reg := tools.NewRegistry()
	blocked := map[string]bool{
		"delegate": true, "dispatch_tasks": true,
		"spawn_agent": true, "inspect_agents": true,
		"join_run": true, "cancel_run": true,
	}
	for _, t := range h.FullRegistry.List() {
		if _, privileged := t.(tools.PrivilegedTool); !blocked[t.Name()] && !privileged {
			reg.Register(t)
		}
	}
	return reg
}

// Ensure MultiStepHandler implements runtime.Handler at compile time.
var _ runtime.Handler = (*MultiStepHandler)(nil)
