package legacytui

import (
	"context"
	"fmt"
	"github.com/MiviaLabs/mivia-agent/internal/cli"
)

// handleResumeSlash implements the /resume slash command for the TUI.
// With no argument, lists interrupted runs. With a run ID, shows confirmation
// and sets pendingResume for the user to confirm with 'y'.
func (m *TUIModel) handleResumeSlash(fields []string) bool {
	c := cli.FindCoordinator()
	if c == nil {
		m.appendInfo("no active coordinator (no orchestration runs exist)")
		return true
	}
	if len(fields) < 2 {
		// No argument: list interrupted runs.
		runs, err := cli.ListInterruptedRuns(context.Background(), c)
		if err != nil {
			m.appendBlock(cli.ChatBlock{Kind: cli.ChatBlockSystem, Text: TUIErrorStyle.Render("error listing runs: " + err.Error()), Rendered: TUIErrorStyle.Render("error listing runs: " + err.Error())})
			return true
		}
		msg := cli.FormatListedRuns(runs)
		m.appendInfo(msg)
		return true
	}

	runID := fields[1]

	// Check if the run is resumable.
	runs, err := cli.ListInterruptedRuns(context.Background(), c)
	if err == nil {
		for _, r := range runs {
			if r.RunID == runID {
				if r.HeldByAnotherExecutor {
					m.appendInfo(fmt.Sprintf("cannot resume run %s: held by another executor", runID))
					return true
				}
				info := cli.ResumeConfirmationInfo{
					RunID:       runID,
					DisplayName: r.DisplayName,
				}
				msg := cli.FormatResumeConfirmation(info)
				m.appendInfo(msg)
				m.pendingResume = runID
				return true
			}
		}
	}

	// Run not found in interrupted list or not resumable.
	// Try a direct resume to get the error message.
	d := cli.FindDispatcher()
	_, resumeErr := cli.ResumeRun(context.Background(), c, d, runID, nil)
	if resumeErr != nil {
		m.appendBlock(cli.ChatBlock{Kind: cli.ChatBlockSystem, Text: TUIErrorStyle.Render(cli.FormatResumeError(resumeErr, runID)), Rendered: TUIErrorStyle.Render(cli.FormatResumeError(resumeErr, runID))})
		return true
	}
	m.appendInfo(fmt.Sprintf("run %s resumed", runID))
	return true
}

// handlePendingResumeInput processes the user's response to a resume
// confirmation prompt. 'y' or 'yes' triggers the resume; anything else
// cancels it.
func (m *TUIModel) handlePendingResumeInput(userText string) bool {
	runID := m.pendingResume
	m.pendingResume = ""

	if !cli.ParseConfirmResponse(userText) {
		m.appendInfo("resume cancelled")
		return true
	}

	c := cli.FindCoordinator()
	if c == nil {
		m.appendInfo("no active coordinator (cannot resume)")
		return true
	}

	d := cli.FindDispatcher()
	_, err := cli.ResumeRun(context.Background(), c, d, runID, nil)
	if err != nil {
		m.appendBlock(cli.ChatBlock{Kind: cli.ChatBlockSystem, Text: TUIErrorStyle.Render(cli.FormatResumeError(err, runID)), Rendered: TUIErrorStyle.Render(cli.FormatResumeError(err, runID))})
		return true
	}
	m.appendInfo(fmt.Sprintf("run %s resumed", runID))
	return true
}
