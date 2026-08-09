package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	tea "github.com/charmbracelet/bubbletea"
)

// workerWaitTimeout bounds how long runTUI blocks on agent workers after the
// tea program exits. Hung tools must not pin process exit forever.
const workerWaitTimeout = 15 * time.Second

// workspaceRestart ends the current TUI so the chat command can construct a
// new session rooted in dir. It avoids mutating live tools and hooks in place.
type workspaceRestart struct {
	dir, resumeSessionName string
	worktreeInstance       contextstate.WorktreeInstance
}

func (e *workspaceRestart) Error() string {
	return "restart chat in workspace " + e.dir
}

func runTUI(sess *chat.Session, res *config.Resolved, toolsOn bool, agentState *agentSessionState, resumeSessionName string) error {
	defer func() {
		err := sess.SaveLast()
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠ auto-save failed: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "✓ session auto-saved\n")
		}
		writeAutosaveStatus(sess.SessionDir, err)
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

	// MetricsAdapter: subscribes to all event bus events for diagnostics.
	metricsAdapter := events.NewMetricsAdapter()
	metricsAdapter.Subscribe(bus)
	// Mouse: enable cell-motion at Program start when available (not via Init).
	opts := []tea.ProgramOption{tea.WithAltScreen()}
	if model.mouseEnabled {
		opts = append(opts, tea.WithMouseCellMotion())
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
		return &workspaceRestart{dir: model.restartWorkspace, resumeSessionName: model.resumeSessionName, worktreeInstance: model.restartWorktreeInstance}
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

// sessionInvocationSink adapts dispatcher invocation lifecycle events to the
// session event bus. The bus is read when the event publishes because the
// interactive dispatcher is attached before runTUI creates the bus. A nil
// bus (REPL, one-shot, tests) makes the sink a no-op. The closure is safe
// for concurrent callers: bus.Publish is goroutine-safe.
func sessionInvocationSink(sess *chat.Session) func(runtime.Event) {
	return func(e runtime.Event) {
		var bus *events.Bus
		if sess != nil {
			bus = sess.EventBus
		}
		if bus == nil {
			return
		}
		bus.Publish(invocationEvent(e))
	}
}

// invocationEvent maps one runtime lifecycle observation to a session bus
// event. The three lifecycle kinds map by name; any other type is terminal
// and surfaces as completed.
func invocationEvent(e runtime.Event) events.Event {
	kind := events.KindInvocationCompleted
	switch e.Type {
	case "started":
		kind = events.KindInvocationStarted
	case "retrying":
		kind = events.KindInvocationRetrying
	}
	return events.Event{
		Kind:      kind,
		Timestamp: time.Now(),
		Name:      e.Metadata.Name,
		Detail:    e.Metadata.Kind + " " + e.Metadata.Status,
		Metadata: map[string]string{
			"id":     e.Metadata.ID,
			"turn":   e.Metadata.TurnID,
			"parent": e.Metadata.ParentID,
		},
		AgentTask:  e.Metadata.ID,
		AgentName:  e.Metadata.Name,
		AgentDepth: 1,
	}
}
