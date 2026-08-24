package newtui

import (
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
	conv := uiadapter.NewConversation(sess)
	approver := uiadapter.NewApprover(sess)
	themes, err := theme.Embedded()
	if err != nil {
		return err
	}
	var th theme.Theme
	for _, t := range themes {
		if t.Name == "mivia-dark" {
			th = t
			break
		}
	}

	runner := uiadapter.NewCommandRunner(sess, res, agentState)
	screen := conversation.New(th, theme.TierTrueColor, themes, conv, approver, 80, nil)
	screen.SetCommands(uiadapter.DefaultCommands())
	screen.SetCommandRunner(runner)

	root := app.New(screen, th, theme.TierTrueColor, themes).WithOptions(app.Options{
		Mouse: true,
	})

	p := tea.NewProgram(root)
	_, err = p.Run()
	return err
}
