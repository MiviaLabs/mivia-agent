package uiadapter_test

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
)

func boolPtr(b bool) *bool { return &b }
func intPtr(n int) *int    { return &n }

// TestBuildGeneralView_ResolvesEveryTUIOverride pins buildGeneralView's full
// field-by-field mapping from res.TUI: every pointer field set to a
// non-default value must actually reach the view, not just fall back to
// the hardcoded default (the exact regression the doc comment above it
// describes - a stale literal instead of the configured value).
func TestBuildGeneralView_ResolvesEveryTUIOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	res := &config.Resolved{
		ProviderName: "ollama", Model: "llama3.3",
		TUI: config.TUIConfig{
			Theme:         "light",
			Mouse:         boolPtr(false),
			ShowReasoning: boolPtr(false),
			ScrollLines:   intPtr(9),
			ScreenReader:  boolPtr(true),
			ReducedMotion: boolPtr(true),
		},
	}
	state := &cliagents.AgentSessionState{Registry: agents.NewRegistry(), WorkspaceRoot: t.TempDir()}
	store := uiadapter.NewSettingsStore(nil, res, state)
	v := store.Settings().General.General()

	if v.Theme != "light" {
		t.Errorf("Theme = %q, want %q", v.Theme, "light")
	}
	if v.Mouse {
		t.Error("Mouse = true, want the configured false")
	}
	if v.ShowReasoning {
		t.Error("ShowReasoning = true, want the configured false")
	}
	if v.ScrollLines != 9 {
		t.Errorf("ScrollLines = %d, want 9", v.ScrollLines)
	}
	if !v.ScreenReader {
		t.Error("ScreenReader = false, want the configured true")
	}
	if !v.ReducedMotion {
		t.Error("ReducedMotion = false, want the configured true")
	}
}

// TestBuildGeneralView_ZeroScrollLinesKeepsDefault pins the guard on
// ScrollLines specifically: *ScrollLines > 0 (not just non-nil), so an
// explicit zero does not zero out the view's usable scrollback.
func TestBuildGeneralView_ZeroScrollLinesKeepsDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	res := &config.Resolved{
		ProviderName: "ollama", Model: "llama3.3",
		TUI: config.TUIConfig{ScrollLines: intPtr(0)},
	}
	state := &cliagents.AgentSessionState{Registry: agents.NewRegistry(), WorkspaceRoot: t.TempDir()}
	store := uiadapter.NewSettingsStore(nil, res, state)
	if got := store.Settings().General.General().ScrollLines; got != 3 {
		t.Errorf("ScrollLines with an explicit 0 override = %d, want the default 3", got)
	}
}
