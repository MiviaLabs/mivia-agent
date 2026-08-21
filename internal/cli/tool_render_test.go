package cli

import (
	"strings"
	"testing"
)

func TestToolRenderItem_StatusParityAndCaps(t *testing.T) {
	for _, tc := range []struct {
		done, failed bool
		want         string
	}{
		{false, false, "◐"}, {true, false, "✓"}, {true, true, "✗"},
	} {
		item := NewToolRenderItem("read_file", "", "result", tc.done, tc.failed)
		if got := item.statusIcon(false); got != tc.want {
			t.Fatalf("status=%q want %q", got, tc.want)
		}
	}
	item := NewToolRenderItem("read_file", "", strings.Repeat("x", 100), true, false)
	if got := item.summary(12); len(got) > 12 {
		t.Fatalf("summary exceeded cap: %d", len(got))
	}
}

func TestToolRenderItem_RedactionAndASCIIWithoutColor(t *testing.T) {
	installTestRedactionPolicy(t)
	item := NewToolRenderItem("run_command", `token=secret-value`, `Authorization: Bearer abc.def`, true, false)
	got := formatToolLine(item, 80, toolRenderOptions{ASCII: true, Color: false})
	if strings.Contains(got, "secret-value") || strings.Contains(got, "abc.def") {
		t.Fatalf("leaked secret: %q", got)
	}
	if !strings.Contains(got, "*") || strings.Contains(got, "\033[") {
		t.Fatalf("not ASCII/no-color: %q", got)
	}
	// Kind icon present even without color (ASCII stand-in for run_command is ">").
	if !strings.Contains(got, ">") || !strings.Contains(got, "run_command") {
		t.Fatalf("missing kind icon in monochrome line: %q", got)
	}
}

func TestToolKindIcon_ASCIIAndUnicode(t *testing.T) {
	// Unicode terminals get the typed action glyphs (⚙ tool, ◆ agent);
	// ASCII terminals keep single-byte per-tool stand-ins.
	if got := toolKindIcon("search_replace", false); got != "⚙" {
		t.Fatalf("unicode edit icon=%q", got)
	}
	if got := toolKindIcon("dispatch_tasks", false); got != "◆" {
		t.Fatalf("unicode dispatch icon=%q", got)
	}
	if got := toolKindIcon("search_replace", true); got != "e" {
		t.Fatalf("ascii edit icon=%q", got)
	}
	if got := toolKindIcon("dispatch_tasks", true); got != "+" {
		t.Fatalf("ascii dispatch icon=%q", got)
	}
}

func TestTerminalToolRenderOptions_EnvironmentPolicy(t *testing.T) {
	cases := []struct {
		name      string
		noColor   string
		term      string
		wantASCII bool
		wantColor bool
	}{
		{name: "default", term: "xterm-256color", wantColor: true},
		{name: "no color", noColor: "1", term: "xterm-256color", wantColor: false},
		{name: "dumb terminal", term: "dumb", wantASCII: true},
		{name: "dumb and no color", noColor: "1", term: "dumb", wantASCII: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", tc.noColor)
			t.Setenv("TERM", tc.term)
			got := terminalToolRenderOptions()
			if got.ASCII != tc.wantASCII || got.Color != tc.wantColor {
				t.Fatalf("options=%+v want ASCII=%v Color=%v", got, tc.wantASCII, tc.wantColor)
			}
		})
	}
}
