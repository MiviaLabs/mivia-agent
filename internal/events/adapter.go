package events

import "time"

// NewEventFromAgentParts creates an events.Event from the individual fields
// of an agent.Event, without importing the agent package.
func NewEventFromAgentParts(kind Kind, toolCallID, name, detail, content, input, output string) Event {
	return Event{
		Kind:       kind,
		Timestamp:  time.Now(),
		ToolCallID: toolCallID,
		Name:       name,
		Detail:     detail,
		Content:    content,
		Input:      input,
		Output:     output,
	}
}
