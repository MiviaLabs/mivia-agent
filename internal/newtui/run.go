package newtui

import (
	"context"
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
	}()

	p := newTeaProgram(root)
	wireMouseNotifier(settingsStore, p)
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

// loadThemes is theme.Embedded, indirected so a test can force the
// error return (the compiled-in embed.FS itself cannot be corrupted
// in-process).
var loadThemes = theme.Embedded

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
	var th theme.Theme
	for _, t := range themes {
		if t.Name == "mivia-dark" {
			th = t
			break
		}
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
	runner.SetSettingsStore(settingsStore)
	screen := conversation.New(th, theme.TierTrueColor, themes, conv, approver, 80, nil)

	screen.SetCommands(runner.Commands())
	screen.SetCommandRunner(runner)
	screen.SetSubagentThreads(threads)
	screen.SetSettings(settingsStore.Settings())
	// pool.RemoteInputs() fans in every pooled session's chatsync-validated
	// remote input (internal/uiadapter/remote_input.go); the screen is the
	// sole thing that ever turns one into a conv.Send call (item 1 of the
	// steering design - see poolSyncOptions' comment for the full rationale).
	screen.SetRemoteInputs(pool.RemoteInputs())

	env := os.Environ()
	report := termprobe.Probe(env, "")
	// The help overlay names the detected terminal's own key for
	// overriding mouse capture (rule 7.5); empty clears the line.
	screen.SetMouseOverrideHint(report.MouseHint)

	root := app.New(screen, th, theme.TierTrueColor, themes).WithOptions(app.Options{
		Mouse:       mouseEnabled(res, env),
		FullRepaint: report.FullRepaint,
	})

	return root, settingsStore, runner, nil
}
