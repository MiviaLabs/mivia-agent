package clichat

// chat_slash_handlers_handle_agent_test.go covers the
// handleSlashAgent branches in chat_slash_handlers.go that the
// broader slash-handler tests do not drive individually.

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

func TestHandleSlashAgentBranches(t *testing.T) {
	// Build a minimal state with a populated agent registry.
	reg := agents.NewRegistry()
	_ = reg.Publish(agents.ResolvedAgent{Name: "alpha"})
	state := &AgentSessionState{
		Registry: reg,
		ToolBase: tools.NewRegistry(),
	}
	sess := chat.NewSession(&config.Resolved{ProviderName: "p", Model: "m"}, nil)
	// handleSlashAgent with no fields: print current agent, return
	// (true, false, nil). Lines 22-25.
	ok, _, err := handleSlashAgent(nil, sess, &config.Resolved{ProviderName: "p", Model: "m"}, nil, state)
	if !ok || err != nil {
		t.Errorf("handleSlashAgent(no fields) = (%v, %v); want (true, nil)", ok, err)
	}
}
