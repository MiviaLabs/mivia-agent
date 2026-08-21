package legacytui

import (
	"fmt"
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// Router → registry: find keys the handlers really act on.
//
// cli.ValidateKeyRegistry checks that the registry is internally sound, and
// TestRegisteredChatKeysAreReallyBound checks registry → router. Neither
// catches the direction that produces undocumented Keys: a handler quietly
// acting on a key nobody declared, so /help never mentions it. These probes
// drive a candidate key universe through each surface and report every key
// that changes observable state.

type keyProbe struct {
	scope cli.KeyScope
	key   string
}

// candidateKeys is the universe probed. It is deliberately wider than the
// registry: the point is to discover bindings nobody wrote down.
var candidateKeys = []string{
	"up", "down", "left", "right", "pgup", "pgdown", "home", "end",
	"shift+home", "shift+end", "enter", "esc", "tab", "shift+tab", " ",
	"backspace", "delete", "f1", "f2", "f3",
	"a", "b", "d", "e", "f", "g", "j", "k", "n", "o", "q", "u", "y",
	"G", "P", "N", "Y",
	"ctrl+a", "ctrl+c", "ctrl+d", "ctrl+e", "ctrl+g", "ctrl+k", "ctrl+l",
	"ctrl+n", "ctrl+o", "ctrl+p", "ctrl+q", "ctrl+r", "ctrl+t", "ctrl+u", "ctrl+v", "ctrl+w",
	"ctrl+y", "ctrl+left", "ctrl+right", "ctrl+up", "ctrl+down",
}

// fingerprint captures the state a probe watches for change.
func fingerprint(m *TUIModel) string {
	dash := ""
	if m.runDash != nil {
		dash = fmt.Sprintf("%d/%v", m.runDash.selectedIdx, m.runDash.isOpen())
	}
	sidebar := ""
	if m.sessionsSidebar != nil {
		sidebar = fmt.Sprintf("%d/%d/%s/%d", m.sessionsSidebar.cursor, m.sessionsSidebar.confirm, m.sessionsSidebar.notice, m.sessionsSidebar.scroll)
	}
	workflows := ""
	if m.workflowsSidebar != nil {
		workflows = fmt.Sprintf("%d/%d", m.workflowsSidebar.cursor, m.workflowsSidebar.scroll)
	}
	ov := ""
	if m.overlay != nil {
		ov = fmt.Sprintf("%d", m.overlay.yOffset)
	}
	queue := fmt.Sprintf("%v/%d/%d/%v", m.queueMgr.open, m.queueMgr.selected, len(m.pendingQueue), m.editingQueued)
	return fmt.Sprintf("dash=%s sidebar=%s workflows=%s ov=%s queue=%s suggest=%v/%d/%d sel=%d mode=%d focus=%d block=%s mouse=%v draft=%q vp=%d follow=%v",
		dash, sidebar, workflows, ov, queue, m.suggest.open, len(m.suggest.commands), m.suggest.selected, m.sessionSel, m.mode, m.focus, m.selectedBlockID,
		m.mouseEnabled, m.textarea.Value(), m.viewport.YOffset, m.followOutput)
}

// probeSurface drives every candidate key through one surface and returns the
// keys that changed state (or that the handler reports as consumed by acting).
func probeSurface(t *testing.T, scope cli.KeyScope, setup func(*TUIModel), drive func(*TUIModel, string)) []keyProbe {
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

	// Slash suggestion popup.
	all = append(all, probeSurface(t, cli.ScopeSuggest, func(m *TUIModel) {
		m.setFocus(cli.FocusComposer)
		m.textarea.SetValue("/")
		m.textarea.SetCursor(1)
		m.syncSuggest()
	}, func(m *TUIModel, key string) {
		m.handleSuggestKey(key)
	})...)

	// Sessions manager (non-modal sidebar). The sidebar owns a small key set;
	// driving handleChatKey would attribute every global key to this scope.
	all = append(all, probeSurface(t, cli.ScopeSessions, func(m *TUIModel) {
		m.sessions = []chat.SessionInfo{{Name: "one"}, {Name: "two"}}
		m.sessionsSidebar = newSessionsSidebar()
		m.setFocus(cli.FocusSidebar)
	}, func(m *TUIModel, key string) {
		m.handleSidebarKey(key)
	})...)

	// Workflows manager (non-modal right sidebar).
	all = append(all, probeSurface(t, cli.ScopeWorkflows, func(m *TUIModel) {
		m.width = 100
		m.workflowsSidebar = newWorkflowsSidebar()
		m.workflowsSidebar.rows = []workflowRunRow{
			{run: workflowledger.RunSnapshot{RunID: "wfr-1", WorkflowName: "one", Status: workflowledger.RunStatusRunning}},
			{run: workflowledger.RunSnapshot{RunID: "wfr-2", WorkflowName: "two", Status: workflowledger.RunStatusPending}},
		}
		m.setFocus(cli.FocusWorkflowsSidebar)
	}, func(m *TUIModel, key string) {
		m.handleWorkflowsSidebarKey(key)
	})...)

	// Block/help/status overlay.
	all = append(all, probeSurface(t, cli.ScopeOverlay, func(m *TUIModel) {
		lines := make([]string, 200)
		for i := range lines {
			lines[i] = "line"
		}
		m.overlay = newDialog("probe", lines)
		m.overlay.yOffset = 20 // room to scroll both ways
	}, func(m *TUIModel, key string) {
		if m.overlay != nil {
			m.handleOverlayKey(key)
		}
	})...)

	// Welcome screen.
	all = append(all, probeSurface(t, cli.ScopeWelcome, func(m *TUIModel) {
		m.mode = modeWelcome
		m.sessions = []chat.SessionInfo{{Name: "one"}, {Name: "two"}, {Name: "three"}}
		m.sessionSel = 1
	}, func(m *TUIModel, key string) {
		m.handleWelcomeKey(key)
	})...)

	// Run dashboard (visible, transcript focused - the only state in which it
	// owns keys).
	all = append(all, dashboardScopeProbes(t)...)

	// Queue manager (modal popup).
	all = append(all, queueScopeProbes(t)...)

	return all
}

// dashboardScopeProbes drives candidate keys through the visible run
// dashboard while the transcript side has focus.
func dashboardScopeProbes(t *testing.T) []keyProbe {
	return probeSurface(t, cli.ScopeDashboard, func(m *TUIModel) {
		m.runDash = newRunDashboard()
		m.runDash.handleEvent(ledger.LifecycleEvent{RunID: "r1", Kind: "run_created"})
		m.runDash.handleEvent(ledger.LifecycleEvent{RunID: "r2", Kind: "run_created"})
		m.runDash.toggleOpen()
		m.setFocus(cli.FocusScrollback)
	}, func(m *TUIModel, key string) {
		if m.runDash != nil && m.runDash.isVisible() && m.focus == cli.FocusScrollback {
			switch key {
			case "up", "down":
				m.handleChatKey(key, false)
			}
		}
	})
}

// queueScopeProbes drives candidate keys through the queue manager surface.
// The head item is a slash command so the probe's enter (steer) runs it
// locally via cli.HandleSlash and never starts a real turn or leaks a worker
// goroutine.
func queueScopeProbes(t *testing.T) []keyProbe {
	return probeSurface(t, cli.ScopeQueue, func(m *TUIModel) {
		m.pendingQueue = []string{"/help", "second message"}
		m.pendingQueueLabels = []string{"/help", "second message"}
		m.pendingSkillTurns = []*SkillSlashSpec{nil, nil}
		m.queueMgr = QueueMgrState{open: true, selected: 0}
	}, func(m *TUIModel, key string) {
		m.handleQueueManagerKey(key)
	})
}
