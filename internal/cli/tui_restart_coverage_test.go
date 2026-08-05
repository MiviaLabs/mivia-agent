package cli

import (
	"errors"
	"os"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/creack/pty"
)

func TestRunTUIReturnsRequestedWorkspaceRestart(t *testing.T) {
	original := updateMessageImpl
	t.Cleanup(func() { updateMessageImpl = original })
	expected := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	updateMessageImpl = func(model *tuiModel, _ tea.Msg) (tea.Model, tea.Cmd) {
		model.restartWorkspace = t.TempDir()
		model.resumeSessionName = "resume"
		model.restartWorktreeInstance = expected
		return model, tea.Quit
	}
	inputWriter, input, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	oldInput := os.Stdin
	os.Stdin = input
	t.Cleanup(func() {
		os.Stdin = oldInput
		_ = input.Close()
		_ = inputWriter.Close()
	})
	session := chat.NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, nullCompleter{})
	err = runTUI(session, &config.Resolved{}, false, nil, "")
	var restart *workspaceRestart
	if !errors.As(err, &restart) {
		t.Fatalf("runTUI error = %v, want workspace restart", err)
	}
	if restart.resumeSessionName != "resume" || restart.worktreeInstance != expected {
		t.Fatalf("workspace restart = %+v", restart)
	}
}
