package subagents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/prompts"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
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
	// MaxContextTokens rejects an irreducible nested prompt locally.
	MaxContextTokens int
	// MaxContextTokensFunc reads a session-owned budget at invocation time.
	// It supersedes MaxContextTokens when present.
	MaxContextTokensFunc func() int
	// MaxTokens reserves the configured completion allowance.
	MaxTokens *int
	// Reasoning is the dial configured for Model. A delegated task runs on a
	// configured model just like the root session, so it must think at the
	// depth that model declares rather than at the provider's default.
	Reasoning reasoning.Setting
	// ReasoningFunc reads a session-owned dial at invocation time. It
	// supersedes Reasoning when present, so a runtime effort choice reaches a
	// handler built before the choice was made.
	ReasoningFunc func() reasoning.Setting
	// TotalTimeout is the maximum wall-clock time for the whole call, with
	// the same semantics as MultiStepHandler.TotalTimeout: <= 0 adds no
	// bound, a tighter req.Timeout wins, and the parent deadline is never
	// extended. Construction sites that carry the
	// default_total_timeout_seconds budget set it via totalTaskTimeout-style
	// resolution; zero leaves the per-task timeout as the only bound.
	TotalTimeout time.Duration
}

// dial is the reasoning setting this invocation sends.
func (h *OneShotHandler) dial() reasoning.Setting {
	if h.ReasoningFunc != nil {
		return h.ReasoningFunc()
	}
	return h.Reasoning
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
	if strings.TrimSpace(taskPrompt) == "" {
		return nil, fmt.Errorf("subagent %q: empty task prompt", req.Name)
	}

	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: h.SystemPrompt},
		{Role: provider.RoleUser, Content: taskPrompt},
	}
	maxContextTokens := h.MaxContextTokens
	if h.MaxContextTokensFunc != nil {
		maxContextTokens = h.MaxContextTokensFunc()
	}
	cost, err := provider.EstimatePromptCost(msgs, nil, provider.ContextAccountingFor(h.Completer))
	if err != nil {
		return nil, fmt.Errorf("%w: estimate request cost: %v", agent.ErrPromptBudgetExceeded, err)
	}
	if maxContextTokens > 0 && cost > maxContextTokens {
		return nil, fmt.Errorf("%w (%d > %d tokens)", agent.ErrPromptBudgetExceeded, cost, maxContextTokens)
	}

	// Same clamp as the multi-step handler: the whole-call total budget and
	// the per-task timeout apply only when tighter than the parent deadline,
	// and the parent deadline is never extended.
	callCtx, cancel := budgetContext(ctx, h.TotalTimeout, req.Timeout)
	defer cancel()

	dial := h.dial()
	reply, err := h.Completer.Chat(callCtx, provider.Request{
		Model:            h.Model,
		Messages:         msgs,
		MaxTokens:        h.MaxTokens,
		ReasoningLevel:   dial.Level,
		ReasoningDialect: dial.Dialect,
		SessionID:        req.SessionID,
	})
	if err != nil {
		return nil, fmt.Errorf("subagent %q: %w", req.Name, err)
	}

	return json.Marshal(map[string]any{
		"output": reply,
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
Do NOT guess or invent.

` + prompts.WritingStandard

// DefaultSubagentTimeout is retained for callers that invoke a one-shot
// handler directly. Coordinator tasks use their explicit effective timeout.
const DefaultSubagentTimeout = 15 * time.Minute
