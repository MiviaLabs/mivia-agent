package settings

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/topbar"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// mcpSectionOf reaches the MCP section (nav index 5, the last entry).
func mcpSectionOf(s Screen) *mcpSection { return s.sections[5].(*mcpSection) }

func focusMCP(t *testing.T, s Screen) Screen {
	t.Helper()
	for i := 0; i < 5; i++ {
		next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		s = next.(Screen)
	}
	if got := s.sections[s.nav].Title(); got != "MCP" {
		t.Fatalf("nav landed on %q, want MCP", got)
	}
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	return next.(Screen)
}

func awaitMCPSaveTest(t *testing.T, s Screen, cmd tea.Cmd) Screen {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a Cmd from an MCP action")
	}
	next, _ := s.Update(cmd())
	return next.(Screen)
}

func TestMaskArgElidesOnlyTheValueAfterAMarker(t *testing.T) {
	cases := map[string]string{
		"--token=sk-test-not-real-canary": "--token=***",
		"--api-key=abc123":                "--api-key=***",
		"plain-arg":                       "plain-arg",
		"-y":                              "-y",
	}
	for in, want := range cases {
		if got := maskArg(in); got != want {
			t.Errorf("maskArg(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMCPSectionListsServersMaskedAndEndpointHostOnly(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	plain := ansi.Strip(mcpSectionOf(s).View())
	for _, want := range []string{"filesystem", "search", "connected", "authentication failed"} {
		if !strings.Contains(plain, want) {
			t.Errorf("MCP view is missing %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "sk-test-not-real-canary") {
		t.Errorf("MCP view rendered the raw canary token unmasked:\n%s", plain)
	}
	if !strings.Contains(plain, "--token=***") {
		t.Errorf("MCP view did not mask the token argument:\n%s", plain)
	}
}

func TestToggleEnabledFlipsAndPersists(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	s = focusMCP(t, s)
	before := mcpSectionOf(s).rows[0]

	next, cmd := s.Update(tea.KeyPressMsg{Text: " ", Code: ' '})
	s = awaitMCPSaveTest(t, next.(Screen), cmd)

	var after ports.MCPServerView
	for _, srv := range h.SettingsAdapters().MCP.MCPServers() {
		if srv.ID == before.ID {
			after = srv
		}
	}
	if after.Enabled == before.Enabled {
		t.Errorf("server %q enabled flag did not flip: still %v", before.ID, after.Enabled)
	}
	if after.State != ports.MCPStateUnknown {
		t.Errorf("expected the fake toggle to reset state to Unknown (no runtime status source), got %v", after.State)
	}
}

func TestRemovingAnMCPServerUpdatesTheStore(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	s = focusMCP(t, s)
	target := mcpSectionOf(s).rows[0].ID

	next, cmd := s.Update(tea.KeyPressMsg{Text: "x", Code: 'x'})
	s = awaitMCPSaveTest(t, next.(Screen), cmd)

	for _, srv := range h.SettingsAdapters().MCP.MCPServers() {
		if srv.ID == target {
			t.Errorf("server %q still present after removal", target)
		}
	}
}

func TestUnavailableMCPSectionSaysSo(t *testing.T) {
	th := loadTheme(t)
	tb := topbar.New(th, theme.TierTrueColor, ports.ModelInfo{}, ports.Usage{}, 80)
	s := New(th, theme.TierTrueColor, tb, ports.Settings{}, 0)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	s = next.(Screen)
	if got := ansi.Strip(mcpSectionOf(s).View()); !strings.Contains(got, "unavailable") {
		t.Errorf("expected the nil-store MCP section to say unavailable, got %q", got)
	}
}

// TestMCPRowsAlignColumns pins settings-screen.md section 1's aligned
// layout: every server row's tool-count column must start at the same
// screen position regardless of how long its own id or target is.
func TestMCPRowsAlignColumns(t *testing.T) {
	s, _ := newHarnessScreen(t, 120, 30)
	rows := strings.Split(ansi.Strip(mcpSectionOf(s).View()), "\n")
	var withTools []string
	for _, r := range rows {
		if strings.Contains(r, "tools") {
			withTools = append(withTools, r)
		}
	}
	if len(withTools) < 2 {
		t.Fatalf("fixture has fewer than 2 MCP server rows: %v", withTools)
	}
	first := strings.Index(withTools[0], " tools")
	for i, r := range withTools[1:] {
		if got := strings.Index(r, " tools"); got != first {
			t.Errorf("row %d: tool-count column at %d, want %d (same as row 0):\n%q\n%q",
				i+1, got, first, withTools[0], r)
		}
	}
}
