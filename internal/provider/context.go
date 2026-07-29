// Package provider implements LLM chat adapters for mivia.
package provider

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
