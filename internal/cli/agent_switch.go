package cli

import (
	"fmt"
	"strings"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// classicAgentState is the root agent context for the classic REPL/one-shot
// chat path. The TUI stores the same pointer on tuiModel.agentState.
var classicAgentState *agentSessionState

// agentSessionState is the mid-session mutable agent context. Startup and
// /agent switch share this so model-switch rebuilds keep the selected agent.
type agentSessionState struct {
	mu                 sync.Mutex
	Global             config.AgentsGlobal
	Selected           *agents.ResolvedAgent
	AllowProjectSkills bool
	Registry           *agents.AgentRegistry
	WorkspaceRoot      string
	// ToolBase is the post-dispatcher, pre-scope registry for re-scoping.
	// Nil when tools are off.
	ToolBase         *tools.Registry
	BaselinePrompt   string
	BaselineMaxSteps int
	BaselineCaptured bool
}

func (s *agentSessionState) context() agentSessionContext {
	if s == nil {
		return agentSessionContext{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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
	if state == nil {
		return ""
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.Selected == nil {
		return ""
	}
	return state.Selected.Name
}

func formatAgentAvailable(reg *agents.AgentRegistry) string {
	if reg == nil || reg.Len() == 0 {
		return "(none)"
	}
	rows := make([]string, 0, reg.Len())
	for _, name := range reg.Names() {
		a, ok := reg.Get(name)
		if ok {
			rows = append(rows, name+"("+string(a.Provenance.Source)+")")
		}
	}
	return strings.Join(rows, ", ")
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
	// Selection and all session-owned surfaces are one logical transaction.
	// Candidate construction is intentionally inside this lock so two /agent
	// requests cannot publish different surfaces under one selected name.
	state.mu.Lock()
	defer state.mu.Unlock()
	if busy {
		return fmt.Errorf("finish current work first")
	}
	release, err := sess.BeginSurfaceSwitch()
	if err != nil {
		return err
	}
	defer release()
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
	if !state.BaselineCaptured {
		state.BaselinePrompt, state.BaselineMaxSteps = sess.AgentSettings()
		state.BaselineCaptured = true
	}
	prompt, maxSteps := selectedAgentSettings(&selected, state)
	var candidate *agentSurface
	if sess.Tools != nil && state.ToolBase != nil {
		candidate, err = buildAgentScopedSurface(sess, res, state, &selected)
		if err != nil {
			return err
		}
	}
	// Commit selection and every session-owned surface only after all candidate
	// construction and validation has succeeded.
	sel := selected
	state.Selected = &sel
	if res != nil {
		res.SystemPrompt = prompt
	}
	if candidate == nil {
		sess.SetAgentSettings(prompt, maxSteps)
		return nil
	}
	sess.PublishAgentSurface(prompt, maxSteps, candidate.registry, candidate.dispatcher, candidate.skillReg)
	return nil
}

func selectedAgentSettings(selected *agents.ResolvedAgent, state *agentSessionState) (string, int) {
	if selected != nil && strings.TrimSpace(selected.SystemPrompt) != "" {
		return selected.SystemPrompt, selectedMaxTurns(selected, state.BaselineMaxSteps)
	}
	if selected != nil && selected.MaxTurns != nil {
		return state.BaselinePrompt, *selected.MaxTurns
	}
	return state.BaselinePrompt, state.BaselineMaxSteps
}

func selectedMaxTurns(selected *agents.ResolvedAgent, baseline int) int {
	if selected != nil && selected.MaxTurns != nil {
		return *selected.MaxTurns
	}
	return baseline
}

type agentSurface struct {
	registry   *tools.Registry
	dispatcher *runtime.Dispatcher
	skillReg   *skills.Registry
}

// rebuildAgentScopedDispatcher rebuilds tools from ToolBase, applies root
// agent scope, then builds the dispatcher from the scoped registry. Scope is
// applied BEFORE dispatcher construction so the dispatcher and sess.Tools
// agree — a tool absent from sess.Tools is also absent from the dispatcher's
// executable registry (INV-AG-29 execution denial).
func rebuildAgentScopedDispatcher(sess *chat.Session, res *config.Resolved, state *agentSessionState) error {
	selected := state.Selected
	candidate, err := buildAgentScopedSurface(sess, res, state, selected)
	if err != nil {
		return err
	}
	prompt, maxSteps := sess.AgentSettings()
	sess.PublishAgentSurface(prompt, maxSteps, candidate.registry, candidate.dispatcher, candidate.skillReg)
	return nil
}

func buildAgentScopedSurface(sess *chat.Session, res *config.Resolved, state *agentSessionState, selected *agents.ResolvedAgent) (*agentSurface, error) {
	binding := sess.CurrentBinding()
	if binding.Completer == nil {
		return nil, fmt.Errorf("dispatcher: nil completer")
	}
	root := state.WorkspaceRoot
	if root == "" {
		root = "."
	}
	skillReg, warnings, err := loadSessionSkills(root, state.AllowProjectSkills)
	if err != nil {
		return nil, fmt.Errorf("load skills: %w", err)
	}
	warnSkillLoad(warnings)
	skillReg = filterSkillRegistryForGate(skillReg, state.AllowProjectSkills)
	skillScope := skillScopeFromAgent(selected)

	// Start from the pre-scope base so switching to a wider agent regains tools.
	// Apply root agent scope BEFORE building the dispatcher so the dispatcher
	// captures a scoped registry. This keeps the dispatcher and sess.Tools in
	// agreement (INV-AG-29 execution denial).
	registry := state.ToolBase.CloneForGenerationExcluding("ledger_read", "list_run_events")
	registry = scopedRootRegistry(registry, selected, state.Global.MandatoryToolDenylistAdditions)
	cfg := config.SubagentConfig{}
	var modelCatalog []config.ProviderModelGroup
	if res != nil {
		cfg = res.Subagents
		modelCatalog = res.ModelCatalog()
	}
	dispatcher, err := NewSessionDispatcher(SessionDispatcherOpts{
		Registry:            registry,
		Completer:           binding.Completer,
		Model:               binding.Model,
		ProviderName:        binding.ProviderName,
		ModelGeneration:     binding.ModelGeneration,
		ModelGenerationFunc: sess.CurrentModelGeneration,
		ModelCatalog:        modelCatalog,
		Config:              cfg,
		ToolResultCapBytes:  sess.MaxToolResultChars,
		MaxContextTokens:    sess.PromptBudget(),
		MaxTokens:           sess.MaxTokens,
		Budget:              sess.PromptBudget,
		SkillReg:            skillReg,
		SkillScope:          skillScope,
		AgentRegistry:       state.Registry,
	})
	if err != nil {
		return nil, fmt.Errorf("dispatcher: %w", err)
	}
	return &agentSurface{registry: registry, dispatcher: dispatcher, skillReg: filterSkillsForScope(skillReg, skillScope)}, nil
}
