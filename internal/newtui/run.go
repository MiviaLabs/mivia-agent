package newtui

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"

	tea "charm.land/bubbletea/v2"
	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/screen/conversation"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
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

	root, settingsStore, runner, err := buildApp(sess, res, toolsOn, agentState, resumeSessionName)
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

func buildApp(sess *chat.Session, res *config.Resolved, toolsOn bool, agentState *cli.AgentSessionState, resumeSessionName string) (tea.Model, *uiadapter.SettingsStore, *uiadapter.CommandRunner, error) {
	registerSubagentProgress()
	approver := uiadapter.NewApprover(sess)
	themes, err := loadThemes()
	if err != nil {
		return nil, nil, nil, err
	}
	themeName := ""
	if res != nil {
		themeName = res.TUI.Theme
	}
	th, err := chooseTheme(themes, themeName)
	if err != nil {
		return nil, nil, nil, err
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

	return root, settingsStore, runner, nil
}
