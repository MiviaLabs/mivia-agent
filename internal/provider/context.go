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
	if maxTokens <= 0 || len(msgs) <= 1 {
		return msgs
	}
	total := MessagesTokens(msgs)
	if total <= maxTokens {
		return msgs
	}

	// Find system prompt (always kept).
	sysMsg := Message{}
	sysOffset := 0
	if len(msgs) > 0 && msgs[0].Role == RoleSystem {
		sysMsg = msgs[0]
		sysOffset = 1
	}

	// Group messages into turns.
	// A turn starts with a user message and includes all following messages
	// up to (but not including) the next user message.
	// All indices are relative to sysOffset (i.e. index 0 = msgs[sysOffset]).
	// Non-user messages that precede the first user message are grouped into
	// a synthetic "preamble" turn so they are not silently dropped.
	type turn struct {
		start  int // relative index
		end    int // exclusive relative index
		tokens int
	}
	var turns []turn
	currentStart := -1
	currentTokens := 0
	tail := msgs[sysOffset:]
	for relIdx, m := range tail {
		if m.Role == RoleUser && currentStart >= 0 {
			turns = append(turns, turn{start: currentStart, end: relIdx, tokens: currentTokens})
			currentStart = relIdx
			currentTokens = MessageTokens(m)
		} else if m.Role == RoleUser {
			currentStart = relIdx
			currentTokens = MessageTokens(m)
		} else {
			if currentStart < 0 {
				currentStart = relIdx // preamble: first non-user before any user message
			}
			currentTokens += MessageTokens(m)
		}
	}
	if currentStart >= 0 {
		turns = append(turns, turn{start: currentStart, end: len(tail), tokens: currentTokens})
	}

	// Budget for non-system messages.
	// Clamp to at least 1 so budget calculations stay valid.
	budget := maxTokens - MessageTokens(sysMsg)
	if budget < 1 {
		budget = 1
	}

	// Walk turns from the end, accumulating until we hit budget.
	keepStart := len(tail) // relative index where kept messages start
	running := 0
	for i := len(turns) - 1; i >= 0; i-- {
		running += turns[i].tokens
		if running <= budget {
			keepStart = turns[i].start
		} else if i == len(turns)-1 {
			// The most recent turn itself exceeds budget — keep it anyway.
			keepStart = turns[i].start
			break
		} else {
			break
		}
	}

	// Build result: system + tail[keepStart:].
	var result []Message
	if sysOffset > 0 {
		result = make([]Message, 0, len(tail)-keepStart+1)
		result = append(result, sysMsg)
	} else {
		result = make([]Message, 0, len(tail)-keepStart)
	}
	result = append(result, tail[keepStart:]...)
	return result
}
