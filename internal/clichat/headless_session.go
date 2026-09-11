package clichat

// headless_session.go builds a chat session for a host with no operator at
// the keyboard - today `mivia automations run` and `serve`.
//
// It exists because such a host had been assembling its own, much poorer
// surface: internal/cliautomations called composition.BuildSession with an
// empty RegistryInput and a zero DispatcherInput, which meant NONE of the
// workspace's [tools] policy reached the registry (no run_allowlist, no
// write_path_denylist, no secret_path_patterns, no byte caps), NO lifecycle
// hooks ran, and the dispatcher was a plain runtime.NewToolDispatcher rather
// than a session dispatcher - so the model got no agent roster, no skill
// scope and none of the session tool catalog (dispatch_tasks, the messaging
// and ledger tools, load_tools). A headless workflow run
// (internal/cliworkflow) already loads agent definitions and builds through
// NewSessionDispatcher; automations were the outlier.
//
// The sequence below is the same one runConfiguredChatOnce follows, in the
// same order, minus the parts that only mean something to an interactive
// terminal (the REPL, the memory index reconciler, the parked-run recovery
// sweep, the full-disk posture prompt).

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/composition"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/hooks"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// HeadlessSessionInput carries what NewHeadlessSession cannot derive.
//
// RunDir and StoreRoot are deliberately separate. RunDir is where the run
// EXECUTES - a managed worktree for a worktree run, the project otherwise -
// and it is the root for everything the run reads as workspace content: the
// filesystem confinement root for the file tools, the [tools] policy that
// governs them, the memory root, the agent roles and the skills. StoreRoot
// owns the durable records instead, which is why StorePath is resolved
// against it by the caller: run history must stay in one place per project
// regardless of which worktree a given fire executed in.
type HeadlessSessionInput struct {
	// RunDir is the directory the run executes in. Required.
	RunDir string
	// StorePath is the SQLite checkpoint store, already resolved against the
	// caller's own store root. Required.
	StorePath string
	// StoreRoot is the project the durable records belong to - the same
	// root StorePath was resolved against, which for a worktree run is NOT
	// RunDir. It scopes the checkpoint principal, so every run of a project
	// writes its sessions into one namespace no matter which worktree
	// executed it. Empty falls back to RunDir.
	StoreRoot string
	// Resolved is the host's configuration. It is NEVER mutated: every
	// session works on its own shallow copy, because a daemon builds many
	// sessions from one pointer and a per-run adjustment written through it
	// would leak into every later run.
	Resolved *config.Resolved
	// Restore, when non-nil, runs after the session is constructed and
	// BEFORE its surface is attached. It exists for a RESUME: the
	// interactive path loads a saved session and only then attaches
	// (chat_command.go's resumeChatSession, then attachSessionDispatcher),
	// and the order is load-bearing - publishing a surface rewrites the
	// system and memory messages and captures the prefix identity, and a
	// later Load would replace the history and the binding underneath all
	// three.
	//
	// The summarizer and token-estimate calibration are refreshed for you
	// once Restore returns, against whatever binding the restore actually
	// published; a saved session may carry a different provider or model
	// than the one this process started with.
	Restore func(*chat.Session) error
	// Completer is required, not optional. An absent completer makes
	// cliagents.AttachRebuiltSurface a silent no-op, which would leave the
	// session running on a dispatcher that never saw the workspace's tool
	// policy - a fail-open, not a degrade.
	Completer provider.Completer
}

// HeadlessSession is one fully attached session plus the state a caller
// needs to keep alongside it.
type HeadlessSession struct {
	// Session is attached and ready for turns.
	Session *chat.Session
	// Store is the checkpoint store BuildSession opened. The CALLER closes
	// it; the returned cleanup does not, because the store usually outlives
	// the surface (a caller reads run history back from it).
	Store *storage.SQLite
	// State is the session's own agent state: tier plan, skill scope, agent
	// registry, tool base. Surface rebuilds read it.
	State *cliagents.AgentSessionState
}

// NewHeadlessSession builds and attaches one session. The returned cleanup
// tears the surface down in dependency order and must run before the
// caller closes Store.
//
// The ledger repository is deliberately NOT adopted here. BuildSession
// always wires a SQLite context store, so dispatcherOptsForSurface passes it
// as SharedSQLite and NewSessionDispatcher borrows a repository over it
// (ledger.NewBorrowedStorageLedgerRepository) instead of opening one it
// would own. That is the same branch adoptSessionLedgerRepo's own
// SharedSQLite guard takes on the interactive path. Adopting one here would
// open a second durable store for no reader.
func NewHeadlessSession(in HeadlessSessionInput) (*HeadlessSession, func(), error) {
	noop := func() {}
	if err := in.validate(); err != nil {
		return nil, noop, err
	}
	// Copy-on-write: ApplyWorkspacePromptGate and the root-prompt assignment
	// below both write to the config, and the caller's pointer is shared
	// across every run a daemon fires.
	res := *in.Resolved

	state, skillReg, err := loadHeadlessAgentState(in.RunDir)
	if err != nil {
		return nil, noop, err
	}
	cliagents.ApplyWorkspacePromptGate(&res, state.Global)
	// The roster is environment fact and must reach the model that has
	// dispatch_tasks. Assigned BEFORE BuildSession, exactly like the
	// interactive path's own res.SystemPrompt line, so the session is
	// constructed with it rather than having it republished afterwards.
	res.SystemPrompt = rootPromptForSession(true, &res, state.Registry)

	bus := events.New()
	registry, closeSurface, err := buildHeadlessRegistry(in.RunDir, &res, state, bus)
	if err != nil {
		return nil, noop, err
	}

	sess, store, _, err := composition.BuildSession(composition.SessionInput{
		Config:           &res,
		Completer:        in.Completer,
		PrebuiltRegistry: registry,
		Dispatcher:       headlessDispatcherInput(in.RunDir, registry),
		EventBus:         bus,
		StorePath:        in.StorePath,
		// The SAME principal tuple the interactive host mints
		// (enableSessionContext): a normalized workspace id and the
		// "local-user" subject. composition.BuildSession defaults an empty
		// SubjectID to the session's own id, which is fresh on every fire -
		// and every catalog read is subject-scoped, so a run's saved session
		// became unreadable the moment the run ended. `automations resume`
		// could never find one, and the rows landed in a namespace neither
		// /resume nor `mivia sessions` reads.
		WorkspaceID: contextWorkspaceID(in.storeRoot()),
		SubjectID:   headlessSubjectID,
	})
	if err != nil {
		closeSurface()
		return nil, noop, fmt.Errorf("clichat: headless session: %w", err)
	}

	// Required before any Load: chat.Session.Load refuses to publish a
	// loaded transcript for a session carrying a model catalog without one
	// ("session binding factory is required for configured model
	// catalogs"), and every other host installs one.
	sess.SetBindingFactory(cliagents.ChatBindingFactory(sess, &res, in.RunDir, state))

	if err := restoreHeadlessSession(sess, &res, in.Restore); err != nil {
		abandonHeadlessSession(sess, store, closeSurface, false)
		return nil, noop, err
	}

	if err := attachHeadlessSurface(sess, &res, state, skillReg, registry); err != nil {
		abandonHeadlessSession(sess, store, closeSurface, true)
		return nil, noop, err
	}

	cleanup := func() {
		// Dispatcher first: its teardown reads through the ledger repo and
		// the remainder spool, and cliorchestrate.InitCoordinator keys a
		// coordinator, a subagent pool and a lifecycle subscription off it
		// in package-global maps that only the dispatcher's OnClose hook
		// removes. A daemon that skipped this would accumulate one of each
		// per fire, forever.
		sess.CloseDispatcher()
		closeSurface()
	}
	return &HeadlessSession{Session: sess, Store: store, State: state}, cleanup, nil
}

// buildHeadlessRegistry composes the run's tool registry: the workspace's
// own [tools] policy (cliagents.BuildToolsForRoot, which also wires the
// workflow tools and the memory store) with the configured MCP servers
// merged in. The returned closer releases both.
//
// MCP is merged BEFORE the caller builds the session, so the dispatcher and
// the advertised union both see the discovered tools - the same order
// internal/uiadapter uses. A contained server outage does not fail the
// merge (composition.MergeMCPTools continues without that server), so an
// error here is a real configuration or startup fault and fails the run
// rather than silently dropping every MCP tool.
//
// The manager is per RUN, not per spawner: it belongs to the session, and
// one shared across runs would make a run's discovered tool surface visible
// to the next. The cost is that a daemon starts and reaps each configured
// server's process once per fire.
func buildHeadlessRegistry(runDir string, res *config.Resolved, state *cliagents.AgentSessionState, bus *events.Bus) (*tools.Registry, func(), error) {
	registry, closeTools, err := cliagents.BuildToolsForRoot(runDir, runDir, false, res, cliagents.SessionRootWiring{
		Bus:                 func() *events.Bus { return bus },
		LoadWorkspaceConfig: state.Global.LoadWorkspaceConfig,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("clichat: headless session tools: %w", err)
	}
	mcpMgr, closeMCP, err := composition.AttachMCPServers(registry, res.MCP, res.RedactionPolicy, res.MCP.GlobalServerIDs())
	if err != nil {
		closeTools()
		return nil, nil, fmt.Errorf("clichat: headless session MCP: %w", err)
	}
	// Every surface rebuild re-supplies EnsureMCPTools from this manager; a
	// delegated task agent whose ensurer is nil silently loses its MCP tools.
	state.MCPManager = mcpMgr
	return registry, func() {
		closeMCP()
		closeTools()
	}, nil
}

// restoreHeadlessSession runs the caller's restore step and then re-seeds
// everything the session captured against its pre-restore binding.
//
// Both refreshes are performed HERE rather than left to the caller: a
// resume that forgets them keeps compacting through the pre-resume
// model/completer and keeps a stale token-estimate seed feeding the
// context gauge, and neither failure is visible at the call site. The
// interactive resume pairs the same two calls with its own Load
// (resumeChatSession), as does the TUI session pool.
func restoreHeadlessSession(sess *chat.Session, res *config.Resolved, restore func(*chat.Session) error) error {
	if restore == nil {
		return nil
	}
	if err := restore(sess); err != nil {
		return fmt.Errorf("clichat: headless session restore: %w", err)
	}
	cliagents.RefreshSummarizerAfterModelSwitch(sess, res)
	sess.RefreshCalibrationAfterModelSwitch(context.Background())
	return nil
}

// abandonHeadlessSession tears down a session this builder is about to
// discard.
//
// The context lease is released FIRST. composition.BuildSession's wiring
// arms a heartbeat goroutine that renews that lease every tick and never
// exits on its own, independent of the store handle - so closing the store
// alone leaves it waking forever to renew against a database that no
// longer exists, pinning the whole abandoned session. That is the same leak
// the spawner's own CloseLastRun was written to prevent, and a `serve`
// daemon would accumulate one per failed fire.
func abandonHeadlessSession(sess *chat.Session, store *storage.SQLite, closeSurface func(), closeDispatcher bool) {
	sess.ReleaseContextLease(context.Background())
	if closeDispatcher {
		sess.CloseDispatcher()
	}
	closeSurface()
	_ = store.Close()
}

// headlessSubjectID is the checkpoint principal's subject for every
// headless session. It matches the interactive host's own constant so both
// write into one namespace: a per-session subject would make each run's
// saved transcript readable only by the process that wrote it.
const headlessSubjectID = "local-user"

// storeRoot is StoreRoot, or RunDir when the caller supplied none.
func (in HeadlessSessionInput) storeRoot() string {
	if in.StoreRoot != "" {
		return in.StoreRoot
	}
	return in.RunDir
}

func (in HeadlessSessionInput) validate() error {
	switch {
	case in.RunDir == "":
		return fmt.Errorf("clichat: headless session: run dir is required")
	case in.StorePath == "":
		return fmt.Errorf("clichat: headless session: store path is required")
	case in.Resolved == nil:
		return fmt.Errorf("clichat: headless session: resolved config is required")
	case in.Completer == nil:
		return fmt.Errorf("clichat: headless session: completer is required")
	}
	return nil
}

// loadHeadlessAgentState loads the run directory's skills and agent roles,
// mirroring loadChatSkills + prepareAgentSession.
//
// Everything is read from the RUN directory, not the project root: a
// worktree run executes a branch's checkout, and that checkout's own agent
// roles, skills and tool policy are what govern it - the same rule
// BuildToolsForRoot already applies to [tools], under the same
// LoadWorkspaceConfig gate.
func loadHeadlessAgentState(runDir string) (*cliagents.AgentSessionState, *skills.Registry, error) {
	global, err := config.LoadAgentsGlobal(runDir)
	if err != nil {
		return nil, nil, fmt.Errorf("clichat: headless session agents config: %w", err)
	}
	skillReg, skillWarnings, err := cliagents.LoadSessionSkills(runDir, global.LoadWorkspaceConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("clichat: headless session skills: %w", err)
	}
	cliagents.WarnSkillLoad(skillWarnings)
	loaded, err := cliagents.LoadAgentDefinitions(runDir, "", skillReg)
	if err != nil {
		return nil, nil, fmt.Errorf("clichat: headless session agents: %w", err)
	}
	cliagents.WarnAgentLoad(loaded.Warnings)
	return &cliagents.AgentSessionState{
		Global:             loaded.Global,
		Selected:           loaded.Selected,
		AllowProjectSkills: loaded.Global.LoadWorkspaceConfig,
		Registry:           loaded.Registry,
		WorkspaceRoot:      runDir,
		SkillRegFull:       skillReg,
	}, skillReg, nil
}

// headlessDispatcherInput carries the lifecycle-hook wiring onto the
// dispatcher BuildSession constructs.
//
// composition.HookPolicyFuncs returns nil funcs unless BOTH WorkspaceRoot
// and HooksConfigured are set, so omitting either silently disarms every
// PreToolUse and PostToolUse hook the workspace declares - which is how a
// headless automation run came to execute run_command with none of this
// project's own guards. HooksConfigured is read once, here, at build time;
// a hook session installed after this point does not arm this dispatcher.
func headlessDispatcherInput(runDir string, registry *tools.Registry) composition.DispatcherInput {
	in := composition.DispatcherInput{Registry: registry, WorkspaceRoot: runDir}
	if HookSessionConfiguredFunc == nil || CurrentHookSessionFunc == nil {
		return in
	}
	in.HooksConfigured = HookSessionConfiguredFunc()
	in.HookGroups = func() []hooks.Group { return CurrentHookSessionFunc().RunnableGroups() }
	in.NoteHookWarnings = func(w []string) { CurrentHookSessionFunc().NoteRunWarnings(w) }
	return in
}

// attachHeadlessSurface publishes the session surface: the session
// dispatcher (tool catalog, delegation, skill scope, agent roster) over the
// registry just built.
//
// A refused publication is an ERROR here, never a soft no-op.
// AttachRebuiltSurface reports (false, 0, nil) when the process seams are
// unwired or the binding has no completer, and the session would then keep
// running on BuildSession's plain dispatcher - which carries none of the
// workspace's tool policy. Degrading into that silently is a fail-open.
func attachHeadlessSurface(sess *chat.Session, res *config.Resolved, state *cliagents.AgentSessionState, skillReg *skills.Registry, registry *tools.Registry) error {
	state.ToolBase = registry.Clone()
	sess.ToolBaseResolver = func() *tools.Registry { return state.ToolBase }
	sess.ToolDenylist = state.Global.MandatoryToolDenylistAdditions
	published, _, err := cliagents.AttachRebuiltSurface(sess, res, state)
	if err != nil {
		return fmt.Errorf("clichat: headless session surface: %w", err)
	}
	if !published {
		return fmt.Errorf("clichat: headless session surface: publication refused; the session would run without the workspace's tool policy")
	}
	return nil
}
