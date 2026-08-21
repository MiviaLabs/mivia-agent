package legacytui

import (
	"errors"
	"os"
	"runtime"
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
	updateMessageImpl = func(model *TUIModel, _ tea.Msg) (tea.Model, tea.Cmd) {
		model.restartWorkspace = t.TempDir()
		model.resumeSessionName = "resume"
		model.restartWorktreeInstance = expected
		return model, tea.Quit
	}
	inputWriter, input, err := pty.Open()
	if err != nil {
		// creack/pty is Unix-only; on Windows a pipe input is enough because
		// the injected updateMessageImpl short-circuits the program before
		// any real input is read.
		if runtime.GOOS != "windows" {
			t.Fatal(err)
		}
		reader, pipeWriter, pipeErr := os.Pipe()
		if pipeErr != nil {
			t.Fatal(pipeErr)
		}
		input = reader
		inputWriter = pipeWriter
	}
	oldInput := os.Stdin
	// On Unix the pty slave is also made the process stdin so any code that
	// probes the controlling terminal sees the pty. On Windows the pipe must
	// NOT become os.Stdin: bubbletea treats an input whose fd equals
	// os.Stdin's fd as console input and then tries to open CONIN$, which
	// does not exist under a CI runner. Leaving os.Stdin alone routes the
	// pipe through the plain cancel-reader path instead.
	if runtime.GOOS != "windows" {
		os.Stdin = input
	}
	origInputOption := tuiInputOption
	tuiInputOption = func() tea.ProgramOption { return tea.WithInput(input) }
	t.Cleanup(func() {
		tuiInputOption = origInputOption
		os.Stdin = oldInput
		_ = input.Close()
		if inputWriter != nil {
			_ = inputWriter.Close()
		}
	})
	session := chat.NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, nullCompleter{})
	err = RunTUI(session, &config.Resolved{}, false, nil, "")
	var restart *WorkspaceRestart
	if !errors.As(err, &restart) {
		t.Fatalf("runTUI error = %v, want workspace restart", err)
	}
	if restart.ResumeSessionName != "resume" || restart.WorktreeInstance != expected {
		t.Fatalf("workspace restart = %+v", restart)
	}
}
