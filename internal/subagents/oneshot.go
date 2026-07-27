package subagents

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// OneShotHandler implements runtime.Handler by making a single LLM call
// with no tools and returning structured JSON results. This is the
// default subagent handler for both delegate and dispatch_tasks tools.
type OneShotHandler struct {
	// Completer is the LLM provider used to make the one-shot call.
	Completer provider.Completer
	// Model is the model name to use (e.g. "deepseek-v4-flash").
	Model string
	// SystemPrompt is the system prompt for the sub-agent LLM call.
	SystemPrompt string
}

// Invoke makes one LLM call with the task prompt and returns structured JSON.
func (h *OneShotHandler) Invoke(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
	if ctx.Err() != nil {
		return nil, fmt.Errorf("subagent %q: %w", req.Name, ctx.Err())
	}

	var taskPrompt string
	if err := json.Unmarshal(req.Input, &taskPrompt); err != nil {
		return nil, fmt.Errorf("subagent %q: invalid task input: %w", req.Name, err)
	}
	if taskPrompt == "" {
		return nil, fmt.Errorf("subagent %q: empty task prompt", req.Name)
	}

	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: h.SystemPrompt},
		{Role: provider.RoleUser, Content: taskPrompt},
	}

	callCtx := ctx
	if req.Timeout > 0 {
		if parentDeadline, ok := ctx.Deadline(); !ok || req.Timeout < time.Until(parentDeadline) {
			var cancel context.CancelFunc
			callCtx, cancel = context.WithTimeout(ctx, req.Timeout)
			defer cancel()
		}
	}

	reply, err := h.Completer.Chat(callCtx, provider.Request{
		Model:    h.Model,
		Messages: msgs,
	})
	if err != nil {
		return nil, fmt.Errorf("subagent %q: %w", req.Name, err)
	}

	return json.Marshal(map[string]any{
		"output": reply,
		"task":   taskPrompt,
	})
}

// Ensure OneShotHandler implements runtime.Handler.
var _ runtime.Handler = (*OneShotHandler)(nil)

// DefaultSubagentSystemPrompt is the default system prompt for sub-agents (DEPRECATED: use prompts.OneshotSystemPrompt).
const DefaultSubagentSystemPrompt = `You are a focused sub-agent with NO tools available.
You cannot read files, list directories, or execute commands.

## What you CAN do
Answer from general knowledge only: definitions, translations, summaries of
well-known concepts, explanations of standard patterns, language syntax, etc.

## What you CANNOT do
- Read files or directories
- Search code or the web
- Execute commands
- Give repo-specific answers (file contents, function signatures, project structure)

If a task requires information you cannot access, state clearly:
"I cannot answer this without file access."
Do NOT guess or invent.`

// DefaultSubagentTimeout is the default per-task timeout for one-shot subagents.
const DefaultSubagentTimeout = 60 * time.Second
