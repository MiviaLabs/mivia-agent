package uiadapter

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/hooksession"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/composer"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// CommandRunner bridges the UI slash-command loop with the backend session,
// config, and agent state.
type CommandRunner struct {
	sess          *chat.Session
	pool          *SessionPool
	res           *config.Resolved
	agentState    *cliagents.AgentSessionState
	settingsStore *SettingsStore
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

// SetSettingsStore links a SettingsStore to synchronize active sessions during session switches.
func (r *CommandRunner) SetSettingsStore(s *SettingsStore) {
	if r != nil {
		r.settingsStore = s
	}
}

// SetActiveSession updates the active session for subsequent commands.
func (r *CommandRunner) SetActiveSession(sess *chat.Session) {
	if r != nil {
		r.sess = sess
		if r.settingsStore != nil {
			r.settingsStore.SetActiveSession(sess)
		}
	}
}

func (r *CommandRunner) activeSession() *chat.Session {
	if r == nil {
		return nil
	}
	return r.sess
}

func (r *CommandRunner) skillRegistry() *skills.Registry {
	if r == nil {
		return nil
	}
	if r.agentState != nil && r.agentState.SkillRegFull != nil {
		return r.agentState.SkillRegFull
	}
	if r.sess != nil {
		return r.sess.CurrentBinding().SkillRegistry
	}
	return nil
}

// DefaultCommands returns the list of available slash commands for composer auto-completion.
func DefaultCommands() []composer.Command {
	return []composer.Command{
		{Name: "agents", Desc: "pick or switch agent (Mivia is the default orchestrator)"},
		{Name: "clear", Desc: "clear conversation transcript"},
		{Name: "new", Desc: "start a fresh session"},
		{Name: "compact", Desc: "compact context and history"},
		{Name: "context", Desc: "show context token usage"},
		{Name: "cost", Desc: "show current session spend"},
		{Name: "effort", Desc: "set reasoning effort level for active model"},
		{Name: "help", Desc: "show the keymap dialog"},
		{Name: "hooks", Desc: "list armed lifecycle hooks"},
		{Name: "model", Desc: "pick or switch model"},
		{Name: "queue", Desc: "manage queued messages"},
		{Name: "quit", Desc: "exit mivia"},
		{Name: "resume", Desc: "resume a previous session"},
		{Name: "settings", Desc: "open the settings screen"},
		{Name: "theme", Desc: "pick a theme"},
		{Name: "yolo", Desc: "toggle YOLO mode (auto-approve all tool executions)"},
	}
}

// SkillCommands returns the slash command candidates for the given skill registry.
func SkillCommands(reg *skills.Registry) []composer.Command {
	if reg == nil {
		return nil
	}
	builtins := DefaultCommands()
	reserved := make(map[string]struct{}, len(builtins))
	for _, c := range builtins {
		reserved[c.Name] = struct{}{}
	}

	var cmds []composer.Command
	for _, def := range reg.List() {
		if !def.UserInvocable {
			continue
		}
		rawToken, ok := skills.SlashToken(def.Name)
		if !ok {
			continue
		}
		name := strings.TrimPrefix(rawToken, "/")
		if _, collision := reserved[name]; collision {
			continue
		}
		desc := def.ShortDescription
		if desc == "" {
			desc = def.Description
			if cut := strings.IndexAny(desc, ".;:\n"); cut > 0 {
				desc = desc[:cut]
			}
			desc, _ = skills.SanitizeModelFacingText(desc, 60)
		}
		cmds = append(cmds, composer.Command{
			Name: name,
			Desc: desc,
		})
	}
	sort.Slice(cmds, func(i, j int) bool {
		return cmds[i].Name < cmds[j].Name
	})
	return cmds
}

// Commands returns all available slash commands, merging builtins with active skills.
func (r *CommandRunner) Commands() []composer.Command {
	builtins := DefaultCommands()
	skillCmds := SkillCommands(r.skillRegistry())
	if len(skillCmds) == 0 {
		return builtins
	}
	out := make([]composer.Command, 0, len(builtins)+len(skillCmds))
	out = append(out, builtins...)
	out = append(out, skillCmds...)
	return out
}

// Run executes one slash command by name with arguments.
func (r *CommandRunner) Run(ctx context.Context, name, args string) ports.CommandOutcome {
	switch name {
	case "theme":
		return ports.CommandOutcome{OpenTheme: true}
	case "help":
		return ports.CommandOutcome{OpenHelp: true}
	case "hooks":
		return r.handleHooks(args)
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
	case "effort":
		return r.handleEffort(args)
	case "model":
		return r.handleModel(args)
	case "queue":
		return ports.CommandOutcome{OpenQueue: true}
	case "agents", "agent":
		return r.handleAgents(args)
	case "new":
		return r.handleNew()
	case "resume", "load":
		return r.handleResume(args)
	case "yolo":
		return r.handleYolo()
	default:
		if outcome, handled := r.handleSkill(name, args); handled {
			return outcome
		}
		label := "/" + name
		if args != "" {
			label += " " + args
		}
		return ports.CommandOutcome{Err: "unknown command " + label}
	}
}

func (r *CommandRunner) handleSkill(name, args string) (ports.CommandOutcome, bool) {
	reg := r.skillRegistry()
	if reg == nil {
		return ports.CommandOutcome{}, false
	}
	var target skills.Definition
	var found bool
	cleanName := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(name)), "/")
	for _, def := range reg.List() {
		token, ok := skills.SlashToken(def.Name)
		bareToken := strings.TrimPrefix(token, "/")
		if (ok && bareToken == cleanName) || strings.EqualFold(def.Name, cleanName) {
			target = def
			found = true
			break
		}
	}
	if !found {
		return ports.CommandOutcome{}, false
	}
	if !target.UserInvocable {
		return ports.CommandOutcome{Err: fmt.Sprintf("skill %q cannot be invoked directly", name)}, true
	}
	if r.agentState != nil {
		scope := r.agentState.SkillScopeSnapshot()
		if err := scope.CheckSkillDefinition(target); err != nil {
			return ports.CommandOutcome{Err: fmt.Sprintf("skill %q not permitted: %v", name, err)}, true
		}
	}
	prompt := skills.RenderNamedSkillSlashPrompt(target.Name, target.Instructions, args)
	return ports.CommandOutcome{SubmitPrompt: prompt}, true
}

func (r *CommandRunner) handleClear() ports.CommandOutcome {
	sess := r.activeSession()
	if sess == nil {
		return ports.CommandOutcome{Err: "no active session"}
	}
	if err := sess.Clear(); err != nil {
		return ports.CommandOutcome{Err: "clear failed: " + err.Error()}
	}
	return ports.CommandOutcome{ClearTranscript: true}
}

func (r *CommandRunner) handleCompact(ctx context.Context, focus string) ports.CommandOutcome {
	sess := r.activeSession()
	if sess == nil {
		return ports.CommandOutcome{Err: "no active session"}
	}
	if err := sess.Compact(ctx, focus); err != nil {
		return ports.CommandOutcome{Err: "compact failed: " + err.Error()}
	}
	u := sess.ContextUsage()
	notice := fmt.Sprintf("Context compacted (%d%% used, %s/%s prompt).",
		u.Percent, chat.FormatTokenK(u.UsedTokens), chat.FormatTokenK(u.BudgetTokens))
	return ports.CommandOutcome{Notice: notice}
}

// handleHooks serves /hooks on the new TUI. It reaches internal/hooksession
// directly rather than through internal/cli: hooksession is a leaf package
// (imports only internal/config and internal/hooks), so this does not
// reintroduce the CLI dependency internal/uiadapter must stay free of.
func (r *CommandRunner) handleHooks(args string) ports.CommandOutcome {
	fields := append([]string{"/hooks"}, strings.Fields(args)...)
	return ports.CommandOutcome{Notice: hooksession.SlashOutput(fields)}
}

func (r *CommandRunner) handleContext() ports.CommandOutcome {
	sess := r.activeSession()
	if sess == nil {
		return ports.CommandOutcome{Err: "no active session"}
	}
	u := sess.ContextUsage()
	notice := fmt.Sprintf("Context usage: %s used (%d%% of %s budget, reserve %s).",
		chat.FormatTokenK(u.UsedTokens), u.Percent, chat.FormatTokenK(u.BudgetTokens), chat.FormatTokenK(u.OutputReserveTokens))
	return ports.CommandOutcome{Notice: notice}
}

func (r *CommandRunner) handleCost() ports.CommandOutcome {
	sess := r.activeSession()
	if sess == nil {
		return ports.CommandOutcome{Err: "no active session"}
	}
	u := sess.ContextUsage()
	notice := fmt.Sprintf("Context: %d tokens used.", u.UsedTokens)
	return ports.CommandOutcome{Notice: notice}
}

func (r *CommandRunner) handleModel(args string) ports.CommandOutcome {
	if r.activeSession() == nil || r.res == nil {
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
	sess := r.activeSession()
	if len(groups) == 0 && sess != nil {
		if cur := sess.CurrentModel(); cur != "" {
			return []ports.ModelChoiceGroup{{Models: []string{cur}}}
		}
	}
	return groups
}

func resolveProviderAndModel(res *config.Resolved, selProvider, name string) (string, string) {
	name = strings.TrimSpace(name)
	providerName := res.ProviderName
	if selProvider != "" {
		providerName = selProvider
	}

	// 1. Explicit provider prefix matching a catalog provider
	for _, group := range res.ModelCatalog() {
		prefix := group.Provider + "/"
		if strings.HasPrefix(strings.ToLower(name), prefix) {
			return group.Provider, name[len(prefix):]
		}
	}

	// 1b. Prefix matching a configured provider runtime
	if res.ProviderRuntimes != nil {
		for p := range res.ProviderRuntimes {
			prefix := strings.ToLower(p) + "/"
			if strings.HasPrefix(strings.ToLower(name), prefix) {
				return p, name[len(prefix):]
			}
		}
	}

	// 1c. Name containing a slash matching known provider name
	if p, m, ok := strings.Cut(name, "/"); ok && p != "" && m != "" {
		for _, group := range res.ModelCatalog() {
			if strings.EqualFold(group.Provider, p) {
				return group.Provider, m
			}
		}
		if res.ProviderRuntimes != nil {
			for rName := range res.ProviderRuntimes {
				if strings.EqualFold(rName, p) {
					return rName, m
				}
			}
		}
	}

	// 2. Search unique provider in catalog. A name matching more than one
	// Selectable provider is NOT resolved here - silently picking the first
	// catalog-order match would be an unannounced provider switch (different
	// auth, base URL, and wire behavior) on nothing but name coincidence,
	// the exact class of surprise a same-named model across providers (e.g.
	// "claude-sonnet-5" under both an OpenAI-compatible proxy and the native
	// anthropic provider) causes. Falling through here leaves providerName
	// as today's default (current selection), and SwitchModelCommand's
	// resulting "not available" error carries the ambiguity via
	// res.OtherProvidersWithModel in SelectModel's error path below - naming
	// every match so the user picks explicitly with /model <provider> <name>
	// rather than the tool guessing for them.
	var matchedProvider string
	matches := 0
	for _, group := range res.ModelCatalog() {
		if !group.Selectable {
			continue
		}
		for _, m := range group.Models {
			if m.Name == name {
				matchedProvider = group.Provider
				matches++
				break
			}
		}
	}
	if matches == 1 {
		return matchedProvider, name
	}

	return providerName, name
}

// SelectModel switches the session's active model.
func (r *CommandRunner) SelectModel(_ context.Context, name string) ports.CommandOutcome {
	sess := r.activeSession()
	if sess == nil || r.res == nil {
		return ports.CommandOutcome{Err: "session or configuration not initialized"}
	}
	selProvider := ""
	if sel := sess.CurrentSelection(); sel.ProviderName != "" {
		selProvider = sel.ProviderName
	}
	providerName, modelName := resolveProviderAndModel(r.res, selProvider, name)

	discarded, err := cliagents.SwitchModelCommand(sess, r.res, providerName, modelName)
	if err != nil {
		msg := fmt.Sprintf("failed to switch model to %q (%s): %v", modelName, providerName, err)
		if others := r.res.OtherProvidersWithModel(providerName, modelName); len(others) == 1 {
			msg += fmt.Sprintf(" (found under provider %s - run /model %s %s to switch)", others[0], others[0], modelName)
		} else if len(others) > 1 {
			msg += fmt.Sprintf(" (found under providers: %s - run /model <provider> %s to switch)", strings.Join(others, ", "), modelName)
		}
		return ports.CommandOutcome{Err: msg}
	}
	r.res.ProviderName = providerName
	r.res.Model = modelName
	notice := fmt.Sprintf("Model set to %s (%s).", modelName, providerName)
	if discarded != "" {
		notice += fmt.Sprintf(" (Reasoning effort override %q discarded).", discarded)
	}
	return ports.CommandOutcome{Notice: notice}
}

func (r *CommandRunner) handleEffort(args string) ports.CommandOutcome {
	sess := r.activeSession()
	if sess == nil {
		return ports.CommandOutcome{Err: "no active session"}
	}
	if args != "" {
		return r.SelectEffort(context.Background(), args)
	}
	choices := sess.ReasoningChoices()
	if len(choices) == 0 {
		return ports.CommandOutcome{Notice: fmt.Sprintf("Model %q declares no reasoning efforts.", sess.CurrentModel())}
	}
	defaultEffort := sess.ReasoningDefault()
	var list []string
	for _, c := range choices {
		str := string(c)
		if c == defaultEffort {
			str += " (default)"
		}
		list = append(list, str)
	}
	if !defaultEffort.Active() {
		list = append(list, "unset")
	}
	return ports.CommandOutcome{EffortChoices: list}
}

// SelectEffort applies a reasoning effort level override.
func (r *CommandRunner) SelectEffort(_ context.Context, levelStr string) ports.CommandOutcome {
	sess := r.activeSession()
	if sess == nil {
		return ports.CommandOutcome{Err: "no active session"}
	}
	levelStr = strings.TrimSpace(levelStr)
	// Strip optional " (default)" marker if present
	if idx := strings.Index(levelStr, " ("); idx >= 0 {
		levelStr = levelStr[:idx]
	}
	var level reasoning.Level
	if levelStr == "unset" || levelStr == "" {
		level = ""
	} else {
		parsed, err := reasoning.ParseLevel(levelStr)
		if err != nil {
			return ports.CommandOutcome{Err: fmt.Sprintf("invalid reasoning effort %q: %v", levelStr, err)}
		}
		level = parsed
	}

	if err := sess.SetReasoningEffort(level); err != nil {
		return ports.CommandOutcome{Err: fmt.Sprintf("failed to set reasoning effort: %v", err)}
	}

	model := sess.CurrentModel()
	effective := sess.ReasoningSetting()
	if level.Active() {
		return ports.CommandOutcome{Notice: fmt.Sprintf("Reasoning effort set to %s for %s.", effective.Level, model)}
	}
	if effective.Active() {
		return ports.CommandOutcome{Notice: fmt.Sprintf("Reasoning effort choice cleared for %s: %s (model default) is in force.", model, effective.Level)}
	}
	return ports.CommandOutcome{Notice: fmt.Sprintf("Reasoning effort unset for %s: no reasoning field is sent.", model)}
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
	sess := r.activeSession()
	if sess == nil || r.agentState == nil {
		return ports.CommandOutcome{Err: "agent switching is not available in this session"}
	}
	if name == "" {
		return ports.CommandOutcome{Err: "agent name is empty"}
	}
	err := cliagents.ApplySessionAgent(sess, r.res, r.agentState, name, false)
	if err != nil {
		return ports.CommandOutcome{Err: fmt.Sprintf("failed to switch agent to %q: %v", name, err)}
	}
	return ports.CommandOutcome{Notice: fmt.Sprintf("Switched active agent to %s.", name)}
}

func (r *CommandRunner) handleResume(args string) ports.CommandOutcome {
	if r.activeSession() == nil {
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
	sess := r.activeSession()
	if sess == nil {
		return nil, fmt.Errorf("no active session")
	}
	infos, err := sess.ListSessions()
	if err != nil {
		return nil, err
	}
	currID := sess.SessionID
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
	if r.activeSession() == nil && r.res == nil {
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
		if c, ok := conv.(*Conversation); ok && c != nil {
			r.SetActiveSession(c.Session())
		}
		return ports.CommandOutcome{
			Conversation:    conv,
			ClearTranscript: true,
			Notice:          fmt.Sprintf("Resumed session %s.", id),
		}
	}
	sess := r.activeSession()
	if sess == nil {
		return ports.CommandOutcome{Err: "no active session"}
	}
	if err := sess.Load(id); err != nil {
		return ports.CommandOutcome{Err: fmt.Sprintf("failed to resume session %q: %v", id, err)}
	}
	r.SetActiveSession(sess)
	return ports.CommandOutcome{
		Conversation:    NewConversation(sess),
		ClearTranscript: true,
		Notice:          fmt.Sprintf("Resumed session %s.", id),
	}
}

func (r *CommandRunner) handleNew() ports.CommandOutcome {
	sess := r.activeSession()
	if sess == nil {
		return ports.CommandOutcome{Err: "no active session"}
	}
	// Best-effort save; proceed even on failure.
	if err := sess.SaveLast(); err != nil {
		// Non-fatal: log to notice suffix below.
		_ = err
	}
	if r.pool == nil {
		return ports.CommandOutcome{Err: "no session pool available"}
	}
	conv, err := r.pool.CreateFresh()
	if err != nil {
		return ports.CommandOutcome{Err: "failed to create new session: " + err.Error()}
	}
	if c, ok := conv.(*Conversation); ok && c != nil {
		r.SetActiveSession(c.Session())
	}
	return ports.CommandOutcome{
		Conversation:    conv,
		ClearTranscript: true,
		Notice:          "New session started.",
	}
}

func (r *CommandRunner) handleYolo() ports.CommandOutcome {
	sess := r.activeSession()
	if sess == nil {
		return ports.CommandOutcome{Err: "no active session"}
	}
	enabled, policy := sess.ToggleYOLO()
	if enabled {
		return ports.CommandOutcome{Notice: "YOLO mode enabled: all tool calls auto-approved."}
	}
	return ports.CommandOutcome{Notice: fmt.Sprintf("YOLO mode disabled: active approval policy is %q.", policy)}
}
