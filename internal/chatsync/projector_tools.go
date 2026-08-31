package chatsync

import (
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// Tool and subagent projections. Split out of projector.go to keep that file
// under the 500-line structural budget (.mivia/policy/go-structure.json).

func (p *Projector) projectTool(env Envelope, ev events.Event) []WireEvent {
	env.Block = ev.ToolCallID
	name := applyTruncation(&env, "name", ev.Name, BudgetShortField)
	callID := applyTruncation(&env, "tool_call_id", ev.ToolCallID, BudgetShortField)

	if ev.Kind == events.KindToolStart {
		var input string
		if shouldIncludeToolIO(p.opts) {
			input = redactText(ev.Input)
			input = applyTruncation(&env, "input", input, BudgetToolInput)
		} else {
			env.Redacted = append(env.Redacted, "input")
		}
		payload := &ToolStartedPayload{
			Envelope:   env,
			ToolCallID: callID,
			Name:       name,
			InputBytes: len(ev.Input),
			Input:      input,
		}
		return []WireEvent{p.nextWireEvent(TypeToolStarted, payload)}
	}

	var output string
	if shouldIncludeToolIO(p.opts) {
		output = redactText(ev.Output)
		output = applyTruncation(&env, "output", output, BudgetToolOutput)
	} else {
		env.Redacted = append(env.Redacted, "output")
	}
	detail := applyTruncation(&env, "detail", ev.Detail, BudgetShortField)
	payload := &ToolEndedPayload{
		Envelope:    env,
		ToolCallID:  callID,
		Name:        name,
		Status:      toolEndStatus(ev.Detail),
		OutputBytes: len(ev.Output),
		Detail:      detail,
		Output:      output,
	}
	return []WireEvent{p.nextWireEvent(TypeToolEnded, payload)}
}

func (p *Projector) projectSubagent(env Envelope, ev events.Event) []WireEvent {
	env.Block = ev.ToolCallID
	name := applyTruncation(&env, "name", ev.Name, BudgetShortField)
	callID := applyTruncation(&env, "tool_call_id", ev.ToolCallID, BudgetShortField)

	switch ev.Kind {
	case events.KindSubagentStart:
		var input string
		if shouldIncludeToolIO(p.opts) {
			input = redactText(ev.Input)
			input = applyTruncation(&env, "input", input, BudgetToolInput)
		} else {
			env.Redacted = append(env.Redacted, "input")
		}
		payload := &SubagentToolStartedPayload{
			Envelope:   env,
			ToolCallID: callID,
			Name:       name,
			InputBytes: len(ev.Input),
			Input:      input,
		}
		return []WireEvent{p.nextWireEvent(TypeSubagentToolStarted, payload)}
	case events.KindSubagentEnd:
		var output string
		if shouldIncludeToolIO(p.opts) {
			output = redactText(ev.Output)
			output = applyTruncation(&env, "output", output, BudgetToolOutput)
		} else {
			env.Redacted = append(env.Redacted, "output")
		}
		detail := applyTruncation(&env, "detail", ev.Detail, BudgetShortField)
		payload := &SubagentToolEndedPayload{
			Envelope:    env,
			ToolCallID:  callID,
			Name:        name,
			Status:      toolEndStatus(ev.Detail),
			OutputBytes: len(ev.Output),
			Detail:      detail,
			Output:      output,
		}
		return []WireEvent{p.nextWireEvent(TypeSubagentToolEnded, payload)}
	case events.KindSubagentHeartbeat:
		detail := applyTruncation(&env, "detail", ev.Detail, BudgetShortField)
		payload := &SubagentProgressPayload{
			Envelope: env,
			Detail:   detail,
		}
		return []WireEvent{p.nextWireEvent(TypeSubagentProgress, payload)}
	case events.KindSubagentDone:
		agentName := applyTruncation(&env, "name", ev.AgentName, BudgetShortField)
		status := applyTruncation(&env, "status", ev.Detail, BudgetShortField)
		payload := &SubagentEndedPayload{
			Envelope: env,
			Name:     agentName,
			Status:   status,
		}
		return []WireEvent{p.nextWireEvent(TypeSubagentEnded, payload)}
	default:
		return nil
	}
}

func toolEndStatus(detail string) string {
	if detail == "" {
		return "ok"
	}
	return detail
}
