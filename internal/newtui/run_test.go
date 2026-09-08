package newtui

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/charmbracelet/x/ansi"
)

func TestBuildAppPropagatesThemeLoadError(t *testing.T) {
	original := loadThemes
	wantErr := errors.New("corrupt embedded theme")
	loadThemes = func() ([]theme.Theme, error) { return nil, wantErr }
	defer func() { loadThemes = original }()

	sess := chat.NewSession(&config.Resolved{}, nil)
	agentState := &cli.AgentSessionState{}
	if _, _, _, err := buildApp(sess, &config.Resolved{}, true, agentState, ""); !errors.Is(err, wantErr) {
		t.Fatalf("buildApp err = %v, want %v", err, wantErr)
	}
}

func TestBuildApp(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{}, nil)
	res := &config.Resolved{}
	agentState := &cli.AgentSessionState{}

	appModel, _, _, err := buildApp(sess, res, true, agentState, "")
	if err != nil {
		t.Fatalf("buildApp failed: %v", err)
	}
	if appModel == nil {
		t.Fatal("expected non-nil app model")
	}
}

func TestBuildAppUsesConfiguredThemeAndDetectedTier(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	sess := chat.NewSession(&config.Resolved{TUI: config.TUIConfig{Theme: "mivia-light"}}, nil)
	root, _, _, err := buildApp(sess, &config.Resolved{TUI: config.TUIConfig{Theme: "mivia-light"}}, true, &cli.AgentSessionState{}, "")
	if err != nil {
		t.Fatal(err)
	}
	m, ok := root.(app.Model)
	if !ok {
		t.Fatalf("root type = %T, want app.Model", root)
	}
	if m.Theme.Name != "mivia-light" {
		t.Fatalf("startup theme = %q, want mivia-light", m.Theme.Name)
	}
	if m.Tier == theme.TierTrueColor {
		t.Fatalf("startup tier = %v, want NO_COLOR degradation", m.Tier)
	}
}

func TestLoadAllThemesIncludesUserThemes(t *testing.T) {
	oldEmbedded, oldUser, oldDir := loadEmbeddedThemes, loadUserThemes, userThemesDir
	defer func() { loadEmbeddedThemes, loadUserThemes, userThemesDir = oldEmbedded, oldUser, oldDir }()
	loadEmbeddedThemes = func() ([]theme.Theme, error) { return []theme.Theme{{Name: "built-in"}}, nil }
	loadUserThemes = func(string) ([]theme.Theme, error) { return []theme.Theme{{Name: "custom"}}, nil }
	userThemesDir = func() string { return "/custom/themes" }

	themes, err := loadAllThemes()
	if err != nil {
		t.Fatal(err)
	}
	if len(themes) != 2 || themes[0].Name != "built-in" || themes[1].Name != "custom" {
		t.Fatalf("themes = %+v, want built-in and custom", themes)
	}
}

func TestChooseThemeFallsBackToDarkForUnknownName(t *testing.T) {
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	got, err := chooseTheme(themes, "not-installed")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "mivia-dark" {
		t.Fatalf("fallback theme = %q, want mivia-dark", got.Name)
	}
}

func TestPersistThemeWritesSelectedName(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "mivia.toml")
	store := uiadapter.NewSettingsStore(nil, &config.Resolved{ConfigPath: configPath}, nil)
	if msg := persistTheme(store, "mivia-light")(); msg != nil {
		t.Fatalf("persistTheme returned %v", msg)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `theme = 'mivia-light'`) {
		t.Fatalf("saved config = %q, missing selected theme", data)
	}
}

// TestRunTUICallsBuildApp exercises RunTUI's whole body - buildApp, the
// notifier wiring, the program run, and the lease-release defer - through
// the newTeaProgram seam. An earlier version ran the program on the
// process's real stdin and relied on Run() failing fast off-TTY, which
// holds on linux but not on windows, where it ran headless forever and
// died on the 10-minute test timeout (the same trap run_mouse_test.go
// fixed one commit earlier; this test hid behind that hang).
func TestRunTUICallsBuildApp(t *testing.T) {
	original := newTeaProgram
	newTeaProgram = func(root tea.Model) *tea.Program {
		p := tea.NewProgram(root, tea.WithInput(strings.NewReader("")), tea.WithOutput(io.Discard))
		// Quit blocks until the event loop is receiving, so firing it
		// before Run starts still lands exactly once the program is live.
		go p.Quit()
		return p
	}
	defer func() { newTeaProgram = original }()

	sess := chat.NewSession(&config.Resolved{}, nil)
	agentState := &cli.AgentSessionState{}

	done := make(chan error, 1)
	go func() { done <- RunTUI(sess, nil, true, agentState, "") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunTUI = %v, want clean quit", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("RunTUI did not return after the program quit")
	}
}

func ctrl(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl} }

// TestBuildApp_SubagentHistoryVisibleInDialog is an end-to-end smoke test
// of the production wiring path: it drives the real tea.Model exactly as a
// user would - open the panel, select the dispatched subagent row, open
// its dialog - starting from a session whose history already contains a
// completed dispatch_tasks call (the shape /resume hands back). The actual
// regression coverage for the wiring bug (a switched-to session's
// Conversation never getting SetSubagents called on it) lives at the
// uiadapter level: TestSessionPool_GetOrCreateWiresSubagentThreadsOnResume
// and TestSessionPool_CreateFreshWiresSubagentThreads, which fail against
// the pre-fix SessionPool and pass here only because buildApp now sources
// its Conversation and SubagentThreads from the SAME pool those exercise.
func TestBuildApp_SubagentHistoryVisibleInDialog(t *testing.T) {
	res := &config.Resolved{Model: "test-model"}
	sess := chat.NewSession(res, nil)
	sess.SessionID = "session-1"
	sess.Messages = []provider.Message{
		{
			Role: provider.RoleAssistant,
			ToolCalls: []provider.ToolCall{
				{
					ID: "call_disp_1",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{
						Name:      "dispatch_tasks",
						Arguments: `{"tasks":[{"id":"task-leak-check","prompt":"perform detailed research on memory leaks","agent":"researcher"}]}`,
					},
				},
			},
		},
		{
			Role:       provider.RoleTool,
			ToolCallID: "call_disp_1",
			Content:    `[{"task_id":"task-leak-check","status":"completed","output":"found 0 leaks across 12 packages"}]`,
		},
	}
	agentState := &cli.AgentSessionState{}

	root, _, _, err := buildApp(sess, res, true, agentState, "")
	if err != nil {
		t.Fatalf("buildApp: %v", err)
	}

	m, _ := root.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m, _ = m.Update(ctrl('b')) // open the activity panel, focused on its list
	// The panel opens on its model row. Below it the section headers are
	// selectable rows of their own (they fold their sections), so the
	// walk down to the subagent passes the "subagents" header first.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "perform detailed research on memory leaks") {
		t.Errorf("expected the dispatched subagent's prompt to render in the dialog, got:\n%s", view)
	}
	if !strings.Contains(view, "found 0 leaks across 12 packages") {
		t.Errorf("expected the dispatched subagent's output to render in the dialog, got:\n%s", view)
	}
}

// TestLoadAllThemes_EmbeddedAndUserErrorsSurface pins loadAllThemes' own
// two error branches directly (not via the loadThemes indirection
// TestBuildAppPropagatesThemeLoadError swaps out entirely).
func TestLoadAllThemes_EmbeddedAndUserErrorsSurface(t *testing.T) {
	oldEmbedded, oldUser, oldDir := loadEmbeddedThemes, loadUserThemes, userThemesDir
	defer func() { loadEmbeddedThemes, loadUserThemes, userThemesDir = oldEmbedded, oldUser, oldDir }()

	embeddedErr := errors.New("embedded broken")
	loadEmbeddedThemes = func() ([]theme.Theme, error) { return nil, embeddedErr }
	if _, err := loadAllThemes(); !errors.Is(err, embeddedErr) {
		t.Fatalf("loadAllThemes err = %v, want the embedded-load error", err)
	}

	userErr := errors.New("user dir broken")
	loadEmbeddedThemes = func() ([]theme.Theme, error) { return []theme.Theme{{Name: "built-in"}}, nil }
	loadUserThemes = func(string) ([]theme.Theme, error) { return nil, userErr }
	userThemesDir = func() string { return "/custom/themes" }
	if _, err := loadAllThemes(); !errors.Is(err, userErr) {
		t.Fatalf("loadAllThemes err = %v, want the user-load error", err)
	}
}

// TestChooseTheme_NoMatchAndNoDefaultIsAnError pins the final error branch:
// neither the requested name nor the mivia-dark fallback is present.
func TestChooseTheme_NoMatchAndNoDefaultIsAnError(t *testing.T) {
	if _, err := chooseTheme([]theme.Theme{{Name: "other"}}, "missing"); err == nil {
		t.Fatal("chooseTheme accepted a theme set with no match and no default")
	}
}
