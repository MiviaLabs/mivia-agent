package demoharness

import (
	"context"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// SettingsAdapters wires h's General/Providers/MCP/Agents/Automations
// state behind ports.Settings. Go has no method overloading, and each
// section interface names its mutator "Apply" with a different
// argument type - so no single type can implement all five (one type
// cannot own five differently-typed Apply methods). Each field below
// is instead a thin wrapper embedding *Harness for the read methods
// (promoted, no collision - only the names differ there) and adding
// its own single-purpose Apply.
func (h *Harness) SettingsAdapters() ports.Settings {
	return ports.Settings{
		General:     harnessGeneral{h},
		Providers:   harnessProviders{h},
		MCP:         harnessMCP{h},
		Agents:      harnessAgents{h},
		Automations: harnessAutomations{h},
	}
}

var (
	_ ports.GeneralSettings    = harnessGeneral{}
	_ ports.ProviderSettings   = harnessProviders{}
	_ ports.MCPSettings        = harnessMCP{}
	_ ports.AgentSettings      = harnessAgents{}
	_ ports.AutomationSettings = harnessAutomations{}
)

// saveHandle is the fake ports.SaveHandle: it fires Pending then
// Saved/Failed on its own goroutine, so a caller's Events loop
// exercises the same async shape a real adapter's disk write would
// produce.
type saveHandle struct {
	id     string
	events chan ports.SaveEvent
	cancel func()
}

func (h *saveHandle) ID() string                     { return h.id }
func (h *saveHandle) Events() <-chan ports.SaveEvent { return h.events }
func (h *saveHandle) Cancel()                        { h.cancel() }

// newSaveHandle starts a handle that runs apply (under the Harness
// lock) and reports the result. apply returning an error yields
// SaveFailed with that error's text as Message - never a field's raw
// value, per docs/design/settings-screen.md §5.
func (h *Harness) newSaveHandle(apply func() error) ports.SaveHandle {
	h.mu.Lock()
	h.saveSeq++
	id := fmt.Sprintf("save-%d", h.saveSeq)
	h.mu.Unlock()

	ch := make(chan ports.SaveEvent, 4)
	done := make(chan struct{})
	go func() {
		ch <- ports.SaveEvent{State: ports.SavePending}
		select {
		case <-done:
			close(ch)
			return
		default:
		}
		ch <- ports.SaveEvent{State: ports.SaveValidating}
		h.mu.Lock()
		err := apply()
		h.mu.Unlock()
		if err != nil {
			ch <- ports.SaveEvent{State: ports.SaveFailed, Message: err.Error()}
		} else {
			ch <- ports.SaveEvent{State: ports.SaveSaved}
		}
		close(ch)
	}()
	return &saveHandle{id: id, events: ch, cancel: func() { close(done) }}
}

// timeNow is wrapped so settings tests can pin a clock without a
// per-method injection point.
var timeNow = time.Now

// harnessGeneral is the ports.GeneralSettings adapter.
type harnessGeneral struct{ *Harness }

func (g harnessGeneral) General() ports.GeneralView {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.settingsGeneral
}

func (g harnessGeneral) Apply(_ context.Context, _ ports.Scope, e ports.GeneralEdit) (ports.SaveHandle, error) {
	return g.newSaveHandle(func() error { return g.applyGeneral(e) }), nil
}

func (h *Harness) applyGeneral(e ports.GeneralEdit) error {
	switch v := e.(type) {
	case ports.SetTheme:
		h.settingsGeneral.Theme = v.Name
	case ports.SetMouse:
		h.settingsGeneral.Mouse = v.On
	case ports.SetShowReasoning:
		h.settingsGeneral.ShowReasoning = v.On
	case ports.SetScrollLines:
		if v.N <= 0 {
			return fmt.Errorf("scroll lines must be positive")
		}
		h.settingsGeneral.ScrollLines = v.N
	case ports.SetApprovalDefault:
		h.settingsGeneral.ApprovalDefault = v.Mode
	case ports.SetScreenReader:
		h.settingsGeneral.ScreenReader = v.On
	case ports.SetReducedMotion:
		h.settingsGeneral.ReducedMotion = v.On
	default:
		return fmt.Errorf("unknown general edit %T", e)
	}
	return nil
}
