package cli

import (
	"context"
	"fmt"
)

// handleResumeSlash implements the /resume slash command for the TUI.
// With no argument, lists interrupted runs. With a run ID, shows confirmation
// and sets pendingResume for the user to confirm with 'y'.
func (m *tuiModel) handleResumeSlash(fields []string) bool {
	c := findCoordinator()
	if c == nil {
		m.appendInfo("no active coordinator (no orchestration runs exist)")
		return true
	}
	if len(fields) < 2 {
		// No argument: list interrupted runs.
		runs, err := listInterruptedRuns(context.Background(), c)
		if err != nil {
			m.appendBlock(ChatBlock{Kind: ChatBlockSystem, Text: tuiErrorStyle.Render("error listing runs: " + err.Error()), Rendered: tuiErrorStyle.Render("error listing runs: " + err.Error())})
			return true
		}
		msg := formatListedRuns(runs)
		m.appendInfo(msg)
		return true
	}

	runID := fields[1]

	// Check if the run is resumable.
	runs, err := listInterruptedRuns(context.Background(), c)
	if err == nil {
		for _, r := range runs {
			if r.RunID == runID {
				if r.HeldByAnotherExecutor {
					m.appendInfo(fmt.Sprintf("cannot resume run %s: held by another executor", runID))
					return true
				}
				info := resumeConfirmationInfo{
					RunID:       runID,
					DisplayName: r.DisplayName,
				}
				msg := formatResumeConfirmation(info)
				m.appendInfo(msg)
				m.pendingResume = runID
				return true
			}
		}
	}

	// Run not found in interrupted list or not resumable.
	// Try a direct resume to get the error message.
	d := findDispatcher()
	_, resumeErr := resumeRun(context.Background(), c, d, runID, nil)
	if resumeErr != nil {
		m.appendBlock(ChatBlock{Kind: ChatBlockSystem, Text: tuiErrorStyle.Render(formatResumeError(resumeErr, runID)), Rendered: tuiErrorStyle.Render(formatResumeError(resumeErr, runID))})
		return true
	}
	m.appendInfo(fmt.Sprintf("run %s resumed", runID))
	return true
}

// handlePendingResumeInput processes the user's response to a resume
// confirmation prompt. 'y' or 'yes' triggers the resume; anything else
// cancels it.
func (m *tuiModel) handlePendingResumeInput(userText string) bool {
	runID := m.pendingResume
	m.pendingResume = ""

	if !parseConfirmResponse(userText) {
		m.appendInfo("resume cancelled")
		return true
	}

	c := findCoordinator()
	if c == nil {
		m.appendInfo("no active coordinator (cannot resume)")
		return true
	}

	d := findDispatcher()
	_, err := resumeRun(context.Background(), c, d, runID, nil)
	if err != nil {
		m.appendBlock(ChatBlock{Kind: ChatBlockSystem, Text: tuiErrorStyle.Render(formatResumeError(err, runID)), Rendered: tuiErrorStyle.Render(formatResumeError(err, runID))})
		return true
	}
	m.appendInfo(fmt.Sprintf("run %s resumed", runID))
	return true
}
