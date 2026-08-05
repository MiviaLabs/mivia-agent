package chat

import (
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// Pure helpers that shape a context-enabled turn's input. Split from
// context_integration.go, which the merge with master pushed past the
// file-size gate; these three have no Session receiver and no state.

func cloneContextMessages(messages []provider.Message) []provider.Message {
	output := make([]provider.Message, len(messages))
	copy(output, messages)
	for index := range output {
		output[index].ToolCalls = append([]provider.ToolCall(nil), messages[index].ToolCalls...)
	}
	return output
}

func prepareInputForContext(messages []provider.Message, budget int, maxTokens *int, binding ModelBinding, principal contextstate.Principal, policy contextstate.PolicySnapshot, instance contextstate.WorktreeInstance) contextmgr.PrepareInput {
	return contextmgr.PrepareInput{
		Messages: messages, Budget: budget, OutputReserve: outputReserve(maxTokens),
		CurrentObjective: latestUserMessage(messages), Principal: principal,
		Revision: contextstate.Revision{}, Binding: captureBindingRevision(binding), WorktreeInstance: instance, Policy: policy,
	}
}

func latestUserMessage(messages []provider.Message) string {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == provider.RoleUser {
			return messages[index].Content
		}
	}
	return ""
}
