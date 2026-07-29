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
	return joinPrunedMessages(system, tail, keepTurnStart(groupMessageTurns(tail), len(tail), budget))
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

func joinPrunedMessages(system Message, tail []Message, keepStart int) []Message {
	result := make([]Message, 0, len(tail)-keepStart+1)
	if system.Role == RoleSystem {
		result = append(result, system)
	}
	return append(result, tail[keepStart:]...)
}
