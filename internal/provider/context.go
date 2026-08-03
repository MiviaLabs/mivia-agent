// Package provider implements LLM chat adapters for mivia.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"
)

// estimateTokens returns a rough token count for a string.
// Uses ~4 chars per token heuristic (conservative for mixed code/text).
func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	n := len(s) / 4
	if n < 1 {
		return 1
	}
	return n
}

// MessageTokens returns the estimated token count for a single message.
func MessageTokens(m Message) int {
	total := estimateTokens(m.Content)
	total += estimateTokens(m.ReasoningContent)
	// Tool call arguments count too.
	for _, tc := range m.ToolCalls {
		total += estimateTokens(tc.Function.Name)
		total += estimateTokens(tc.Function.Arguments)
	}
	return total
}

// MessagesTokens returns the estimated total token count for a slice of messages.
func MessagesTokens(msgs []Message) int {
	total := 0
	for _, m := range msgs {
		total += MessageTokens(m)
	}
	return total
}

const (
	requestFrameTokens = 3
	messageFrameTokens = 4
	toolFrameTokens    = 4
	schemaFrameTokens  = 4
)

// EstimateRequestCost returns a conservative, provider-neutral request cost.
// The estimate intentionally charges for fields that a compact content-only
// estimator misses: message framing, roles, names, tool IDs, function calls,
// registered tool schemas, and the reserved completion allowance.
//
// This is an accounting helper, not a provider tokenizer. Callers use the
// same function before pruning, planning, and local hard-budget rejection so
// boundary decisions do not depend on the surface that made the request.
func EstimateRequestCost(messages []Message, tools []ToolSpec, outputReserve int) (int, error) {
	if outputReserve < 0 {
		return 0, fmt.Errorf("output reserve must not be negative")
	}
	total := requestFrameTokens + outputReserve
	for _, message := range messages {
		total += messageFrameTokens + estimateTokens(message.Role)
		total += estimateTokens(message.Content)
		total += estimateTokens(message.ReasoningContent)
		total += estimateTokens(message.Name)
		total += estimateTokens(message.ToolCallID)
		for _, call := range message.ToolCalls {
			total += toolFrameTokens + estimateTokens(call.ID)
			total += estimateTokens(call.Type)
			total += estimateTokens(call.Function.Name)
			total += estimateTokens(call.Function.Arguments)
		}
	}
	for _, tool := range tools {
		encoded, err := json.Marshal(tool)
		if err != nil {
			return 0, fmt.Errorf("marshal tool schema for cost: %w", err)
		}
		total += schemaFrameTokens + estimateTokens(string(encoded))
	}
	return total, nil
}

// EstimateMessageTokens estimates the token cost of a single message using
// the len(s)/4 heuristic with per-role and per-call frame constants. This
// avoids re-marshaling tool schemas when only per-message costs are needed
// (e.g., the planner's incremental tail-fill loop).
//
// It cannot fail - the cost is pure arithmetic over fields already in memory,
// with no marshaling. Returning no error keeps callers from writing an error
// branch that can never be taken (and never be tested).
func EstimateMessageTokens(msg Message) int {
	total := messageFrameTokens + estimateTokens(msg.Role)
	total += estimateTokens(msg.Content)
	total += estimateTokens(msg.ReasoningContent)
	total += estimateTokens(msg.Name)
	total += estimateTokens(msg.ToolCallID)
	for _, call := range msg.ToolCalls {
		total += toolFrameTokens + estimateTokens(call.ID)
		total += estimateTokens(call.Type)
		total += estimateTokens(call.Function.Name)
		total += estimateTokens(call.Function.Arguments)
	}
	return total
}

// EstimateToolSchemaCost computes the tool-schema portion of prompt cost once,
// so callers can hoist it out of hot loops. Returns 0 for an empty or nil list.
func EstimateToolSchemaCost(tools []ToolSpec) (int, error) {
	total := 0
	for _, tool := range tools {
		encoded, err := json.Marshal(tool)
		if err != nil {
			return 0, fmt.Errorf("marshal tool schema for cost: %w", err)
		}
		total += schemaFrameTokens + estimateTokens(string(encoded))
	}
	return total, nil
}

// EstimateMessagesPromptCost is EstimatePromptCost with the tool-schema charge
// supplied by the caller instead of recomputed. Callers that price several
// candidate message selections against one fixed tool list hoist
// EstimateToolSchemaCost out of the loop and pass its result here, which is
// exactly the same number without re-marshaling every schema per candidate.
//
// It cannot fail: with no tools to marshal, the remaining cost is arithmetic
// over fields already in memory.
func EstimateMessagesPromptCost(messages []Message, schemaCost int) int {
	total := requestFrameTokens
	for _, message := range messages {
		total += EstimateMessageTokens(message)
	}
	return total + schemaCost
}

// EstimatePromptCost returns the input-side request cost. Callers whose budget
// already excludes the reserved completion allowance must use this rather than
// charging that allowance a second time.
func EstimatePromptCost(messages []Message, tools []ToolSpec) (int, error) {
	return EstimateRequestCost(messages, tools, 0)
}

// RequestTokens returns the request cost using MaxTokens as the output
// reserve. It is the convenient form for a fully assembled provider request.
func RequestTokens(request Request) (int, error) {
	reserve := 0
	if request.MaxTokens != nil {
		reserve = *request.MaxTokens
	}
	return EstimateRequestCost(request.Messages, request.Tools, reserve)
}

// ValidateToolPairing rejects provider message histories that cannot be sent
// as a complete sequence. It deliberately repairs nothing; transport-level
// compatibility code may still use RepairToolPairing for legacy histories,
// while context planning must fail closed instead of silently losing data.
func ValidateToolPairing(messages []Message) error {
	seenSystem := false
	pending := map[string]ToolCall{}
	answered := map[string]struct{}{}
	for index, message := range messages {
		if message.Role == RoleSystem {
			if index != 0 || seenSystem || len(message.ToolCalls) > 0 || message.ToolCallID != "" || message.Name != "" || message.ReasoningContent != "" {
				return fmt.Errorf("invalid system message at index %d", index)
			}
			seenSystem = true
			continue
		}
		if message.Role == RoleUser {
			if len(message.ToolCalls) > 0 || message.ToolCallID != "" || message.Name != "" || message.ReasoningContent != "" {
				return fmt.Errorf("invalid user message at index %d", index)
			}
			if strings.TrimSpace(message.Content) == "" {
				return fmt.Errorf("empty user message at index %d", index)
			}
			if len(pending) > 0 {
				return fmt.Errorf("tool calls remain unanswered before user message at index %d", index)
			}
			continue
		}
		switch message.Role {
		case RoleAssistant:
			if message.ToolCallID != "" || message.Name != "" {
				return fmt.Errorf("invalid assistant metadata at index %d", index)
			}
			if len(message.ToolCalls) == 0 && strings.TrimSpace(message.Content) == "" {
				return fmt.Errorf("assistant message at index %d has no content or tool calls", index)
			}
			if len(pending) > 0 {
				return fmt.Errorf("tool calls remain unanswered before assistant message at index %d", index)
			}
			for _, call := range message.ToolCalls {
				if err := validateToolCall(call); err != nil {
					return fmt.Errorf("assistant message at index %d: %w", index, err)
				}
				if _, exists := pending[call.ID]; exists {
					return fmt.Errorf("duplicate tool call ID %q", call.ID)
				}
				if _, exists := answered[call.ID]; exists {
					return fmt.Errorf("reused tool call ID %q", call.ID)
				}
				pending[call.ID] = call
			}
		case RoleTool:
			if len(message.ToolCalls) > 0 || message.ToolCallID == "" || message.ReasoningContent != "" {
				return fmt.Errorf("invalid tool result at index %d", index)
			}
			call, exists := pending[message.ToolCallID]
			if !exists {
				return fmt.Errorf("orphan tool result %q", message.ToolCallID)
			}
			if message.Name != "" && message.Name != call.Function.Name {
				return fmt.Errorf("tool result %q names %q, want %q", message.ToolCallID, message.Name, call.Function.Name)
			}
			delete(pending, message.ToolCallID)
			answered[message.ToolCallID] = struct{}{}
		default:
			return fmt.Errorf("unsupported message role %q at index %d", message.Role, index)
		}
	}
	if len(pending) > 0 {
		return fmt.Errorf("unterminated tool call")
	}
	return nil
}

func validateToolCall(call ToolCall) error {
	if strings.TrimSpace(call.ID) == "" {
		return fmt.Errorf("tool call has no ID")
	}
	if call.Type != "function" {
		return fmt.Errorf("tool call %q has unsupported type %q", call.ID, call.Type)
	}
	if strings.TrimSpace(call.Function.Name) == "" {
		return fmt.Errorf("tool call %q has no function name", call.ID)
	}
	if strings.TrimSpace(call.Function.Arguments) == "" || !json.Valid([]byte(call.Function.Arguments)) {
		return fmt.Errorf("tool call %q has malformed arguments", call.ID)
	}
	return nil
}

// PruneMessagesKeepTurns is a smarter pruner that removes entire "turns"
// (user → assistant/tool exchanges) to preserve conversational coherence.
// It always keeps the system prompt and the most recent turns within budget.
func PruneMessagesKeepTurns(msgs []Message, maxTokens int) []Message {
	if maxTokens <= 0 || len(msgs) <= 1 || MessagesTokens(msgs) <= maxTokens {
		return msgs
	}
	system, tail := splitSystemMessage(msgs)
	budget := maxTokens - MessageTokens(system)
	if budget < 1 {
		budget = 1
	}
	kept := tail[keepTurnStart(groupMessageTurns(tail), len(tail), budget):]
	return joinPrunedMessages(system, pruneWithinTurn(kept, budget))
}

type messageTurn struct{ start, tokens int }

func splitSystemMessage(msgs []Message) (Message, []Message) {
	if msgs[0].Role == RoleSystem {
		return msgs[0], msgs[1:]
	}
	return Message{}, msgs
}

func groupMessageTurns(msgs []Message) []messageTurn {
	var turns []messageTurn
	start, tokens := -1, 0
	for index, message := range msgs {
		if message.Role == RoleUser && start >= 0 {
			turns = append(turns, messageTurn{start, tokens})
			start, tokens = index, MessageTokens(message)
			continue
		}
		if start < 0 {
			start = index
		}
		tokens += MessageTokens(message)
	}
	if start >= 0 {
		turns = append(turns, messageTurn{start, tokens})
	}
	return turns
}

func keepTurnStart(turns []messageTurn, tailLength, budget int) int {
	keepStart, running := tailLength, 0
	for index := len(turns) - 1; index >= 0; index-- {
		running += turns[index].tokens
		if running <= budget || index == len(turns)-1 {
			keepStart = turns[index].start
			continue
		}
		break
	}
	return keepStart
}

// pruneWithinTurn shrinks a single turn that is over budget on its own.
//
// An agentic loop appends one user message and then only assistant/tool
// messages, so an entire tool-heavy run is one turn: turn-granular pruning has
// nothing to drop and the prompt grows until the provider rejects it
// mid-run. Dropping the oldest tool exchanges keeps the turn's intent (the user
// message) and its most recent findings, which is what the next step needs.
func pruneWithinTurn(msgs []Message, budget int) []Message {
	for MessagesTokens(msgs) > budget {
		smaller := dropOldestToolExchange(msgs)
		if smaller == nil {
			return msgs // only the turn header and plain replies left
		}
		msgs = smaller
	}
	return msgs
}

// dropOldestToolExchange removes the earliest assistant tool_call message
// together with every tool result answering it, mirroring the pairing invariant
// RepairToolPairing enforces: an announced call without its result, or a result
// without its call, makes the API reject the whole request. Returns nil when
// the slice holds no removable exchange.
func dropOldestToolExchange(msgs []Message) []Message {
	for index, message := range msgs {
		if message.Role != RoleAssistant || len(message.ToolCalls) == 0 {
			continue
		}
		announced := make(map[string]bool, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			announced[call.ID] = true
		}
		out := make([]Message, 0, len(msgs)-1)
		out = append(out, msgs[:index]...)
		adjacent := true
		for _, rest := range msgs[index+1:] {
			// An id-less tool result cannot be matched by id, but it belongs to
			// the exchange it directly follows and is dropped as an orphan
			// downstream anyway, so it goes with that exchange here.
			if rest.Role == RoleTool && (announced[rest.ToolCallID] || (adjacent && rest.ToolCallID == "")) {
				continue
			}
			adjacent = false
			out = append(out, rest)
		}
		return out
	}
	return nil
}

func joinPrunedMessages(system Message, kept []Message) []Message {
	result := make([]Message, 0, len(kept)+1)
	if system.Role == RoleSystem {
		result = append(result, system)
	}
	return append(result, kept...)
}
