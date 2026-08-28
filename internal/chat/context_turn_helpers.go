package chat

import (
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

func (s *Session) plainTurnCurrent(token OperationToken, turn uint64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.turnID == turn && s.tokenCurrentLocked(token)
}

func contextTurnMessages(messages []provider.Message, userText string) []provider.Message {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == provider.RoleUser && messages[index].Content == userText {
			return cloneContextMessages(messages[index:])
		}
	}
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == provider.RoleUser {
			return cloneContextMessages(messages[index:])
		}
	}
	return nil
}

// outputReserve mirrors internal/agent's helper of the same name - see its
// doc comment for why this does not itself shrink the prompt budget (that
// happens upstream, in config.EffectiveOutputTokens).
func outputReserve(maxTokens *int, level reasoning.Level) int {
	if maxTokens != nil && *maxTokens >= 0 {
		return *maxTokens
	}
	return reasoning.OutputReserveFloor(level)
}
