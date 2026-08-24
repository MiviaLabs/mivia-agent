package newtui

import (
	"io"
	"log"

	tea "charm.land/bubbletea/v2"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/screen/conversation"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
)

// RunTUI is the alternative launcher that wires the new Mivia UI.
func RunTUI(sess *chat.Session, res *config.Resolved, toolsOn bool, agentState *cli.AgentSessionState, resumeSessionName string) error {
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
	conv := uiadapter.NewConversation(sess)
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

	threads := uiadapter.NewSubagentThreads()
	settingsStore := uiadapter.NewSettingsStore(sess, res, agentState)

	runner := uiadapter.NewCommandRunner(sess, res, agentState)
	screen := conversation.New(th, theme.TierTrueColor, themes, conv, approver, 80, nil)
	screen.SetCommands(uiadapter.DefaultCommands())
	screen.SetCommandRunner(runner)
	screen.SetSubagentThreads(threads)
	screen.SetSettings(settingsStore.Settings())

	root := app.New(screen, th, theme.TierTrueColor, themes).WithOptions(app.Options{
		Mouse: true,
	})

	return root, nil
}
