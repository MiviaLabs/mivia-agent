package uiadapter

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/hooksession"
	"github.com/MiviaLabs/mivia-agent/internal/miviaauth"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/composer"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// CommandRunner bridges the UI slash-command loop with the backend session,
// config, and agent state.
type CommandRunner struct {
	compactionMu     sync.Mutex
	compactionActive bool
	sess             *chat.Session
	pool             *SessionPool
	res              *config.Resolved
	agentState       *cliagents.AgentSessionState
	settingsStore    *SettingsStore

	// loginService builds the miviaauth.Service /login talks to. A field
	// rather than a direct miviaauth.DefaultService() call so tests in
	// this package can substitute a stub sessionClient without a real
	// HTTP round trip or a real ~/.mivia/auth.json.
	loginService func() (*miviaauth.Service, error)

	// summariesFn overrides the worktree-row listing source for tests
	// (nil = the real three-arm UNION query). Package-internal only.
	summariesFn func() ([]ports.SessionSummary, error)
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
		sess:         sess,
		pool:         pool,
		res:          res,
		agentState:   state,
		loginService: miviaauth.DefaultService,
	}
}

// Pool returns the SessionPool backing this runner's session switches
// (/resume, /new). Callers building the UI use it to source the initial
// Conversation and its SubagentThreads registry from the SAME pool that
// SelectSession and handleNew hand out on later switches, so the
// activity panel's thread dialog is wired to whichever session is
// actually active rather than a separately-constructed twin.
func (r *CommandRunner) Pool() *SessionPool {
	return r.pool
}

// NewCommandRunnerWithPool constructs a CommandRunner with an explicit SessionPool.
func NewCommandRunnerWithPool(sess *chat.Session, pool *SessionPool, res *config.Resolved, state *cliagents.AgentSessionState) *CommandRunner {
	return &CommandRunner{
		sess:         sess,
		pool:         pool,
		res:          res,
		agentState:   state,
		loginService: miviaauth.DefaultService,
	}
}

// SetSettingsStore links a SettingsStore to synchronize active sessions during session switches.
func (r *CommandRunner) SetSettingsStore(s *SettingsStore) {
	if r != nil {
		r.settingsStore = s
	}
}

// SetActiveSession updates the active session for subsequent commands. It
// also swaps r.agentState to that session's own private entry state (see
// SessionPool.AgentState), so commands run against the session actually on
// screen see and mutate ONLY that session's selected agent, skill scope, and
// tier plan - not whichever pooled session happened to switch last
// (bug-audit "pooled worktree sessions share mutable agent state"). A
// session the pool has no entry for (no pool, or a caller that bypassed
// pool-based creation) keeps whatever agentState was already bound, matching
// the pre-fork behavior.
func (r *CommandRunner) SetActiveSession(sess *chat.Session) {
	if r != nil {
		r.sess = sess
		if r.pool != nil && sess != nil {
			if state := r.pool.AgentState(sess.SessionID); state != nil {
				r.agentState = state
			}
		}
		if r.settingsStore != nil {
			r.settingsStore.SetActiveSession(sess)
			r.settingsStore.agentState = r.agentState
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
		{Name: "login", Desc: "sign in to your mivia account"},
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
	case "login":
		return r.handleLogin(args)
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
	return ports.CommandOutcome{SubmitPrompt: prompt, SubmitPersistedText: skillInvocationText(target.Name, args)}, true
}

// skillInvocationText is what a skill invocation persists into conversation
// history in place of its full expanded prompt: the short command the user
// actually typed. RenderNamedSkillSlashPrompt's output is thousands of tokens
// (the whole SKILL.md body) and belongs in the one request that needs it, not
// replayed on every later turn for the rest of the session - see
// intent.Send.PersistedText. Falls back to the raw name when it does not
// resolve to a valid slash token (SlashToken rejects anything outside
// lowercase/digits/hyphen after normalization), which is defensive only:
// every definition reaching here was already matched by name/token above.
func skillInvocationText(name, args string) string {
	token, ok := skills.SlashToken(name)
	if !ok {
		token = "/" + name
	}
	if args != "" {
		return token + " " + args
	}
	return token
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
	// The compiled root agent is always offered first, so switching back
	// never needs recalling the name; registry members follow, deduped
	// against it (a workspace cannot register the reserved root name, but
	// the dedup keeps the contract local).
	choices := []string{ports.DefaultAgentName}
	if r.agentState != nil && r.agentState.Registry != nil {
		for _, name := range r.agentState.Registry.Names() {
			if name != ports.DefaultAgentName {
				choices = append(choices, name)
			}
		}
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

// SessionActive reports whether a pooled session currently has a turn
// in flight: a map lookup and an atomic load, no I/O. It is safe to
// call on every /resume picker refresh tick, unlike listSessionSummaries
// which re-queries the session store.
func (r *CommandRunner) SessionActive(id string) bool {
	return r.pool != nil && r.pool.IsActive(id)
}

func (r *CommandRunner) listSessionSummaries() ([]ports.SessionSummary, error) {
	sess := r.activeSession()
	if sess == nil {
		return nil, fmt.Errorf("no active session")
	}
	infos, err := sess.ListAllSessions()
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
		// Route pseudo-rows keep the "Worktree · <name>" label the REPL's
		// session catalog uses, so both surfaces name them alike.
		if info.WorktreeRoute && info.Worktree != "" {
			title = "Worktree · " + info.Worktree
		}
		active := r.SessionActive(id)
		state := "done"
		if active {
			state = "running"
		}
		out = append(out, ports.SessionSummary{
			ID:                 id,
			Title:              title,
			UpdatedAt:          info.UpdatedAt,
			Active:             active,
			State:              state,
			IsCurrent:          id == currID,
			Turns:              info.TurnCount,
			ContextTokens:      info.TokenCount,
			Worktree:           info.Worktree,
			WorktreeRoute:      info.WorktreeRoute,
			WorktreeDir:        info.Dir,
			WorktreeInstanceID: info.WorktreeInstance.ID,
		})
	}
	return out, nil
}

// resumeErrorText renders a resume failure for the transcript. The one case
// with a known action gets a plain-language message: a session whose lease
// another live process still holds tells the user who has it and how long
// until retry succeeds, instead of the raw wrapped error chain.
func resumeErrorText(id string, err error) string {
	var live *contextstate.SessionLiveError
	if errors.As(err, &live) {
		return fmt.Sprintf(
			"session %s is in use by another mivia process (last heartbeat %s ago) - close that process, or retry in ~%s",
			id, live.LeaseAge.Round(time.Second), live.RetryAfter.Round(time.Second))
	}
	return fmt.Sprintf("failed to resume session %q: %v", id, err)
}

// SelectSession loads and resumes a saved session.
func (r *CommandRunner) SelectSession(ctx context.Context, id string) ports.CommandOutcome {
	if r.activeSession() == nil && r.res == nil {
		return ports.CommandOutcome{Err: "no active session"}
	}
	if id == "" {
		return ports.CommandOutcome{Err: "session ID is empty"}
	}
	if r.pool != nil {
		// Tripwire 1 router: if the typed id matches a LISTED bound row,
		// resume through the root-scoped creator instead of an unbound
		// Load - today byte-identical (bound ids are never listed), and
		// correct-by-construction if storage ever starts leaking them.
		if summary, ok := selectWorktreeSummary(r.summariesFor(), id); ok {
			return r.ResumeInWorktree(ctx, summary)
		}
		conv, err := r.pool.GetOrCreate(id)
		if err != nil {
			return ports.CommandOutcome{Err: resumeErrorText(id, err)}
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
		return ports.CommandOutcome{Err: resumeErrorText(id, err)}
	}
	// This fallback (no session pool) reuses the active Session object across
	// resumes rather than building a fresh one, so both the summarizer and the
	// token-estimate calibration are still bound to whatever session Load just
	// replaced - the same staleness session_pool.go's own resume path guards
	// against. See chat.Session.RefreshCalibrationAfterModelSwitch's doc
	// comment for what a stale calibration seed does to the context gauge.
	cliagents.RefreshSummarizerAfterModelSwitch(sess, r.res)
	sess.RefreshCalibrationAfterModelSwitch(ctx)
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
