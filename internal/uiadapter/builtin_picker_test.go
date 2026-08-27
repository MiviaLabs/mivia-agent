package uiadapter_test

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// TestDefaultAgentNameMatchesRootIdentity pins the two spellings of the
// compiled root identity together: the UI picker constant and the config
// reservation must name the same agent.
func TestDefaultAgentNameMatchesRootIdentity(t *testing.T) {
	if ports.DefaultAgentName != config.RootAgentName {
		t.Fatalf("ports.DefaultAgentName = %q, want config.RootAgentName %q",
			ports.DefaultAgentName, config.RootAgentName)
	}
}

// TestPickerOffersRootUnconditionallyAndRestoresRoot pins the picker wiring:
// the compiled root entry is offered even when the registry is non-empty, and
// selecting it restores the root surface (Selected == nil).
func TestPickerOffersRootUnconditionallyAndRestoresRoot(t *testing.T) {
	comp := &nullCompleter{}
	res := &config.Resolved{ProviderName: "test", Model: "m1"}
	sess := chat.NewSession(res, comp)
	reg := agents.NewRegistry()
	for _, a := range []agents.ResolvedAgent{
		{Name: "reviewer", Description: "code reviewer", SystemPrompt: "you are a reviewer"},
		{Name: "general-purpose", Description: "built-in", SystemPrompt: "built-in general-purpose prompt"},
	} {
		if err := reg.Publish(a); err != nil {
			t.Fatal(err)
		}
	}
	state := &cliagents.AgentSessionState{Registry: reg, ToolBase: tools.NewRegistry()}
	runner := uiadapter.NewCommandRunner(sess, res, state)

	out := runner.Run(context.Background(), "agents", "")
	choices := out.AgentChoices
	if len(choices) != 3 || choices[0] != ports.DefaultAgentName {
		t.Fatalf("AgentChoices = %v, want [%s reviewer general-purpose] with the root entry first",
			choices, ports.DefaultAgentName)
	}

	// Selecting the built-in registry member succeeds.
	if out := runner.SelectAgent(context.Background(), "general-purpose"); out.Err != "" {
		t.Fatalf("SelectAgent(general-purpose) error: %s", out.Err)
	}
	if state.Selected == nil || state.Selected.Name != "general-purpose" {
		t.Fatalf("Selected = %+v, want general-purpose", state.Selected)
	}

	// Selecting the root entry restores the root surface.
	if out := runner.SelectAgent(context.Background(), ports.DefaultAgentName); out.Err != "" {
		t.Fatalf("SelectAgent(root) error: %s", out.Err)
	}
	if state.Selected != nil {
		t.Fatalf("root selection must restore Selected == nil, got %q", state.Selected.Name)
	}
}

// TestPickerOffersRootOnEmptyRegistry pins the defensive fallback: a nil or
// empty registry still offers the root entry instead of an empty picker.
func TestPickerOffersRootOnEmptyRegistry(t *testing.T) {
	comp := &nullCompleter{}
	res := &config.Resolved{ProviderName: "test", Model: "m1"}
	sess := chat.NewSession(res, comp)
	state := &cliagents.AgentSessionState{Registry: agents.NewRegistry(), ToolBase: tools.NewRegistry()}
	runner := uiadapter.NewCommandRunner(sess, res, state)

	out := runner.Run(context.Background(), "agents", "")
	if len(out.AgentChoices) != 1 || out.AgentChoices[0] != ports.DefaultAgentName {
		t.Fatalf("AgentChoices = %v, want [%s]", out.AgentChoices, ports.DefaultAgentName)
	}
}
