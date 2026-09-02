package clichat

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// An operator's denylist must survive the baseline-messaging injection.
//
// post_message is re-registered into the already-scoped subagent registry so
// an agent gets it without listing it. The opt-out consulted the agent's own
// DisallowedTools only, so an operator's mandatory_tool_denylist entry was
// stripped by applyToolPolicy, excluded by AuthorizedAgentTools, dropped by
// ScopedRegistry - and then put straight back here.
//
// This is the same defect shape as the MCP one, one call away: a producer
// that re-adds a name AFTER the policy has run and consults the wrong list.
// EffectiveDenylist is the one that carries the operator's additions;
// DisallowedTools is the agent file's own.
func TestBaselineMessagingHonoursTheOperatorDenylist(t *testing.T) {
	full := tools.NewRegistry()
	full.Register(msgProbe{name: toolPostMessage})
	scoped := tools.NewRegistry() // as ScopedRegistry left it: post_message denied out

	agent := agents.ResolvedAgent{
		Name: "worker",
		// The agent file itself said nothing about post_message; the OPERATOR
		// denied it, which is what resolve records in EffectiveDenylist.
		DisallowedTools:   nil,
		EffectiveDenylist: []string{toolPostMessage},
	}

	injectBaselineMessaging(full, scoped, config.SubagentConfig{}, messagingDisallowed(agent))

	if _, ok := scoped.Get(toolPostMessage); ok {
		t.Error("post_message was re-injected into a scoped registry after the " +
			"operator denied it: applyToolPolicy stripped it, AuthorizedAgentTools " +
			"excluded it, ScopedRegistry dropped it, and the baseline injection " +
			"put it straight back")
	}
}

// An agent that opts out through its OWN disallowed_tools must still opt out.
func TestBaselineMessagingStillHonoursTheAgentsOwnOptOut(t *testing.T) {
	full := tools.NewRegistry()
	full.Register(msgProbe{name: toolPostMessage})
	scoped := tools.NewRegistry()

	agent := agents.ResolvedAgent{Name: "worker", DisallowedTools: []string{toolPostMessage}}
	injectBaselineMessaging(full, scoped, config.SubagentConfig{}, messagingDisallowed(agent))

	if _, ok := scoped.Get(toolPostMessage); ok {
		t.Error("the agent file's own opt-out stopped working")
	}
}

// ...and an agent that denied nothing still GETS post_message, which is the
// whole point of the injection.
func TestBaselineMessagingStillInjectsWhenNothingIsDenied(t *testing.T) {
	full := tools.NewRegistry()
	full.Register(msgProbe{name: toolPostMessage})
	scoped := tools.NewRegistry()

	agent := agents.ResolvedAgent{Name: "worker"}
	injectBaselineMessaging(full, scoped, config.SubagentConfig{}, messagingDisallowed(agent))

	if _, ok := scoped.Get(toolPostMessage); !ok {
		t.Error("post_message was not injected for an agent that denied nothing")
	}
}

type msgProbe struct{ name string }

func (m msgProbe) Name() string               { return m.name }
func (m msgProbe) Description() string        { return m.name }
func (m msgProbe) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (m msgProbe) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}
