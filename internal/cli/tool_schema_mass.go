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
//
// Advertised/Tokens price the pinned wire snapshot (plan
// tools-advertising/01): core tier PLUS every deferred candidate, since
// admission now changes execution authority only, never what is advertised.
// Locked/LockedTokens are the subset of that snapshot which is authorized and
// visible but not yet admitted for execution - "deferred" no longer means
// "withheld", it means "locked until loaded with load_tools". LockedTokens
// prices the same shortened (one-line) description advertisedToolSpecs
// actually ships for a locked tool, not its full Description(), so it stays
// consistent with Tokens.
type schemaMass struct {
	Advertised   int
	Tokens       int
	Locked       int
	LockedTokens int
	AgentName    string
	Publication  string
}

// measureSchemaMass prices what a surface actually advertises, plus how much
// of that is locked (authorized and visible, not yet admitted for execution).
// Without the second number, "deferred loading is enabled" is a claim with no
// evidence behind it; with it, the operator can see whether the tier split is
// worth its complexity on their configuration. admitted names the tools
// already published for execution: they are still counted in Tokens (they are
// still advertised) but excluded from Locked/LockedTokens, since they are no
// longer locked.
func measureSchemaMass(advertised []provider.ToolSpec, base *tools.Registry, plan toolTierPlan, admitted []string, agentName, publication string) schemaMass {
	mass := schemaMass{AgentName: agentName, Publication: publication}
	mass.Advertised = len(advertised)
	mass.Tokens, _ = provider.EstimateToolSchemaCost(advertised)
	live := make(map[string]struct{}, len(admitted))
	for _, name := range admitted {
		live[name] = struct{}{}
	}
	locked := tools.NewRegistry()
	for _, candidate := range plan.Candidates {
		if _, loaded := live[candidate.Name]; loaded {
			continue
		}
		mass.Locked++
		if base == nil {
			continue
		}
		if tool, ok := base.Get(candidate.Name); ok {
			locked.Register(shortDescTool{tool})
		}
	}
	if len(locked.List()) > 0 {
		mass.LockedTokens, _ = provider.EstimateToolSchemaCost(locked.OpenAITools())
	}
	return mass
}

// String renders the operator-facing one-liner used by /tools and diagnostics.
func (m schemaMass) String() string {
	line := fmt.Sprintf("%d tools advertised, ~%d schema tokens per request", m.Advertised, m.Tokens)
	if m.Locked > 0 {
		line += fmt.Sprintf("; %d locked (~%d tokens) until loaded", m.Locked, m.LockedTokens)
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
			"agent":            mass.AgentName,
			"publication":      mass.Publication,
			"tools_advertised": strconv.Itoa(mass.Advertised),
			"schema_tokens":    strconv.Itoa(mass.Tokens),
			"tools_locked":     strconv.Itoa(mass.Locked),
			"locked_tokens":    strconv.Itoa(mass.LockedTokens),
		},
	})
}

// recordSchemaMass measures and records the session's current advertised
// schema mass. It takes state.mu itself; callers already holding it use
// recordSchemaMassLocked.
// admitted is passed in rather than read back off the session: the admission
// path records this measurement before it commits the new admitted set, so
// asking the session would price the surface it just replaced.
func recordSchemaMass(sess *chat.Session, state *AgentSessionState, plan toolTierPlan, admitted []string, agentName, publication string) {
	if state == nil {
		publishSchemaMass(sess, measureSchemaMass(sess.AdvertisedToolSpecs(), nil, plan, admitted, agentName, publication))
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	recordSchemaMassLocked(sess, state, plan, admitted, agentName, publication)
}

func recordSchemaMassLocked(sess *chat.Session, state *AgentSessionState, plan toolTierPlan, admitted []string, agentName, publication string) {
	mass := measureSchemaMass(sess.AdvertisedToolSpecs(), state.ToolBase, plan, admitted, agentName, publication)
	state.LastSchemaMass = mass
	publishSchemaMass(sess, mass)
}

// SchemaMassSnapshot returns the last recorded measurement for display.
func (s *AgentSessionState) SchemaMassSnapshot() schemaMass {
	if s == nil {
		return schemaMass{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.LastSchemaMass
}
