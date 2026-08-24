package uiadapter

import (
	"context"
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/composer"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// CommandRunner bridges the UI slash-command loop with the backend session,
// config, and agent state.
type CommandRunner struct {
	sess       *chat.Session
	pool       *SessionPool
	res        *config.Resolved
	agentState *cliagents.AgentSessionState
}

// Compile-time check that CommandRunner satisfies ports.CommandRunner.
var _ ports.CommandRunner = (*CommandRunner)(nil)

// NewCommandRunner constructs a CommandRunner for the given session and configuration.
func NewCommandRunner(sess *chat.Session, res *config.Resolved, state *cliagents.AgentSessionState) *CommandRunner {
	var toolsOn bool
	if sess != nil {
		toolsOn = sess.UseTools
	}
	pool := NewSessionPool(sess, res, state, toolsOn)
	return &CommandRunner{
		sess:       sess,
		pool:       pool,
		res:        res,
		agentState: state,
	}
}

// NewCommandRunnerWithPool constructs a CommandRunner with an explicit SessionPool.
func NewCommandRunnerWithPool(sess *chat.Session, pool *SessionPool, res *config.Resolved, state *cliagents.AgentSessionState) *CommandRunner {
	return &CommandRunner{
		sess:       sess,
		pool:       pool,
		res:        res,
		agentState: state,
	}
}

// DefaultCommands returns the list of available slash commands for composer auto-completion.
func DefaultCommands() []composer.Command {
	return []composer.Command{
		{Name: "agents", Desc: "pick or switch agent (Mivia is the default orchestrator)"},
		{Name: "clear", Desc: "clear conversation transcript"},
		{Name: "compact", Desc: "compact context and history"},
		{Name: "context", Desc: "show context token usage"},
		{Name: "cost", Desc: "show current session spend"},
		{Name: "help", Desc: "show the keymap dialog"},
		{Name: "model", Desc: "pick or switch model"},
		{Name: "quit", Desc: "exit mivia"},
		{Name: "resume", Desc: "resume a previous session"},
		{Name: "settings", Desc: "open the settings screen"},
		{Name: "theme", Desc: "pick a theme"},
	}
}

// Run executes one slash command by name with arguments.
func (r *CommandRunner) Run(ctx context.Context, name, args string) ports.CommandOutcome {
	switch name {
	case "theme":
		return ports.CommandOutcome{OpenTheme: true}
	case "help":
		return ports.CommandOutcome{OpenHelp: true}
	case "settings":
		return ports.CommandOutcome{OpenSettings: true, SettingsSection: args}
	case "quit", "exit":
		return ports.CommandOutcome{Quit: true}
	case "clear":
		return r.handleClear()
	case "compact":
		return r.handleCompact(ctx, args)
	case "context":
		return r.handleContext()
	case "cost":
		return r.handleCost()
	case "model":
		return r.handleModel(args)
	case "agents", "agent":
		return r.handleAgents(args)
	case "resume", "load":
		return r.handleResume(args)
	default:
		label := "/" + name
		if args != "" {
			label += " " + args
		}
		return ports.CommandOutcome{Err: "unknown command " + label}
	}
}

func (r *CommandRunner) handleClear() ports.CommandOutcome {
	if r.sess == nil {
		return ports.CommandOutcome{Err: "no active session"}
	}
	if err := r.sess.Clear(); err != nil {
		return ports.CommandOutcome{Err: "clear failed: " + err.Error()}
	}
	return ports.CommandOutcome{ClearTranscript: true}
}

func (r *CommandRunner) handleCompact(ctx context.Context, focus string) ports.CommandOutcome {
	if r.sess == nil {
		return ports.CommandOutcome{Err: "no active session"}
	}
	if err := r.sess.Compact(ctx, focus); err != nil {
		return ports.CommandOutcome{Err: "compact failed: " + err.Error()}
	}
	u := r.sess.ContextUsage()
	notice := fmt.Sprintf("Context compacted (%d%% used, %s/%s prompt).",
		u.Percent, chat.FormatTokenK(u.UsedTokens), chat.FormatTokenK(u.BudgetTokens))
	return ports.CommandOutcome{Notice: notice}
}

func (r *CommandRunner) handleContext() ports.CommandOutcome {
	if r.sess == nil {
		return ports.CommandOutcome{Err: "no active session"}
	}
	u := r.sess.ContextUsage()
	notice := fmt.Sprintf("Context usage: %s used (%d%% of %s budget, reserve %s).",
		chat.FormatTokenK(u.UsedTokens), u.Percent, chat.FormatTokenK(u.BudgetTokens), chat.FormatTokenK(u.OutputReserveTokens))
	return ports.CommandOutcome{Notice: notice}
}

func (r *CommandRunner) handleCost() ports.CommandOutcome {
	if r.sess == nil {
		return ports.CommandOutcome{Err: "no active session"}
	}
	u := r.sess.ContextUsage()
	notice := fmt.Sprintf("Context: %d tokens used.", u.UsedTokens)
	return ports.CommandOutcome{Notice: notice}
}

func (r *CommandRunner) handleModel(args string) ports.CommandOutcome {
	if r.sess == nil || r.res == nil {
		return ports.CommandOutcome{Err: "session or configuration not initialized"}
	}
	if args != "" {
		return r.SelectModel(context.Background(), args)
	}
	groups := r.availableModelsByProvider()
	if len(groups) == 0 {
		return ports.CommandOutcome{Err: "no models loaded"}
	}
	return ports.CommandOutcome{ModelChoiceGroups: groups}
}

// availableModelsByProvider returns the selectable catalog grouped by
// provider, in catalog order. The first group's provider name is the
// currently selected provider; later groups keep their catalog order
// so the picker stays stable across re-opens. An empty catalog
// falls back to the session's current model in a single flat group
// with no provider header.
func (r *CommandRunner) availableModelsByProvider() []ports.ModelChoiceGroup {
	if r.res == nil {
		return nil
	}
	var groups []ports.ModelChoiceGroup
	for _, group := range r.res.ModelCatalog() {
		if !group.Selectable {
			continue
		}
		names := make([]string, 0, len(group.Models))
		for _, m := range group.Models {
			names = append(names, m.Name)
		}
		if len(names) == 0 {
			continue
		}
		groups = append(groups, ports.ModelChoiceGroup{
			Provider: group.Provider,
			Models:   names,
		})
	}
	if len(groups) == 0 && r.sess != nil {
		if cur := r.sess.CurrentModel(); cur != "" {
			return []ports.ModelChoiceGroup{{Models: []string{cur}}}
		}
	}
	return groups
}

// SelectModel switches the session's active model.
func (r *CommandRunner) SelectModel(_ context.Context, name string) ports.CommandOutcome {
	if r.sess == nil || r.res == nil {
		return ports.CommandOutcome{Err: "session or configuration not initialized"}
	}
	name = strings.TrimSpace(name)
	providerName := r.res.ProviderName
	if sel := r.sess.CurrentSelection(); sel.ProviderName != "" {
		providerName = sel.ProviderName
	}

	// 1. Check if model name has an explicit provider prefix (e.g. "openrouter/anthropic/claude-3.5-sonnet")
	for _, group := range r.res.ModelCatalog() {
		prefix := group.Provider + "/"
		if strings.HasPrefix(strings.ToLower(name), prefix) {
			providerName = group.Provider
			name = name[len(prefix):]
			break
		}
	}

	// 2. Check if the model name belongs uniquely to a provider in the catalog
	foundInCatalog := false
	for _, group := range r.res.ModelCatalog() {
		if !group.Selectable {
			continue
		}
		for _, m := range group.Models {
			if m.Name == name {
				providerName = group.Provider
				foundInCatalog = true
				break
			}
		}
		if foundInCatalog {
			break
		}
	}

	discarded, err := cliagents.SwitchModelCommand(r.sess, r.res, providerName, name)
	if err != nil {
		return ports.CommandOutcome{Err: fmt.Sprintf("failed to switch model to %q (%s): %v", name, providerName, err)}
	}
	r.res.ProviderName = providerName
	r.res.Model = name
	notice := fmt.Sprintf("Model set to %s (%s).", name, providerName)
	if discarded != "" {
		notice += fmt.Sprintf(" (Reasoning effort override %q discarded).", discarded)
	}
	return ports.CommandOutcome{Notice: notice}
}

func (r *CommandRunner) handleAgents(args string) ports.CommandOutcome {
	if args != "" {
		return r.SelectAgent(context.Background(), args)
	}
	choices := r.availableAgents()
	if len(choices) == 0 {
		return ports.CommandOutcome{Err: "no agents loaded"}
	}
	return ports.CommandOutcome{AgentChoices: choices}
}

func (r *CommandRunner) availableAgents() []string {
	var choices []string
	if r.agentState != nil && r.agentState.Registry != nil {
		choices = r.agentState.Registry.Names()
	}
	if len(choices) == 0 {
		choices = append(choices, ports.DefaultAgentName)
	}
	return choices
}

// SelectAgent switches the session's active agent.
func (r *CommandRunner) SelectAgent(_ context.Context, name string) ports.CommandOutcome {
	if r.sess == nil || r.agentState == nil {
		return ports.CommandOutcome{Err: "agent switching is not available in this session"}
	}
	if name == "" {
		return ports.CommandOutcome{Err: "agent name is empty"}
	}
	err := cliagents.ApplySessionAgent(r.sess, r.res, r.agentState, name, false)
	if err != nil {
		return ports.CommandOutcome{Err: fmt.Sprintf("failed to switch agent to %q: %v", name, err)}
	}
	return ports.CommandOutcome{Notice: fmt.Sprintf("Switched active agent to %s.", name)}
}

func (r *CommandRunner) handleResume(args string) ports.CommandOutcome {
	if r.sess == nil {
		return ports.CommandOutcome{Err: "no active session"}
	}
	if args != "" {
		return r.SelectSession(context.Background(), args)
	}
	summaries, err := r.listSessionSummaries()
	if err != nil {
		return ports.CommandOutcome{Err: "failed to list sessions: " + err.Error()}
	}
	if len(summaries) == 0 {
		return ports.CommandOutcome{Err: "no saved sessions found"}
	}
	return ports.CommandOutcome{SessionChoices: summaries}
}

func (r *CommandRunner) listSessionSummaries() ([]ports.SessionSummary, error) {
	infos, err := r.sess.ListSessions()
	if err != nil {
		return nil, err
	}
	currID := r.sess.SessionID
	var out []ports.SessionSummary
	for _, info := range infos {
		id := info.SessionID
		if id == "" {
			id = info.Name
		}
		title := info.Title
		if title == "" {
			title = info.Name
		}
		out = append(out, ports.SessionSummary{
			ID:            id,
			Title:         title,
			UpdatedAt:     info.UpdatedAt,
			Active:        id == currID,
			State:         "done",
			Turns:         info.TurnCount,
			ContextTokens: info.TokenCount,
		})
	}
	return out, nil
}

// SelectSession loads and resumes a saved session.
func (r *CommandRunner) SelectSession(_ context.Context, id string) ports.CommandOutcome {
	if r.sess == nil && r.res == nil {
		return ports.CommandOutcome{Err: "no active session"}
	}
	if id == "" {
		return ports.CommandOutcome{Err: "session ID is empty"}
	}
	if r.pool != nil {
		conv, err := r.pool.GetOrCreate(id)
		if err != nil {
			return ports.CommandOutcome{Err: fmt.Sprintf("failed to resume session %q: %v", id, err)}
		}
		return ports.CommandOutcome{
			Conversation:    conv,
			ClearTranscript: true,
			Notice:          fmt.Sprintf("Resumed session %s.", id),
		}
	}
	if r.sess == nil {
		return ports.CommandOutcome{Err: "no active session"}
	}
	if err := r.sess.Load(id); err != nil {
		return ports.CommandOutcome{Err: fmt.Sprintf("failed to resume session %q: %v", id, err)}
	}
	return ports.CommandOutcome{
		Conversation:    NewConversation(r.sess),
		ClearTranscript: true,
		Notice:          fmt.Sprintf("Resumed session %s.", id),
	}
}
