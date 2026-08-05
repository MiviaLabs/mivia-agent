package chat

import "github.com/MiviaLabs/mivia-agent/internal/provider"

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

func outputReserve(maxTokens *int) int {
	if maxTokens == nil || *maxTokens < 0 {
		return 0
	}
	return *maxTokens
}
