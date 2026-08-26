package settings

import (
	"context"
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

func TestMCPSection_GroupedByGlobalAndProject(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	plain := ansi.Strip(mcpSectionOf(s).View())

	for _, want := range []string{
		"Global MCP Servers (user config)",
		"Project MCP Servers (workspace)",
		"filesystem",
		"search",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("MCP view is missing %q:\n%s", want, plain)
		}
	}
}

func TestMCPSection_DetailPane(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	plain := ansi.Strip(mcpSectionOf(s).View())

	// Cursor defaults to first server (filesystem, global)
	if !strings.Contains(plain, "Global (user:") {
		t.Errorf("MCP view missing global detail pane label:\n%s", plain)
	}
	if !strings.Contains(plain, "Command: npx") {
		t.Errorf("MCP view missing command in detail pane:\n%s", plain)
	}
	if !strings.Contains(plain, "Transport: stdio") {
		t.Errorf("MCP view missing transport in detail pane:\n%s", plain)
	}
}

func TestMCPSection_NavigationChangesDetail(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	s = focusMCP(t, s)

	// Move cursor down to project server (search)
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s = next.(Screen)

	plain := ansi.Strip(mcpSectionOf(s).View())
	if !strings.Contains(plain, "Project (workspace:") {
		t.Errorf("MCP view missing project detail pane label after nav down:\n%s", plain)
	}
	if !strings.Contains(plain, "https://search.example.internal/mcp") {
		t.Errorf("MCP view missing endpoint in detail pane:\n%s", plain)
	}
	if !strings.Contains(plain, "authentication") {
		t.Errorf("MCP view missing authentication failure in detail pane:\n%s", plain)
	}
}

func TestToggleEnabledFlipsAndPersists(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	s = focusMCP(t, s)
	sec := mcpSectionOf(s)
	before, ok := sec.selectedServer()
	if !ok {
		t.Fatal("no server selected")
	}

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

func TestRemovingAnMCPServer_RequiresConfirmation(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	s = focusMCP(t, s)
	sec := mcpSectionOf(s)
	target, ok := sec.selectedServer()
	if !ok {
		t.Fatal("no server selected")
	}

	// 1. First 'x' should show confirmation notice and NOT delete yet
	next, cmd := s.Update(tea.KeyPressMsg{Text: "x", Code: 'x'})
	if cmd != nil {
		t.Fatal("expected no immediate save cmd on first 'x' (must prompt for confirmation)")
	}
	s = next.(Screen)
	plain := ansi.Strip(mcpSectionOf(s).View())
	if !strings.Contains(plain, "Delete MCP server \"filesystem\"? Press 'x' or 'y' to confirm") {
		t.Fatalf("expected delete confirmation notice on first 'x', got:\n%s", plain)
	}

	// 2. Press 'esc' to cancel deletion
	next, _ = s.Update(tea.KeyPressMsg{Text: "esc", Code: tea.KeyEsc})
	s = next.(Screen)
	plain = ansi.Strip(mcpSectionOf(s).View())
	if strings.Contains(plain, "Delete MCP server") {
		t.Errorf("expected confirmation prompt to be cleared after esc, got:\n%s", plain)
	}

	// Verify server still exists in store
	found := false
	for _, srv := range h.SettingsAdapters().MCP.MCPServers() {
		if srv.ID == target.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("server was removed prematurely after esc")
	}

	// 3. Press 'x' again, then confirm with second 'x'
	next, _ = s.Update(tea.KeyPressMsg{Text: "x", Code: 'x'})
	s = next.(Screen)
	next, cmd = s.Update(tea.KeyPressMsg{Text: "x", Code: 'x'})
	s = awaitMCPSaveTest(t, next.(Screen), cmd)

	for _, srv := range h.SettingsAdapters().MCP.MCPServers() {
		if srv.ID == target.ID {
			t.Errorf("server %q still present after confirmed removal", target.ID)
		}
	}
}

func TestAddingAnMCPServer_FormAndPersist(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	s = focusMCP(t, s)

	// Press 'n' to open Add form
	next, _ := s.Update(tea.KeyPressMsg{Text: "n", Code: 'n'})
	s = next.(Screen)
	sec := mcpSectionOf(s)
	if !sec.editing {
		t.Fatal("expected section to be in editing mode after 'n'")
	}
	plain := ansi.Strip(sec.View())
	if !strings.Contains(plain, "Add New MCP Server") {
		t.Fatalf("expected Add New MCP Server header in view, got:\n%s", plain)
	}

	// Type ID: "github"
	for _, ch := range "github" {
		next, _ = s.Update(tea.KeyPressMsg{Text: string(ch), Code: ch})
		s = next.(Screen)
	}

	// Move to Command field (down 2 steps: Scope -> ID -> Transport -> Command)
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s = next.(Screen)
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s = next.(Screen)

	// Type Command: "uvx"
	for _, ch := range "uvx" {
		next, _ = s.Update(tea.KeyPressMsg{Text: string(ch), Code: ch})
		s = next.(Screen)
	}

	// Move to Args field
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s = next.(Screen)

	// Type Args: "mcp-server-github"
	for _, ch := range "mcp-server-github" {
		next, _ = s.Update(tea.KeyPressMsg{Text: string(ch), Code: ch})
		s = next.(Screen)
	}

	// Press ctrl+s to save
	next, cmd := s.Update(tea.KeyPressMsg{Text: "ctrl+s", Code: 's', Mod: tea.ModCtrl})
	s = awaitMCPSaveTest(t, next.(Screen), cmd)

	// Verify new server was created and persisted in store
	var created *ports.MCPServerView
	for _, srv := range h.SettingsAdapters().MCP.MCPServers() {
		if srv.ID == "github" {
			srvCopy := srv
			created = &srvCopy
			break
		}
	}
	if created == nil {
		t.Fatal("expected new MCP server 'github' in store, but not found")
	}
	if created.Command != "uvx" {
		t.Errorf("got command %q, want 'uvx'", created.Command)
	}
	if len(created.Args) != 1 || created.Args[0] != "mcp-server-github" {
		t.Errorf("got args %v, want ['mcp-server-github']", created.Args)
	}
}

func TestEditingAnMCPServer_FormAndPersist(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	s = focusMCP(t, s)

	// Press 'enter' on selected server (filesystem)
	next, _ := s.Update(tea.KeyPressMsg{Text: "enter", Code: tea.KeyEnter})
	s = next.(Screen)
	sec := mcpSectionOf(s)
	if !sec.editing {
		t.Fatal("expected section to be in editing mode after 'enter'")
	}
	plain := ansi.Strip(sec.View())
	if !strings.Contains(plain, "Edit MCP Server: filesystem") {
		t.Fatalf("expected Edit MCP Server header in view, got:\n%s", plain)
	}

	// Cursor starts on Command field. Change command to "custom-npx"
	// First move to args
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s = next.(Screen)

	// Press ctrl+s to save
	next, cmd := s.Update(tea.KeyPressMsg{Text: "ctrl+s", Code: 's', Mod: tea.ModCtrl})
	s = awaitMCPSaveTest(t, next.(Screen), cmd)

	sec = mcpSectionOf(s)
	if sec.editing {
		t.Error("expected section to exit editing mode after successful save")
	}

	// Verify server still exists in store
	var updated *ports.MCPServerView
	for _, srv := range h.SettingsAdapters().MCP.MCPServers() {
		if srv.ID == "filesystem" {
			srvCopy := srv
			updated = &srvCopy
			break
		}
	}
	if updated == nil {
		t.Fatal("server 'filesystem' missing from store after edit")
	}
}

func TestMCPSection_FormValidation(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	s = focusMCP(t, s)

	// Press 'n' to open Add form
	next, _ := s.Update(tea.KeyPressMsg{Text: "n", Code: 'n'})
	s = next.(Screen)

	// Press ctrl+s with empty ID
	next, cmd := s.Update(tea.KeyPressMsg{Text: "ctrl+s", Code: 's', Mod: tea.ModCtrl})
	if cmd != nil {
		t.Error("expected no save cmd when form validation fails")
	}
	s = next.(Screen)
	sec := mcpSectionOf(s)
	if !sec.editing {
		t.Error("expected to stay in editing mode on validation failure")
	}
	plain := ansi.Strip(sec.View())
	if !strings.Contains(plain, "Server ID is required") {
		t.Errorf("expected validation notice in view, got:\n%s", plain)
	}

	// Press esc to cancel
	next, _ = s.Update(tea.KeyPressMsg{Text: "esc", Code: tea.KeyEsc})
	s = next.(Screen)
	sec = mcpSectionOf(s)
	if sec.editing {
		t.Error("expected to exit editing mode after esc")
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

// TestMCPRowsAlignColumns pins the settings screen's aligned layout:
// every server row's tool-count column must start at the same screen
// position regardless of how long its own id or target is.
func TestMCPRowsAlignColumns(t *testing.T) {
	s, _ := newHarnessScreen(t, 120, 30)
	rows := strings.Split(ansi.Strip(mcpSectionOf(s).View()), "\n")
	var withTools []string
	for _, r := range rows {
		if strings.Contains(r, "tools") && !strings.Contains(r, "registered") {
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

type emptyMCPStore struct{}

func (emptyMCPStore) MCPServers() []ports.MCPServerView { return nil }
func (emptyMCPStore) Apply(_ context.Context, _ ports.Scope, _ ports.MCPEdit) (ports.SaveHandle, error) {
	return nil, nil
}

func TestMCPSection_EmptyGroups(t *testing.T) {
	th := loadTheme(t)
	sec := newMCPSection(emptyMCPStore{})
	sec.SetTheme(th, theme.TierTrueColor)
	sec.SetSize(100, 30)

	plain := ansi.Strip(sec.View())
	if !strings.Contains(plain, "(no global MCP servers configured)") {
		t.Errorf("expected empty global group message, got:\n%s", plain)
	}
	if !strings.Contains(plain, "(no project MCP servers configured)") {
		t.Errorf("expected empty project group message, got:\n%s", plain)
	}
}

func TestMCPSection_StatusCheckAndNotices(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	s = focusMCP(t, s)

	// Press 'c' to check status
	next, _ := s.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
	s = next.(Screen)
	plain := ansi.Strip(mcpSectionOf(s).View())
	if !strings.Contains(plain, "status checked for filesystem") {
		t.Errorf("expected status checked notice, got:\n%s", plain)
	}
}
