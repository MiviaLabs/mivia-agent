package legacytui

// toolui_coverage_test.go drives the pure tool-row helpers in
// toolui.go: toolKindIcon, toolRowElapsed, toolRowIcon.

import (
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/cli"
)

func TestToolKindIcon(t *testing.T) {
	// Non-ascii delegates to ToolIconForName.
	if got := toolKindIcon("read_file", false); got == "" {
		t.Error("toolKindIcon(non-ascii) returned empty")
	}
	// ASCII variants map each tool family to a single character.
	for _, tc := range []struct{ name, want string }{
		{"read_file", "r"},
		{"list_dir", "d"},
		{"grep", "/"},
		{"glob", "/"},
		{"write_file", "e"},
		{"search_replace", "e"},
		{"multi_edit", "e"},
		{"run_command", ">"},
		{"search", "w"},
		{"delegate", "+"},
		{"dispatch_tasks", "+"},
		{"unknown-tool", "-"},
	} {
		if got := toolKindIcon(tc.name, true); got != tc.want {
			t.Errorf("toolKindIcon(%q, ascii) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestToolRowElapsed(t *testing.T) {
	now := time.Now()
	// Done with a zero End: zero elapsed.
	if got := toolRowElapsed(cli.ToolRow{Done: true}, now); got != 0 {
		t.Errorf("done+zero-end elapsed = %v, want 0", got)
	}
	// Done with a real End: End - Start.
	start := now.Add(-2 * time.Second)
	end := now.Add(-1 * time.Second)
	if got := toolRowElapsed(cli.ToolRow{Done: true, Start: start, End: end}, now); got != time.Second {
		t.Errorf("done elapsed = %v, want 1s", got)
	}
	// Running with a zero Start: zero.
	if got := toolRowElapsed(cli.ToolRow{}, now); got != 0 {
		t.Errorf("zero-start elapsed = %v, want 0", got)
	}
	// Running: now - Start.
	if got := toolRowElapsed(cli.ToolRow{Start: start}, now); got < time.Second {
		t.Errorf("running elapsed = %v, want >=1s", got)
	}
}

func TestToolRowIcon(t *testing.T) {
	now := time.Now()
	if got := toolRowIcon(cli.ToolRow{Done: true, Failed: true}, now); got != cli.GlyphCross {
		t.Errorf("failed icon = %q, want %q", got, cli.GlyphCross)
	}
	if got := toolRowIcon(cli.ToolRow{Done: true}, now); got != cli.GlyphCheck {
		t.Errorf("done icon = %q, want %q", got, cli.GlyphCheck)
	}
	if got := toolRowIcon(cli.ToolRow{}, now); got == "" {
		t.Error("running icon returned empty")
	}
}
