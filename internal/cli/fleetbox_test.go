package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

func feedAgents(m *tuiModel, n int) {
	now := time.Now()
	for i := 0; i < n; i++ {
		ev := events.Event{Kind: events.KindSubagentStart, Name: "grep", Detail: "{}"}.
			WithAgentAttribution(fmt.Sprintf("t%d", i), fmt.Sprintf("agent-%d", i), 1)
		m.subagents.Apply(ev, now)
	}
}

func TestFleetBoxVisibilityAndHeight(t *testing.T) {
	m := newReadyChatModel(30, 80)
	m.waiting = true

	// Hidden with no subagents: zero height, nothing rendered.
	if m.fleetBoxVisible() || m.fleetBoxHeight() != 0 {
		t.Fatal("fleet box must be hidden without subagents")
	}
	view := stripANSI(m.View())
	if strings.Contains(view, "┌─ agents") {
		t.Fatalf("fleet box rendered without agents:\n%s", view)
	}

	// Visible with agents while waiting; height = rows + borders.
	feedAgents(m, 2)
	if !m.fleetBoxVisible() {
		t.Fatal("fleet box must show with active subagents")
	}
	if got := m.fleetBoxHeight(); got != 4 {
		t.Fatalf("height=%d want 4 (2 rows + borders)", got)
	}
	view = stripANSI(m.View())
	if !strings.Contains(view, "agent-0") || !strings.Contains(view, "agent-1") {
		t.Fatalf("fleet rows missing:\n%s", view)
	}
	if !strings.Contains(view, "◆") {
		t.Fatalf("fleet rows missing diamonds:\n%s", view)
	}

	// Hidden again once the turn ends — history owns the record.
	m.waiting = false
	if m.fleetBoxVisible() || m.fleetBoxHeight() != 0 {
		t.Fatal("fleet box must hide when the turn ends")
	}
}

func TestFleetBoxCapsRowsExplicitly(t *testing.T) {
	m := newReadyChatModel(40, 90)
	m.waiting = true
	feedAgents(m, 7)
	if got := m.fleetBoxHeight(); got != fleetBoxMaxRows+3 {
		t.Fatalf("height=%d want %d (cap + more line + borders)", got, fleetBoxMaxRows+3)
	}
	box := stripANSI(m.renderFleetBox(90, time.Now()))
	if !strings.Contains(box, "… 3 more") {
		t.Fatalf("cap must be explicit:\n%s", box)
	}
	// Rendered height matches the declared height — layout math depends on it.
	if got := strings.Count(box, "\n") + 1; got != m.fleetBoxHeight() {
		t.Fatalf("rendered %d lines, declared %d", got, m.fleetBoxHeight())
	}
}

func TestLayoutAndViewAgreeWithFleetBox(t *testing.T) {
	// The composer-clipping class of bug: both layout paths must subtract
	// the fleet box identically.
	m := newReadyChatModel(30, 80)
	m.waiting = true
	m.turnStart = time.Now()
	feedAgents(m, 3)
	m.messages = []string{"one", "two"}
	m.layout()
	fromLayout := m.viewport.Height
	m.View()
	fromView := m.viewport.Height
	if fromLayout != fromView {
		t.Fatalf("layout()=%d View()=%d with fleet box visible", fromLayout, fromView)
	}
}

func TestCtrlGOpensFleetOverlay(t *testing.T) {
	m := newReadyChatModel(30, 80)
	m.waiting = true
	feedAgents(m, 2)

	m.handleChatKey("ctrl+g", false)
	if m.overlay == nil {
		t.Fatal("ctrl+g must open the fleet overlay")
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "agent-0") || !strings.Contains(view, "agents · 2") {
		t.Fatalf("fleet overlay content missing:\n%s", view)
	}
	m.handleChatKey("esc", false)
	if m.overlay != nil {
		t.Fatal("esc must close the fleet overlay")
	}

	// Inert without agents (INV-TUI-23: no half-working keys).
	m2 := newReadyChatModel(30, 80)
	m2.handleChatKey("ctrl+g", false)
	if m2.overlay != nil {
		t.Fatal("ctrl+g with no agents must be a no-op")
	}
}
