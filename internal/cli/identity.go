package cli

import (
	"fmt"
	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

func formatSessionAgentStatus(state *agentSessionState, sess *chat.Session) string {
	identity := sessionIdentity(sess, state, sess.CurrentModelGeneration())
	if identity == nil {
		return ""
	}
	return fmt.Sprintf(" agent=%s source=%s generation=%d", identity.DefinitionName, identity.DefinitionSource, identity.ModelGeneration)
}

func currentAgentDisplayName(state *agentSessionState) string {
	if state == nil {
		return "root fallback"
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.Selected == nil {
		return "root fallback"
	}
	return state.Selected.Name
}

func currentAgentDisplaySource(state *agentSessionState) string {
	if state == nil {
		return "compiled"
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.Selected == nil {
		return "compiled"
	}
	return string(state.Selected.Provenance.Source)
}

func sessionIdentity(sess *chat.Session, state *agentSessionState, generation uint64) *events.Identity {
	if sess == nil || generation == 0 {
		return nil
	}
	name, source := "root fallback", "compiled"
	if state != nil {
		state.mu.Lock()
		defer state.mu.Unlock()
	}
	if state != nil && state.Selected != nil {
		name = state.Selected.Name
		source = string(state.Selected.Provenance.Source)
	}
	identity, err := events.NewIdentity(name, source, sess.SessionID, generation)
	if err != nil {
		return nil
	}
	return &identity
}

func installSessionIdentity(sess *chat.Session, state *agentSessionState) {
	if sess == nil {
		return
	}
	sess.SetEventIdentityFactory(func(generation uint64) *events.Identity {
		return sessionIdentity(sess, state, generation)
	})
}

func routedIdentity(definition agents.ResolvedAgent, instanceID string, generation uint64) *events.Identity {
	identity, err := events.NewIdentity(definition.Name, string(definition.Provenance.Source), instanceID, generation)
	if err != nil {
		return nil
	}
	return &identity
}
