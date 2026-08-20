package demoharness

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// harnessAgents is the ports.AgentSettings adapter.
type harnessAgents struct{ *Harness }

func (a harnessAgents) Agents() []ports.AgentView {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]ports.AgentView, len(a.settingsAgents))
	copy(out, a.settingsAgents)
	return out
}

func (a harnessAgents) Apply(_ context.Context, _ ports.Scope, e ports.AgentEdit) (ports.SaveHandle, error) {
	return a.newSaveHandle(func() error { return a.applyAgent(e) }), nil
}

func (h *Harness) findAgent(name string) int {
	for i := range h.settingsAgents {
		if h.settingsAgents[i].Name == name {
			return i
		}
	}
	return -1
}

func (h *Harness) applyAgent(e ports.AgentEdit) error {
	switch v := e.(type) {
	case ports.UpsertAgent:
		if i := h.findAgent(v.Agent.Name); i >= 0 {
			h.settingsAgents[i] = v.Agent
			return nil
		}
		h.settingsAgents = append(h.settingsAgents, v.Agent)
	case ports.RemoveAgent:
		if v.Name == ports.DefaultAgentName {
			return fmt.Errorf("the default agent %q cannot be removed", ports.DefaultAgentName)
		}
		i := h.findAgent(v.Name)
		if i < 0 {
			return fmt.Errorf("agent %q not found", v.Name)
		}
		h.settingsAgents = append(h.settingsAgents[:i], h.settingsAgents[i+1:]...)
	default:
		return fmt.Errorf("unknown agent edit %T", e)
	}
	return nil
}
