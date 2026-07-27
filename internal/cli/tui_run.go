package cli

import (
	"fmt"
	"os"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	tea "github.com/charmbracelet/bubbletea"
)

func runTUI(sess *chat.Session, res *config.Resolved, toolsOn bool) error {
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
	// EventBus: agent loop dual-publishes for extensibility (hooks, future
	// Program.Send). TUI live content is bridge drain (FinalWriter + OnEvent).
	bus := events.New()
	model.eventBus = bus
	sess.EventBus = bus
	model.uiAdapter = NewUIAdapter(bus, model.bridge)
	SetGlobalBus(bus)
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
	model.workerWG.Wait()
	model.bridge.Close()
	bus.Close()
	return err
}
