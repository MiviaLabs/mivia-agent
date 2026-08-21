package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	tea "github.com/charmbracelet/bubbletea"
)

// workerWaitTimeout bounds how long runTUI blocks on agent workers after the
// tea program exits. Hung tools must not pin process exit forever.
const workerWaitTimeout = 15 * time.Second

// WorkspaceRestart ends the current TUI so the chat command can construct a
// new session rooted in Dir. It avoids mutating live tools and hooks in place.
type WorkspaceRestart struct {
	Dir, ResumeSessionName string
	WorktreeInstance       contextstate.WorktreeInstance
}

func (e *WorkspaceRestart) Error() string {
	return "restart chat in workspace " + e.Dir
}

// tuiInputOption is a test seam. When set, runTUI passes the option it
// returns to the tea program, so a test can supply a deterministic input
// (a pty on Unix, a pipe on Windows). Without it, bubbletea falls back to
// opening the controlling terminal when stdin is not one, which does not
// exist under a Windows CI runner. The seam is never set in production.
var tuiInputOption func() tea.ProgramOption

// RunTUI starts the Bubble Tea TUI program for sess and blocks until it
// exits. Shared with internal/clichat's chat command entry point.
func RunTUI(sess *chat.Session, res *config.Resolved, toolsOn bool, agentState *AgentSessionState, resumeSessionName string) error {
	defer func() {
		err := sess.SaveLast()
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠ auto-save failed: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "✓ session auto-saved\n")
		}
		WriteAutosaveStatus(sess.SessionDir, err)
	}()
	model := newTUIModel(sess, res, toolsOn)
	model.agentState = agentState
	if resumeSessionName != "" {
		if err := model.openSessionByName(resumeSessionName); err != nil {
			return fmt.Errorf("resume session %q: %w", resumeSessionName, err)
		}
	}
	// EventBus: agent loop dual-publishes for extensibility (hooks, future
	// Program.Send). TUI live content is bridge drain (FinalWriter + OnEvent).
	bus := events.New()
	model.eventBus = bus
	sess.EventBus = bus
	model.uiAdapter = NewUIAdapter(bus, model.bridge)
	SetGlobalBus(bus)
	// Rendering another process's turns into the TUI's own display is not
	// implemented yet (nil sink) - joining still publishes this session's
	// own turns to internal/hub, so an observer (e.g. mivia-agent-desktop)
	// sees a terminal TUI's live activity even though the TUI itself
	// doesn't yet render the reverse direction.
	hubHandle := JoinHub(sess, nil)
	defer hubHandle.Leave()

	// MetricsAdapter: subscribes to all event bus events for diagnostics.
	metricsAdapter := events.NewMetricsAdapter()
	metricsAdapter.Subscribe(bus)
	// Mouse: enable cell-motion at Program start when available (not via Init).
	opts := []tea.ProgramOption{tea.WithAltScreen()}
	if model.mouseEnabled {
		opts = append(opts, tea.WithMouseCellMotion())
	}
	if tuiInputOption != nil {
		opts = append(opts, tuiInputOption())
	}
	p := tea.NewProgram(model, opts...)
	_, err := p.Run()
	model.mu.Lock()
	if model.cancel != nil {
		model.cancel()
	}
	model.mu.Unlock()
	waitWorkerGroup(&model.workerWG, workerWaitTimeout)
	model.bridge.Close()
	metricsAdapter.Close()
	bus.Close()
	if model.restartWorkspace != "" {
		return &WorkspaceRestart{Dir: model.restartWorkspace, ResumeSessionName: model.resumeSessionName, WorktreeInstance: model.restartWorktreeInstance}
	}
	return err
}

func waitWorkerGroup(wg interface{ Wait() }, timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		fmt.Fprintf(os.Stderr, "⚠ agent worker still running after %s; exiting without wait\n", timeout)
	}
}
