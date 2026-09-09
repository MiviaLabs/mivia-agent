package clichat

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
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

// TestRegisterMessagingToolsHonoursTheOperatorDenylist is the root-session
// registrar itself. run_messages and send_to_task register through
// RegisterSessionTool, which refuses an operator-denied name - but
// post_message is deliberately not a PrivilegedTool, cannot use that gate,
// and so was registered unconditionally: the operator's guardrail existed in
// config and nowhere else, and the name rode the authority registry into
// every plain multi_step and skill subagent, where it ran prompt-free.
func TestRegisterMessagingToolsHonoursTheOperatorDenylist(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()

	deniedD := runtime.New(runtime.Policy{})
	t.Cleanup(func() { deniedD.Close() })
	denied := tools.NewRegistry()
	if err := registerMessagingTools(deniedD, denied, config.SubagentConfig{}, repo, nil, []string{toolPostMessage}); err != nil {
		t.Fatalf("registerMessagingTools: %v", err)
	}
	if _, ok := denied.Get(toolPostMessage); ok {
		t.Error("post_message was registered although the operator denied it - " +
			"the guardrail must be absolute at root, including the registrar " +
			"that owns the name")
	}
	if _, ok := denied.Get("run_messages"); !ok {
		t.Error("run_messages went missing; only the denied name may be skipped")
	}

	openD := runtime.New(runtime.Policy{})
	t.Cleanup(func() { openD.Close() })
	open := tools.NewRegistry()
	if err := registerMessagingTools(openD, open, config.SubagentConfig{}, repo, nil, nil); err != nil {
		t.Fatalf("registerMessagingTools: %v", err)
	}
	if _, ok := open.Get(toolPostMessage); !ok {
		t.Error("post_message was not registered for a session that denied nothing")
	}
}

type msgProbe struct{ name string }

func (m msgProbe) Name() string               { return m.name }
func (m msgProbe) Description() string        { return m.name }
func (m msgProbe) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (m msgProbe) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}
