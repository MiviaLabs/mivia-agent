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
		ContextAccounting: provider.ContextAccountingFor(binding.Completer),
		Revision:          contextstate.Revision{}, Binding: captureBindingRevision(binding), WorktreeInstance: instance, Policy: policy,
		// The session-owned core-memory frame rides on a named user message
		// right after the system prompt; compaction must keep it whole, so
		// every planner invocation preserves that Name (BUG 3).
		PreserveNames: []string{MemoryContextMessageName},
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

// validateRestoredMessages validates a durable checkpoint's message list for
// adoption into the session. The checkpoint legitimately carries the
// session-owned core-memory frame (a named user message), which the provider
// pairing rule would reject; the frame is exempt from the Name check the same
// way the planner exempts preserved names, so a frame-bearing checkpoint is
// restored instead of refused. Every other shape defect still fails closed.
func validateRestoredMessages(messages []provider.Message) error {
	return provider.ValidateToolPairing(maskMemoryFrameNames(messages))
}

// maskMemoryFrameNames returns a shallow clone with the session-owned
// core-memory frame's Name cleared for pairing validation. Only a user-role
// message with the sentinel Name is masked; a checkpoint that carries the
// Name on any other role stays a hard shape error.
func maskMemoryFrameNames(messages []provider.Message) []provider.Message {
	output := make([]provider.Message, len(messages))
	copy(output, messages)
	for index := range output {
		if output[index].Role == provider.RoleUser && output[index].Name == MemoryContextMessageName {
			output[index].Name = ""
		}
	}
	return output
}

// memoryFrameContent returns the rendered content of the session-owned
// core-memory frame in messages, or "" when the checkpoint carries none. It
// mirrors the frame into s.memoryContext so a later /clear or surface
// publication re-seeds from the same block the restored session runs on.
func memoryFrameContent(messages []provider.Message) string {
	for _, message := range messages {
		if isMemoryContextMessage(message) {
			return message.Content
		}
	}
	return ""
}
