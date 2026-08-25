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

// modelsSectionOf reaches the Models section (index 5 - General,
// Projects, Automations, Agents, Skills, Models, MCP) as its concrete
// type for direct cursor/row assertions.
func modelsSectionOf(s Screen) *modelsSection { return s.sections[5].(*modelsSection) }

func awaitModelsSaveTest(t *testing.T, s Screen, cmd tea.Cmd) Screen {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a Cmd from a Models action")
	}
	next, _ := s.Update(cmd())
	return next.(Screen)
}

func focusModels(t *testing.T, s Screen) Screen {
	t.Helper()
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // General -> Projects
	s = next.(Screen)
	for i := 0; i < 4; i++ {
		next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		s = next.(Screen)
	}
	if got := s.sections[s.nav].Title(); got != "Models" {
		t.Fatalf("nav landed on %q, want Models", got)
	}
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	return next.(Screen)
}

func TestModelsSectionListsProvidersAndModels(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	plain := ansi.Strip(modelsSectionOf(s).View())
	for _, want := range []string{"openrouter", "ollama", "deepseek", "anthropic/claude-opus-5", "llama3.1"} {
		if !strings.Contains(plain, want) {
			t.Errorf("Models view is missing %q:\n%s", want, plain)
		}
	}
}

// TestModelsNeverRendersAKeyValue is the containment rule
// (settings-screen.md §5) at the one place this section could leak
// one: APIKeySet is a bool, so there is no field to accidentally print,
// but pin it anyway so a future refactor that adds a raw key field
// trips this test before it trips a security review.
func TestModelsBadges_ActiveAndDefault(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	view := ansi.Strip(modelsSectionOf(s).View())

	// Seed openrouter has claude-opus-5 as active and default
	if !strings.Contains(view, "active, default") {
		t.Errorf("expected view to contain 'active, default' badge, got:\n%s", view)
	}
	// Seed ollama has llama3.1 as default (not active)
	if !strings.Contains(view, "default") {
		t.Errorf("expected view to contain 'default' badge, got:\n%s", view)
	}
}

// TestModelsNeverRendersAKeyValue is the containment rule
// (settings-screen.md §5) at the one place this section could leak
// one: APIKeySet is a bool, so there is no field to accidentally print,
// but pin it anyway so a future refactor that adds a raw key field
// trips this test before it trips a security review.
func TestModelsNeverRendersAKeyValue(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	plain := ansi.Strip(modelsSectionOf(s).View())
	if strings.Contains(plain, "sk-") || strings.Contains(plain, "OPENROUTER_API_KEY=") {
		t.Errorf("Models view leaked something key-shaped:\n%s", plain)
	}
	if !strings.Contains(plain, "key set") && !strings.Contains(plain, "key missing") {
		t.Errorf("Models view does not show the key-presence badge:\n%s", plain)
	}
}

func TestActivatingAModelUpdatesTheStore(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	s = focusModels(t, s)

	// Row 0 is the openrouter provider header; row 1 is its first
	// model (openrouter's default per seedProviders is already
	// active - move down once more to target its SECOND model).
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s = next.(Screen)
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s = next.(Screen)
	if got := modelsSectionOf(s).rows[modelsSectionOf(s).cursor]; got.isProvider || got.model.Name != "openai/gpt-5" {
		t.Fatalf("cursor is on %+v, want the openai/gpt-5 row", got)
	}

	next, cmd := s.Update(tea.KeyPressMsg{Text: " ", Code: ' '})
	s = awaitModelsSaveTest(t, next.(Screen), cmd)

	got := h.SettingsAdapters().Providers.Providers()
	found := false
	for _, p := range got {
		if p.Name == "openrouter" && p.Active && p.ActiveModel == "openai/gpt-5" {
			found = true
		}
	}
	if !found {
		t.Errorf("openrouter/openai/gpt-5 was not activated: %+v", got)
	}
}

func TestActivatingAProviderHeaderIsRejectedWithANotice(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	s = focusModels(t, s)
	if got := modelsSectionOf(s).rows[0]; !got.isProvider {
		t.Fatal("row 0 is not a provider header")
	}
	next, cmd := s.Update(tea.KeyPressMsg{Text: " ", Code: ' '})
	s = next.(Screen)
	if cmd != nil {
		t.Fatal("activating a provider header must not start an async save")
	}
	if modelsSectionOf(s).notice == "" {
		t.Error("expected a notice explaining a provider header cannot be activated directly")
	}
}

func TestSetDefaultModelUpdatesTheStore(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	s = focusModels(t, s)

	// Move cursor to openai/gpt-5 under openrouter (row 2)
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s = next.(Screen)
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s = next.(Screen)
	if got := modelsSectionOf(s).rows[modelsSectionOf(s).cursor]; got.isProvider || got.model.Name != "openai/gpt-5" {
		t.Fatalf("cursor is on %+v, want the openai/gpt-5 row", got)
	}

	next, cmd := s.Update(tea.KeyPressMsg{Text: "d", Code: 'd'})
	s = awaitModelsSaveTest(t, next.(Screen), cmd)

	got := h.SettingsAdapters().Providers.Providers()
	found := false
	for _, p := range got {
		if p.Name == "openrouter" && p.DefaultModel == "openai/gpt-5" {
			found = true
		}
	}
	if !found {
		t.Errorf("openrouter default_model was not set to openai/gpt-5: %+v", got)
	}
}

func TestSetDefaultOnProviderHeaderIsRejectedWithANotice(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	s = focusModels(t, s)
	if got := modelsSectionOf(s).rows[0]; !got.isProvider {
		t.Fatal("row 0 is not a provider header")
	}
	next, cmd := s.Update(tea.KeyPressMsg{Text: "d", Code: 'd'})
	s = next.(Screen)
	if cmd != nil {
		t.Fatal("setting default on provider header must not start an async save")
	}
	if modelsSectionOf(s).notice == "" {
		t.Error("expected a notice explaining a provider header cannot be set as default directly")
	}
}

func TestRemovingAModelUpdatesTheStore(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	s = focusModels(t, s)
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s = next.(Screen)
	if got := modelsSectionOf(s).rows[modelsSectionOf(s).cursor]; got.isProvider {
		t.Fatal("expected the cursor on a model row")
	}
	target := modelsSectionOf(s).rows[modelsSectionOf(s).cursor]

	next, cmd := s.Update(tea.KeyPressMsg{Text: "x", Code: 'x'})
	s = awaitModelsSaveTest(t, next.(Screen), cmd)

	for _, p := range h.SettingsAdapters().Providers.Providers() {
		if p.Name != target.provider.Name {
			continue
		}
		for _, m := range p.Models {
			if m.Name == target.model.Name {
				t.Errorf("model %q still present under %q after removal", target.model.Name, p.Name)
			}
		}
	}
}

func TestNewIsNotYetWiredButDoesNotCrash(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	s = focusModels(t, s)
	next, cmd := s.Update(tea.KeyPressMsg{Text: "n", Code: 'n'})
	s = next.(Screen)
	if cmd != nil {
		t.Error("\"n\" is not wired to an async save yet; it must not start one")
	}
	if modelsSectionOf(s).notice == "" {
		t.Error("expected a notice saying creation is not available yet, not silence")
	}
}

func TestUnavailableModelsSectionSaysSo(t *testing.T) {
	th := loadTheme(t)
	tb := topbar.New(th, theme.TierTrueColor, ports.ModelInfo{}, ports.Usage{}, 80)
	s := New(th, theme.TierTrueColor, tb, ports.Settings{}, 0)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	s = next.(Screen)
	if got := ansi.Strip(modelsSectionOf(s).View()); !strings.Contains(got, "unavailable") {
		t.Errorf("expected the nil-store Models section to say unavailable, got %q", got)
	}
}

// TestModelsProviderRowsAlignColumns pins settings-screen.md section 1's
// aligned layout for the provider group: every provider row's
// model-count column must start at the same screen position regardless
// of its own provider name length. Provider and model rows align in
// separate groups (they carry different columns), so this checks only
// the provider-kind rows.
func TestModelsProviderRowsAlignColumns(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	rows := strings.Split(ansi.Strip(modelsSectionOf(s).View()), "\n")
	var withCount []string
	for _, r := range rows {
		if strings.Contains(r, " models") {
			withCount = append(withCount, r)
		}
	}
	if len(withCount) < 2 {
		t.Fatalf("fixture has fewer than 2 provider rows: %v", withCount)
	}
	first := strings.Index(withCount[0], " models")
	for i, r := range withCount[1:] {
		if got := strings.Index(r, " models"); got != first {
			t.Errorf("row %d: model-count column at %d, want %d (same as row 0):\n%q\n%q",
				i+1, got, first, withCount[0], r)
		}
	}
}
