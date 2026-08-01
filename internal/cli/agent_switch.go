package cli

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// classicAgentState is the root agent context for the classic REPL/one-shot
// chat path. The TUI stores the same pointer on tuiModel.agentState.
var classicAgentState *agentSessionState

// agentSessionState is the mid-session mutable agent context. Startup and
// /agent switch share this so model-switch rebuilds keep the selected agent.
type agentSessionState struct {
	Global             config.AgentsGlobal
	Selected           *agents.ResolvedAgent
	AllowProjectSkills bool
	Registry           *agents.AgentRegistry
	WorkspaceRoot      string
	// ToolBase is the post-dispatcher, pre-scope registry for re-scoping.
	// Nil when tools are off.
	ToolBase *tools.Registry
}

func (s *agentSessionState) context() agentSessionContext {
	if s == nil {
		return agentSessionContext{}
	}
	return agentSessionContext{
		Global:             s.Global,
		Selected:           s.Selected,
		Registry:           s.Registry,
		AllowProjectSkills: s.AllowProjectSkills,
	}
}

// agentListRow is one selectable entry for the /agent dialog and listings.
type agentListRow struct {
	Name        string
	Description string
	Current     bool
}

// agentListRows builds ordered rows from a registry. Pure; unit-tested without TUI.
func agentListRows(reg *agents.AgentRegistry, current string) []agentListRow {
	if reg == nil {
		return nil
	}
	current = strings.TrimSpace(current)
	names := reg.Names()
	out := make([]agentListRow, 0, len(names))
	for _, name := range names {
		a, ok := reg.Get(name)
		if !ok {
			continue
		}
		out = append(out, agentListRow{
			Name:        a.Name,
			Description: a.Description,
			Current:     a.Name == current,
		})
	}
	return out
}

func currentAgentName(state *agentSessionState) string {
	if state == nil || state.Selected == nil {
		return ""
	}
	return state.Selected.Name
}

func formatAgentAvailable(reg *agents.AgentRegistry) string {
	if reg == nil || reg.Len() == 0 {
		return "(none)"
	}
	return strings.Join(reg.Names(), ", ")
}

func formatAgentSet(name string) string {
	return "agent set to " + name
}

func formatAgentCurrent(name string, reg *agents.AgentRegistry) string {
	if name == "" {
		name = "(compiled default)"
	}
	return fmt.Sprintf("current agent=%s\nusage: /agent <name>\navailable: %s", name, formatAgentAvailable(reg))
}

// applySessionAgent switches the root agent for the idle session. busy is the
// TUI waiting flag; active turns and switch guards are checked on sess.
// It reuses ToolBase for re-scope and rebuilds the dispatcher like model switch.
func applySessionAgent(sess *chat.Session, res *config.Resolved, state *agentSessionState, name string, busy bool) error {
	if sess == nil || state == nil {
		return fmt.Errorf("agent switch requires a session and agent state")
	}
	if busy || sess.HasActiveTurn() {
		return fmt.Errorf("finish current work first")
	}
	if err := sess.CheckSwitchAllowed(); err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("agent name is empty")
	}
	if state.Registry == nil {
		return fmt.Errorf("no agents loaded")
	}
	selected, err := agents.Select(state.Registry, name)
	if err != nil {
		return err
	}
	// Update selection first so prompt/steps apply even when tools are off.
	sel := selected
	state.Selected = &sel
	applySelectedAgentPrompt(sess, res, state.Selected)

	if sess.Tools == nil || state.ToolBase == nil {
		return nil
	}
	return rebuildAgentScopedDispatcher(sess, res, state)
}

// rebuildAgentScopedDispatcher rebuilds tools from ToolBase, applies root
// agent scope, then builds the dispatcher from the scoped registry. Scope is
// applied BEFORE dispatcher construction so the dispatcher and sess.Tools
// agree — a tool absent from sess.Tools is also absent from the dispatcher's
// executable registry (INV-AG-29 execution denial).
func rebuildAgentScopedDispatcher(sess *chat.Session, res *config.Resolved, state *agentSessionState) error {
	binding := sess.CurrentBinding()
	if binding.Completer == nil {
		return fmt.Errorf("dispatcher: nil completer")
	}
	root := state.WorkspaceRoot
	if root == "" {
		root = "."
	}
	skillReg, warnings, err := loadSessionSkills(root, state.AllowProjectSkills)
	if err != nil {
		return fmt.Errorf("load skills: %w", err)
	}
	warnSkillLoad(warnings)
	skillReg = filterSkillRegistryForGate(skillReg, state.AllowProjectSkills)
	skillScope := skillScopeFromAgent(state.Selected)

	// Start from the pre-scope base so switching to a wider agent regains tools.
	// Apply root agent scope BEFORE building the dispatcher so the dispatcher
	// captures a scoped registry. This keeps the dispatcher and sess.Tools in
	// agreement (INV-AG-29 execution denial).
	sess.Tools = state.ToolBase.CloneForGenerationExcluding("ledger_read", "list_run_events")
	applyRootAgentScope(sess, state.Selected, state.Global.MandatoryToolDenylistAdditions)
	cfg := config.SubagentConfig{}
	if res != nil {
		cfg = res.Subagents
	}
	dispatcher, err := NewSessionDispatcher(SessionDispatcherOpts{
		Registry:           sess.Tools,
		Completer:          binding.Completer,
		Model:              binding.Model,
		Config:             cfg,
		ToolResultCapBytes: sess.MaxToolResultChars,
		MaxContextTokens:   sess.PromptBudget(),
		MaxTokens:          sess.MaxTokens,
		Budget:             sess.PromptBudget,
		SkillReg:           skillReg,
		SkillScope:         skillScope,
		AgentRegistry:      state.Registry,
	})
	if err != nil {
		return fmt.Errorf("dispatcher: %w", err)
	}
	sess.SetDispatcher(dispatcher)
	sess.SetBindingSkillRegistry(filterSkillsForScope(skillReg, skillScope))
	return nil
}
