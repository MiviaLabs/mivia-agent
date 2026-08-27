package cliagents

import (
	"fmt"
	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

func FormatSessionAgentStatus(state *AgentSessionState, sess *chat.Session) string {
	identity := SessionIdentity(sess, state, sess.CurrentModelGeneration())
	if identity == nil {
		return ""
	}
	return fmt.Sprintf(" agent=%s source=%s generation=%d", identity.DefinitionName, identity.DefinitionSource, identity.ModelGeneration)
}

// SessionIdentity resolves the current agent identity (name and source) for
// a session generation. Shared with internal/legacytui's TUI event handling.
func SessionIdentity(sess *chat.Session, state *AgentSessionState, generation uint64) *events.Identity {
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

func InstallSessionIdentity(sess *chat.Session, state *AgentSessionState) {
	if sess == nil {
		return
	}
	sess.SetEventIdentityFactory(func(generation uint64) *events.Identity {
		return SessionIdentity(sess, state, generation)
	})
}

// RoutedIdentity builds an events.Identity for a routed agent invocation.
func RoutedIdentity(definition agents.ResolvedAgent, instanceID string, generation uint64) *events.Identity {
	identity, err := events.NewIdentity(definition.Name, string(definition.Provenance.Source), instanceID, generation)
	if err != nil {
		return nil
	}
	return &identity
}
