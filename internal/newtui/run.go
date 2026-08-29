package newtui

import (
	"fmt"
	"io"
	"log"
	"os"

	tea "charm.land/bubbletea/v2"
	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
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

	root, settingsStore, err := buildApp(sess, res, toolsOn, agentState, resumeSessionName)
	if err != nil {
		return err
	}

	p := tea.NewProgram(root)
	// The Settings screen's "mouse capture" row persists through the
	// store; this bridge pushes each change into the running program so
	// it takes effect on the next frame (app.MouseCaptureMsg flips
	// View().MouseMode, and the renderer writes ?1002/?1006). Send is a
	// no-op once the program stops.
	if settingsStore != nil {
		settingsStore.SetMouseNotifier(func(on bool) {
			go p.Send(app.MouseCaptureMsg{On: on})
		})
	}
	_, err = p.Run()
	return err
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

func buildApp(sess *chat.Session, res *config.Resolved, toolsOn bool, agentState *cli.AgentSessionState, resumeSessionName string) (tea.Model, *uiadapter.SettingsStore, error) {
	registerSubagentProgress()
	approver := uiadapter.NewApprover(sess)
	themes, err := theme.Embedded()
	if err != nil {
		return nil, nil, err
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
	convPort, err := pool.GetOrCreate(sess.SessionID)
	if err != nil {
		return nil, nil, err
	}
	conv, ok := convPort.(*uiadapter.Conversation)
	if !ok {
		return nil, nil, fmt.Errorf("newtui: pool returned unexpected conversation type %T", convPort)
	}

	settingsStore := uiadapter.NewSettingsStore(sess, res, agentState)
	settingsStore.SetConversation(conv)
	runner.SetSettingsStore(settingsStore)
	screen := conversation.New(th, theme.TierTrueColor, themes, conv, approver, 80, nil)

	screen.SetCommands(runner.Commands())
	screen.SetCommandRunner(runner)
	screen.SetSubagentThreads(threads)
	screen.SetSettings(settingsStore.Settings())

	env := os.Environ()
	report := termprobe.Probe(env, "")
	// The help overlay names the detected terminal's own key for
	// overriding mouse capture (rule 7.5); empty clears the line.
	screen.SetMouseOverrideHint(report.MouseHint)

	root := app.New(screen, th, theme.TierTrueColor, themes).WithOptions(app.Options{
		Mouse:       mouseEnabled(res, env),
		FullRepaint: report.FullRepaint,
	})

	return root, settingsStore, nil
}
