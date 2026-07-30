package cli

import (
	"fmt"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

// Router → registry: find keys the handlers really act on.
//
// validateKeyRegistry checks that the registry is internally sound, and
// TestRegisteredChatKeysAreReallyBound checks registry → router. Neither
// catches the direction that produces undocumented keys: a handler quietly
// acting on a key nobody declared, so /help never mentions it. These probes
// drive a candidate key universe through each surface and report every key
// that changes observable state.

type keyProbe struct {
	scope keyScope
	key   string
}

// candidateKeys is the universe probed. It is deliberately wider than the
// registry: the point is to discover bindings nobody wrote down.
var candidateKeys = []string{
	"up", "down", "left", "right", "pgup", "pgdown", "home", "end",
	"shift+home", "shift+end", "enter", "esc", "tab", "shift+tab", " ",
	"backspace", "delete", "f1", "f2", "f3",
	"a", "b", "d", "f", "g", "j", "k", "n", "o", "q", "u", "y",
	"G", "P", "N", "Y",
	"ctrl+a", "ctrl+c", "ctrl+d", "ctrl+e", "ctrl+g", "ctrl+k", "ctrl+l",
	"ctrl+o", "ctrl+q", "ctrl+r", "ctrl+t", "ctrl+u", "ctrl+v", "ctrl+w",
	"ctrl+y", "ctrl+left", "ctrl+right",
}

// fingerprint captures the state a probe watches for change.
func fingerprint(m *tuiModel) string {
	dash := ""
	if m.runDash != nil {
		dash = fmt.Sprintf("%d/%v", m.runDash.selectedIdx, m.runDash.isOpen())
	}
	dlg := ""
	if m.sessionsDlg != nil {
		dlg = fmt.Sprintf("%d/%d/%s/%d", m.sessionsDlg.cursor, m.sessionsDlg.confirm, m.sessionsDlg.notice, m.sessionsDlg.scroll)
	}
	ov := ""
	if m.overlay != nil {
		ov = fmt.Sprintf("%d", m.overlay.yOffset)
	}
	return fmt.Sprintf("dash=%s dlg=%s ov=%s sel=%d mode=%d focus=%d block=%s mouse=%v draft=%q vp=%d follow=%v",
		dash, dlg, ov, m.sessionSel, m.mode, m.focus, m.selectedBlockID,
		m.mouseEnabled, m.textarea.Value(), m.viewport.YOffset, m.followOutput)
}

// probeSurface drives every candidate key through one surface and returns the
// keys that changed state (or that the handler reports as consumed by acting).
func probeSurface(t *testing.T, scope keyScope, setup func(*tuiModel), drive func(*tuiModel, string)) []keyProbe {
	t.Helper()
	var found []keyProbe
	for _, key := range candidateKeys {
		m := tallScrollModel(t, 6, 50)
		m.waiting = false
		setup(m)
		before := fingerprint(m)
		drive(m, key)
		if fingerprint(m) != before {
			found = append(found, keyProbe{scope: scope, key: key})
		}
	}
	return found
}

// boundKeyProbes reports every (scope, key) the router acts on.
func boundKeyProbes(t *testing.T) []keyProbe {
	t.Helper()
	var all []keyProbe

	// Sessions manager.
	all = append(all, probeSurface(t, scopeSessions, func(m *tuiModel) {
		m.sessions = []chat.SessionInfo{{Name: "one"}, {Name: "two"}}
		m.openSessionsDialog()
	}, func(m *tuiModel, key string) {
		if m.sessionsDlg != nil {
			m.handleSessionsDialogKey(key)
		}
	})...)

	// Block/help/status overlay.
	all = append(all, probeSurface(t, scopeOverlay, func(m *tuiModel) {
		lines := make([]string, 200)
		for i := range lines {
			lines[i] = "line"
		}
		m.overlay = newDialog("probe", lines)
		m.overlay.yOffset = 20 // room to scroll both ways
	}, func(m *tuiModel, key string) {
		if m.overlay != nil {
			m.handleOverlayKey(key)
		}
	})...)

	// Welcome screen.
	all = append(all, probeSurface(t, scopeWelcome, func(m *tuiModel) {
		m.mode = modeWelcome
		m.sessions = []chat.SessionInfo{{Name: "one"}, {Name: "two"}, {Name: "three"}}
		m.sessionSel = 1
	}, func(m *tuiModel, key string) {
		m.handleWelcomeKey(key)
	})...)

	// Run dashboard (visible, transcript focused — the only state in which it
	// owns keys).
	all = append(all, probeSurface(t, scopeDashboard, func(m *tuiModel) {
		m.runDash = newRunDashboard()
		m.runDash.handleEvent(ledger.LifecycleEvent{RunID: "r1", Kind: "run_created"})
		m.runDash.handleEvent(ledger.LifecycleEvent{RunID: "r2", Kind: "run_created"})
		m.runDash.toggleOpen()
		m.setFocus(focusScrollback)
	}, func(m *tuiModel, key string) {
		if m.runDash != nil && m.runDash.isVisible() && m.focus == focusScrollback {
			switch key {
			case "up", "down":
				m.handleChatKey(key, false)
			}
		}
	})...)

	return all
}
