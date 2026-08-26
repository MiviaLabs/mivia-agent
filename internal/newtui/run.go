package newtui

import (
	"fmt"
	"io"
	"log"

	tea "charm.land/bubbletea/v2"
	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/screen/conversation"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
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

	root, err := buildApp(sess, res, toolsOn, agentState, resumeSessionName)
	if err != nil {
		return err
	}

	p := tea.NewProgram(root)
	_, err = p.Run()
	return err
}

func buildApp(sess *chat.Session, res *config.Resolved, toolsOn bool, agentState *cli.AgentSessionState, resumeSessionName string) (tea.Model, error) {
	registerSubagentProgress()
	approver := uiadapter.NewApprover(sess)
	themes, err := theme.Embedded()
	if err != nil {
		return nil, err
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
		return nil, err
	}
	conv, ok := convPort.(*uiadapter.Conversation)
	if !ok {
		return nil, fmt.Errorf("newtui: pool returned unexpected conversation type %T", convPort)
	}

	settingsStore := uiadapter.NewSettingsStore(sess, res, agentState)
	settingsStore.SetConversation(conv)
	runner.SetSettingsStore(settingsStore)
	screen := conversation.New(th, theme.TierTrueColor, themes, conv, approver, 80, nil)

	screen.SetCommands(runner.Commands())
	screen.SetCommandRunner(runner)
	screen.SetSubagentThreads(threads)
	screen.SetSettings(settingsStore.Settings())

	root := app.New(screen, th, theme.TierTrueColor, themes).WithOptions(app.Options{
		Mouse: true,
	})

	return root, nil
}
