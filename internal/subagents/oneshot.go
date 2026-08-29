package subagents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/jschema"
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
	// OutputSchema is the handler-default schema, mirroring
	// MultiStepHandler.OutputSchema. Request.OutputSchema overrides it; nil on
	// both means a free-text answer.
	OutputSchema map[string]any
	// WireStream opts this one-shot call into the provider's wire-stream
	// transport: stream:true on the wire, the plain non-stream contract on
	// the return path (the full answer still comes back as one string). Set
	// from [subagents] wire_stream at the construction site.
	WireStream bool
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

	// dispatch_tasks promises the model that an output_schema is "Validated
	// before the task completes", and the agent field is optional - so an
	// agent-less task lands here. Ignoring req.OutputSchema made that promise
	// silently false: unvalidated prose came back reporting completed, and the
	// model was never even shown the contract it was being held to.
	compiled, appendix, cerr := compileRequestSchema(req.OutputSchema, h.OutputSchema)
	if cerr != nil {
		return nil, fmt.Errorf("subagent %q: %w", req.Name, cerr)
	}
	taskPrompt += appendix

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
	ask := func(messages []provider.Message) (string, error) {
		return h.Completer.Chat(callCtx, provider.Request{
			Model:            h.Model,
			Messages:         messages,
			MaxTokens:        h.MaxTokens,
			ReasoningLevel:   dial.Level,
			ReasoningDialect: dial.Dialect,
			SessionID:        req.SessionID,
			StreamTransport:  h.WireStream,
			// Request-scoped, like every other bound here: a caller that asked
			// for exactly one provider request must get exactly one.
			DisableProviderReplay: req.DisableProviderReplay,
		})
	}

	reply, err := ask(msgs)
	if err != nil {
		return nil, fmt.Errorf("subagent %q: %w", req.Name, err)
	}
	if compiled == nil {
		return json.Marshal(map[string]any{"output": reply})
	}
	structured, err := repairToSchema(compiled, ask, msgs, reply)
	if err != nil {
		return nil, fmt.Errorf("subagent %q: %w", req.Name, err)
	}
	return json.Marshal(map[string]any{"output": structured, "schema": "ok"})
}

// repairToSchema validates a reply and, on failure, re-asks once with a
// corrective turn before giving up.
//
// One retry, then fail closed. There is no tool loop here to spend a step
// budget on, and a second invalid reply to a restated contract is a task that
// cannot meet it - reporting that is honest, where reporting "completed" with
// unvalidated prose was not.
func repairToSchema(
	compiled *jschema.Compiled,
	ask func([]provider.Message) (string, error),
	msgs []provider.Message,
	reply string,
) (any, error) {
	structured, verr := validateSchemaReply(compiled, reply)
	if verr == nil {
		return structured, nil
	}
	corrective := append(append([]provider.Message(nil), msgs...),
		provider.Message{Role: provider.RoleAssistant, Content: reply},
		provider.Message{Role: provider.RoleUser, Content: jschema.FormatCorrectiveWithSchema(verr, compiled.Raw(), nil)},
	)
	retried, rerr := ask(corrective)
	if rerr != nil {
		return nil, rerr
	}
	structured, verr = validateSchemaReply(compiled, retried)
	if verr != nil {
		return nil, fmt.Errorf("%w: %v", ErrSchemaViolation, verr)
	}
	return structured, nil
}

// compileRequestSchema resolves the request schema over the handler default
// and compiles it, returning the model-facing prompt appendix. Shared shape
// with MultiStepHandler.compileOutputSchema so the two handlers cannot
// disagree about which schema wins or what the model is told.
func compileRequestSchema(requested, fallback map[string]any) (*jschema.Compiled, string, error) {
	schemaMap := requested
	if schemaMap == nil {
		schemaMap = fallback
	}
	if schemaMap == nil {
		return nil, "", nil
	}
	compiled, err := jschema.Compile(schemaMap)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrSchemaViolation, err)
	}
	return compiled, jschema.PromptAppendix(compiled.Raw()), nil
}

// validateSchemaReply extracts the output candidate from a reply and
// validates it, returning the parsed value. Same extraction the multi-step
// path uses, so a model that fences its JSON is treated identically by both
// handlers.
func validateSchemaReply(compiled *jschema.Compiled, reply string) (any, error) {
	return compiled.ValidateJSONBytes([]byte(jschema.ExtractOutputCandidate(reply)))
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
