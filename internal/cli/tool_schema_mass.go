package cli

import (
	"fmt"
	"strconv"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// schemaMass is the advertised tool-schema cost of one agent surface: the
// number every request pays before a single message is added.
type schemaMass struct {
	Advertised  int
	Tokens      int
	Deferred    int
	HeldTokens  int
	AgentName   string
	Publication string
}

// measureSchemaMass prices what a surface actually advertises, plus what the
// deferred tier is holding back. Without the second number, "deferred loading
// is enabled" is a claim with no evidence behind it; with it, the operator can
// see whether the tier split is worth its complexity on their configuration.
func measureSchemaMass(registry *tools.Registry, base *tools.Registry, plan toolTierPlan, agentName, publication string) schemaMass {
	mass := schemaMass{Deferred: len(plan.Candidates), AgentName: agentName, Publication: publication}
	if registry != nil {
		mass.Advertised = len(registry.List())
		mass.Tokens, _ = provider.EstimateToolSchemaCost(registry.OpenAITools())
	}
	if base != nil && len(plan.Candidates) > 0 {
		held := tools.NewRegistry()
		for _, candidate := range plan.Candidates {
			if tool, ok := base.Get(candidate.Name); ok {
				held.Register(tool)
			}
		}
		mass.HeldTokens, _ = provider.EstimateToolSchemaCost(held.OpenAITools())
	}
	return mass
}

// String renders the operator-facing one-liner used by /tools and diagnostics.
func (m schemaMass) String() string {
	line := fmt.Sprintf("%d tools advertised, ~%d schema tokens per request", m.Advertised, m.Tokens)
	if m.Deferred > 0 {
		line += fmt.Sprintf("; %d deferred (~%d tokens withheld until loaded)", m.Deferred, m.HeldTokens)
	}
	return line
}

// publishSchemaMass records the measurement on the session event bus when one
// is attached. It reuses the existing config-change kind rather than minting an
// event type for a number that is pure observation.
func publishSchemaMass(sess *chat.Session, mass schemaMass) {
	if sess == nil || sess.EventBus == nil {
		return
	}
	sess.EventBus.Publish(events.Event{
		Kind:      events.KindConfigChange,
		Timestamp: time.Now(),
		SessionID: sess.SessionID,
		Name:      "tool_schema_mass",
		Detail:    mass.String(),
		Metadata: map[string]string{
			"agent":                mass.AgentName,
			"publication":          mass.Publication,
			"tools_advertised":     strconv.Itoa(mass.Advertised),
			"schema_tokens":        strconv.Itoa(mass.Tokens),
			"tools_deferred":       strconv.Itoa(mass.Deferred),
			"deferred_held_tokens": strconv.Itoa(mass.HeldTokens),
		},
	})
}

// recordSchemaMass measures and records the session's current advertised
// schema mass. It takes state.mu itself; callers already holding it use
// recordSchemaMassLocked.
func recordSchemaMass(sess *chat.Session, state *agentSessionState, plan toolTierPlan, agentName, publication string) {
	if state == nil {
		publishSchemaMass(sess, measureSchemaMass(sess.Tools, nil, plan, agentName, publication))
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	recordSchemaMassLocked(sess, state, plan, agentName, publication)
}

func recordSchemaMassLocked(sess *chat.Session, state *agentSessionState, plan toolTierPlan, agentName, publication string) {
	mass := measureSchemaMass(sess.Tools, state.ToolBase, plan, agentName, publication)
	state.LastSchemaMass = mass
	publishSchemaMass(sess, mass)
}

// schemaMassSnapshot returns the last recorded measurement for display.
func (s *agentSessionState) schemaMassSnapshot() schemaMass {
	if s == nil {
		return schemaMass{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.LastSchemaMass
}
