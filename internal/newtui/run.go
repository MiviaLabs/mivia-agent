package newtui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/automation"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/screen/conversation"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/termprobe"
)

func registerSubagentProgress() {
	uiadapter.SubagentProgressRegistrar = func(fn func(agent.Event)) func() {
		token := cli.SetSubagentProgress(fn)
		return func() {
			cli.ClearSubagentProgress(token)
		}
	}
	uiadapter.SessionBusRegistrar = cli.RegisterSessionBus
	// Closes the live half of the per-subagent cancel keys: the route table
	// NewSubagentThreads hands over here is what every later dispatch
	// publishes its (coordinator, runID, taskID) identities into, and what
	// Screen.cancelSelectedSubagentTask (and the thread dialog's
	// per-tool-call cancel) resolve the highlighted row through. Set before
	// buildApp, which is where NewSubagentThreads actually runs. The
	// adapter narrows each published coordinator to the
	// SubagentTaskCoordinator subset the route table stores, because the
	// dispatch side's sink type is not assignable to the UI-side one
	// directly (func parameter types must match exactly).
	uiadapter.SubagentTaskRouteRegistrar = func(sink func(coord uiadapter.SubagentTaskCoordinator, callID, runID, taskID string)) {
		cli.SetSubagentTaskRouteSink(func(coord cli.OrchestrationCoordinator, callID, runID, taskID string) {
			// The dispatch side publishes its narrow orchestration view; the
			// route table stores the UI-cancel subset. The live value is the
			// real coordinator, which carries both, so the widening
			// assertion holds everywhere a route is actually published.
			if c, ok := coord.(uiadapter.SubagentTaskCoordinator); ok {
				sink(c, callID, runID, taskID)
			}
		})
	}
}

// RunTUI is the alternative launcher that wires the new Mivia UI.
func RunTUI(sess *chat.Session, res *config.Resolved, toolsOn bool, agentState *cli.AgentSessionState, resumeSessionName string) error {
	registerSubagentProgress()
	prevLogWriter := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(prevLogWriter)

	root, settingsStore, runner, closeAutomations, err := buildApp(sess, res, toolsOn, agentState, resumeSessionName)
	if err != nil {
		return err
	}
	// Release every pooled session's context lease on the way out. The chat
	// surface's own defer covers only the primary startup session; without
	// this, any session resumed in the TUI kept a fresh lease behind and the
	// next process's resume was refused until the lease TTL ran out.
	defer func() {
		// chatsync.RecommendedStopTimeout, not a shorter ad-hoc value: this ctx
		// also bounds each pooled session's final chat-sync flush
		// (SessionPool.ReleaseLeases), and a real network round trip carrying
		// a real backlog needs a genuine chance to finish before the process
		// exits kills it - see the constant's doc comment.
		ctx, cancel := context.WithTimeout(context.Background(), chatsync.RecommendedStopTimeout)
		defer cancel()
		runner.Pool().ReleaseLeases(ctx)
		runner.Pool().CloseAll()
	}()
	// Registered after the pool defer, so it runs first (defers are
	// LIFO). The automation Service cancels its in-flight runs and
	// settles their rows while the pooled sessions they drive are still
	// open. Without this, quitting mid-run left the row running and its
	// claim held. Every later trigger was then refused as already
	// running until a CLI sweep.
	defer closeAutomations()

	p := newTeaProgram(root)
	wireMouseNotifier(settingsStore, p)
	wireFullDiskNotifier(settingsStore, p)
	_, err = p.Run()
	return err
}

// wireMouseNotifier bridges the Settings screen's "mouse capture" row
// into the running program: it pushes each change so it takes effect
// on the next frame (app.MouseCaptureMsg flips View().MouseMode, and
// the renderer writes ?1002/?1006). Send is a no-op once the program
// stops. A nil store (buildApp could not produce one) skips wiring.
func wireMouseNotifier(store *uiadapter.SettingsStore, p *tea.Program) {
	if store != nil {
		store.SetMouseNotifier(func(on bool) {
			go p.Send(app.MouseCaptureMsg{On: on})
		})
	}
}

// wireFullDiskNotifier bridges the Settings screen's "full disk" toggle
// into the running program: a live re-arm pushes the never-silent
// disclosure (app.SettingsNoticeMsg) so it lands in the conversation
// transcript as a permanent notice. Send is a no-op once the program
// stops. A nil store skips wiring.
func wireFullDiskNotifier(store *uiadapter.SettingsStore, p *tea.Program) {
	if store != nil {
		store.SetFullDiskNotifier(func(text string) {
			go p.Send(app.SettingsNoticeMsg{Text: text})
		})
	}
}

// wireSyncOptsNotifier bridges the Settings screen's three [sync]
// opt-out toggles into the live chat-sync projector: a Settings ->
// General operator action that flips include_thinking,
// include_tool_io, or stream_assistant fires this notifier, which
// fans out to every attached SyncSession in the pool. The pool call
// runs synchronously and is bounded by the number of attached
// sessions (one per logged-in chat), so a "go" wrapper is not needed
// to keep the SaveHandle loop responsive.
//
// A nil store or nil pool skips wiring: the operator's toggle still
// persists to disk via UpdateGeneralConfig; only the live re-arm
// half is silent. The integration tests in
// settings_persist_integration_test.go pin both halves.
func wireSyncOptsNotifier(store *uiadapter.SettingsStore, pool *uiadapter.SessionPool) {
	if store == nil || pool == nil {
		return
	}
	store.SetSyncOptsNotifier(func(includeThinking, includeToolIO, streamAssistant bool) {
		pool.ApplySyncOpts(includeThinking, includeToolIO, streamAssistant)
	})
}

// mouseEnabled resolves the startup mouse-capture decision:
// MIVIA_MOUSE overrides [tui] mouse, which defaults to true. Capture ON
// means in-app drag-select and wheel scrolling work from the first
// frame; native terminal selection stays reachable through the
// per-terminal override key (shown in the help overlay) and the live
// Settings toggle.
func mouseEnabled(res *config.Resolved, env []string) bool {
	on := true
	if res != nil && res.TUI.Mouse != nil {
		on = *res.TUI.Mouse
	}
	for _, kv := range env {
		if len(kv) > len("MIVIA_MOUSE=") && kv[:len("MIVIA_MOUSE=")] == "MIVIA_MOUSE=" {
			return config.ParseTruthyEnv(kv[len("MIVIA_MOUSE="):])
		}
	}
	return on
}

// loadThemes combines embedded and user themes, indirected so tests can
// force loader errors (the compiled-in embed.FS itself cannot be corrupted
// in-process).
var loadEmbeddedThemes = theme.Embedded
var loadUserThemes = theme.LoadUserDir
var userThemesDir = config.UserThemesDir
var loadThemes = loadAllThemes

func loadAllThemes() ([]theme.Theme, error) {
	themes, err := loadEmbeddedThemes()
	if err != nil {
		return nil, err
	}
	if dir := userThemesDir(); dir != "" {
		user, err := loadUserThemes(dir)
		if err != nil {
			return nil, err
		}
		themes = append(themes, user...)
	}
	return themes, nil
}

func chooseTheme(themes []theme.Theme, name string) (theme.Theme, error) {
	if name != "" {
		for _, th := range themes {
			if th.Name == name {
				return th, nil
			}
		}
	}
	for _, th := range themes {
		if th.Name == "mivia-dark" {
			return th, nil
		}
	}
	return theme.Theme{}, fmt.Errorf("theme: default theme mivia-dark is not available")
}

func persistTheme(store *uiadapter.SettingsStore, name string) tea.Cmd {
	return func() tea.Msg {
		if err := store.PersistTheme(name); err != nil {
			return app.SettingsNoticeMsg{Text: "theme save failed: " + err.Error()}
		}
		return nil
	}
}

// newTeaProgram is tea.NewProgram, indirected so a test can run RunTUI
// headless: with the default options the program reads the process's real
// stdin, and on windows Run() does not fail fast off-TTY - it runs forever
// (the verify-windows 10-minute timeout hang). Tests substitute explicit
// non-TTY input/output and quit the program themselves.
var newTeaProgram = func(root tea.Model) *tea.Program { return tea.NewProgram(root) }

// automationSessionSpawner adapts *uiadapter.SessionPool to
// automation.SessionSpawner, the standard Go composition-root adapter:
// this package (internal/newtui) is the only place that names both
// uiadapter.BindFunc (a named type) and automation.SessionSpawner's
// plain-func parameter signatures, converting between them (a named
// func type is not assignable to a plain func parameter). A struct
// wrapping *uiadapter.SessionPool, not a bare func type, so it can also
// implement SetApprovalOverride - a bare func type can only ever
// satisfy a single-method interface.
type automationSessionSpawner struct {
	pool *uiadapter.SessionPool
}

func (a automationSessionSpawner) CreateFreshInDir(bind func(*chat.Session) (string, error), dir string) (ports.Conversation, error) {
	// Background spawn: an automation run must not share the process-wide
	// SubagentProgressRegistrar or the pool's single-slot tool-scope
	// notice with whatever the foreground TUI is doing (see
	// CreateFreshBackgroundInDir's doc comment).
	return a.pool.CreateFreshBackgroundInDir(uiadapter.BindFunc(bind), dir)
}

// GetOrResumeInDir returns the pooled or restored session for id, READY
// for turns: the pool already restored history on the miss path, so the
// caller must NOT call Load again. dir scopes worktree resolution
// exactly as CreateFreshInDir's. bind stays nil here:
// GetOrCreateInDir's short-circuit, join, and live-entry branches never
// invoke it, and a capture closure would blur that contract.
func (a automationSessionSpawner) GetOrResumeInDir(id string, dir string) (ports.Conversation, *chat.Session, error) {
	conv, err := a.pool.GetOrCreateInDir(id, nil, dir)
	if err != nil {
		return nil, nil, err
	}
	c, ok := conv.(*uiadapter.Conversation)
	if !ok {
		return nil, nil, fmt.Errorf("automation spawner: pooled conversation is %T, want *uiadapter.Conversation", conv)
	}
	return conv, c.Session(), nil
}

func (a automationSessionSpawner) SetApprovalOverride(sessionID string, gate func(ctx context.Context, name string, args json.RawMessage) sdkadapter.ApprovalResult, policy string) error {
	return a.pool.SetApprovalOverride(sessionID, gate, policy)
}

// newAutomationSpawner builds the spawn value wireAutomationBackend
// installs on automation.Service: it adapts pool's BindFunc-typed
// CreateFreshInDir (and its SetApprovalOverride) to
// automation.SessionSpawner's plain-func signatures (see
// automationSessionSpawner's own doc comment). A struct value, not a
// func literal, so it is directly callable from a test without needing
// a live executor to invoke it first.
func newAutomationSpawner(pool *uiadapter.SessionPool) automation.SessionSpawner {
	return automationSessionSpawner{pool: pool}
}

// automationCloseTimeout bounds Service.Close on TUI exit. A run that
// does not stop in time is marked interrupted and its claim released.
const automationCloseTimeout = 5 * time.Second

// wireAutomationBackend constructs the concrete automation.Service,
// sweeps runs an earlier process left running, and installs the
// Service on store via SetAutomationBackend. It adapts pool's
// BindFunc-typed CreateFreshInDir to automation.SessionSpawner's plain-
// func signature (see automationSessionSpawner's own doc comment).
// Failure is non-fatal: the Automations settings section stays unbacked
// and the TUI starts normally. The returned closer calls Service.Close
// with a bounded context. It is a no-op when wiring failed, so RunTUI
// can always defer it.
//
// sess.ContextStore() normally already holds the *storage.SQLite the
// root session opened at startup (openContextStore, internal/clichat),
// itself resolved through cli.ContextStorePath(root, res.Subagents) -
// the SAME store `mivia automations` opens (openAutomationStore,
// internal/cliautomations), so the common case needs no extra work
// here. When it is not a *storage.SQLite (a session whose context store
// was never wired, or wired to something else), a nil db is passed to
// automation.New instead of a fallback open, and internal/automation's
// own db==nil guards (runstore.go) turn every run-persistence call into
// a silent no-op: the TUI would start with a working Automations
// settings UI but no run history ever recorded for a run it started,
// and nothing would surface the gap. Open the same shared store
// directly instead, so the Service always has a real backing store
// when one is resolvable, and own its lifecycle (opened here, closed
// by the returned closer) since sess itself never held this handle.
func wireAutomationBackend(store *uiadapter.SettingsStore, pool *uiadapter.SessionPool, sess *chat.Session, agentState *cli.AgentSessionState, res *config.Resolved) (closeFn func()) {
	db, ok := sess.ContextStore().(*storage.SQLite)
	var ownedDB *storage.SQLite
	// An empty WorkspaceRoot is a "no workspace" condition automation.New
	// rejects below anyway (see its own root=="" guard); skip the store
	// open entirely rather than resolving a path against an empty root.
	if !ok && agentState.WorkspaceRoot != "" {
		opened, openErr := storage.OpenSQLite(cli.ContextStorePath(agentState.WorkspaceRoot, res.Subagents))
		if openErr != nil {
			log.Printf("automations disabled: open context store: %v", openErr) // non-fatal; TUI starts normally
			return func() {}
		}
		db, ownedDB = opened, opened
	}
	spawn := newAutomationSpawner(pool)
	autoSvc, err := automation.New(agentState.WorkspaceRoot, db, spawn, automation.Config{})
	if err != nil {
		log.Printf("automations disabled: %v", err) // non-fatal; TUI starts normally
		if ownedDB != nil {
			_ = ownedDB.Close()
		}
		return func() {}
	}
	// Same startup sweep as `mivia automations serve`
	// (internal/cliautomations/serve_cmd.go): a sweep failure must not
	// block the TUI, so it is logged, not returned.
	if n, sweepErr := autoSvc.SweepInterrupted(context.Background()); sweepErr != nil {
		log.Printf("automations: sweep interrupted runs at startup: %v", sweepErr)
	} else if n > 0 {
		log.Printf("automations: swept %d interrupted run(s) at startup", n)
	}
	store.SetAutomationBackend(autoSvc)
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), automationCloseTimeout)
		defer cancel()
		if err := autoSvc.Close(ctx); err != nil {
			log.Printf("automations: close: %v", err)
		}
		if ownedDB != nil {
			_ = ownedDB.Close()
		}
	}
}

// buildApp assembles the root model. The returned closer shuts the
// automation Service down; RunTUI defers it before the pool teardown.
func buildApp(sess *chat.Session, res *config.Resolved, toolsOn bool, agentState *cli.AgentSessionState, resumeSessionName string) (tea.Model, *uiadapter.SettingsStore, *uiadapter.CommandRunner, func(), error) {
	registerSubagentProgress()
	approver := uiadapter.NewApprover(sess)
	themes, err := loadThemes()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	themeName := ""
	if res != nil {
		themeName = res.TUI.Theme
	}
	th, err := chooseTheme(themes, themeName)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	// runner owns the one SessionPool for this process; sourcing conv and
	// threads FROM it (rather than constructing a separate Conversation
	// and SubagentThreads registry here) keeps every later session switch
	// (/resume, /new) wired to the same registry the screen holds. Two
	// separately-built Conversation objects for the same initial session
	// used to leave the pooled twin unwired - see SessionPool tests.
	runner := uiadapter.NewCommandRunner(sess, res, agentState)
	pool := runner.Pool()
	threads := pool.Threads()
	// NewCommandRunner's pool pre-registers sess under its own SessionID
	// (NewSessionPool) as a *uiadapter.Conversation (NewConversation's
	// own return type), so this lookup always hits that entry - never
	// GetOrCreate's construction path - and is always that concrete type.
	convPort, _ := pool.GetOrCreate(sess.SessionID)
	conv := convPort.(*uiadapter.Conversation)

	settingsStore := uiadapter.NewSettingsStore(sess, res, agentState)
	settingsStore.SetConversation(conv)
	closeAutomations := wireAutomationBackend(settingsStore, pool, sess, agentState, res)
	wireSyncOptsNotifier(settingsStore, pool)
	runner.SetSettingsStore(settingsStore)
	env := os.Environ()
	tier := theme.Detect(os.Stdout, env)
	screen := conversation.New(th, tier, themes, conv, approver, 80, nil)

	screen.SetCommands(runner.Commands())
	screen.SetCommandRunner(runner)
	screen.SetSubagentThreads(threads)
	screen.SetSettings(settingsStore.Settings())
	// pool.RemoteInputs() fans in every pooled session's chatsync-validated
	// remote input (internal/uiadapter/remote_input.go); the screen is the
	// sole thing that ever turns one into a conv.Send call (item 1 of the
	// steering design - see poolSyncOptions' comment for the full rationale).
	screen.SetRemoteInputs(pool.RemoteInputs())
	// pool.Notices() is the out-of-turn advisory stream (ports.Notices):
	// chat-sync lifecycle lines, and every workflow progress transition
	// (internal/uiadapter/workflow_notices.go). It had no reader here at all,
	// so a workflow run started with workflow_run showed the operator nothing
	// after the tool call returned, however long the run went on.
	screen.SetNotices(pool.Notices())
	// pool.WorkflowStatus() is the replaceable liveness stream that keeps the
	// status row honest between a step's start and its end. It is separate
	// from Notices on purpose: heartbeats arrive every 15s per running step,
	// and queuing them as advisories evicts the transitions worth reading.
	screen.SetWorkflowStatus(pool.WorkflowStatus())
	screen.SetSessionMounter(runner)
	pool.StartBackgroundWatch(context.Background())

	report := termprobe.Probe(env, "")
	// The help overlay names the detected terminal's own key for
	// overriding mouse capture (rule 7.5); empty clears the line.
	screen.SetMouseOverrideHint(report.MouseHint)

	root := app.New(screen, th, tier, themes).WithOptions(app.Options{
		Mouse:        mouseEnabled(res, env),
		FullRepaint:  report.FullRepaint,
		PersistTheme: func(name string) tea.Cmd { return persistTheme(settingsStore, name) },
	})

	return root, settingsStore, runner, closeAutomations, nil
}
