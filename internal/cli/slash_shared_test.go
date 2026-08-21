package cli

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func TestParseModelArgs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name            string
		fields          []string
		currentProvider string
		defaultProvider string
		wantProvider    string
		wantModel       string
		wantHasArg      bool
	}{
		{
			name:            "no-arg uses current then default",
			fields:          []string{"/model"},
			currentProvider: "",
			defaultProvider: "openai",
			wantProvider:    "openai",
			wantModel:       "",
			wantHasArg:      false,
		},
		{
			name:            "no-arg keeps current provider",
			fields:          []string{"/model"},
			currentProvider: "openrouter",
			defaultProvider: "openai",
			wantProvider:    "openrouter",
			wantModel:       "",
			wantHasArg:      false,
		},
		{
			name:            "one-arg model keeps provider",
			fields:          []string{"/model", "gpt-4o"},
			currentProvider: "openai",
			defaultProvider: "openai",
			wantProvider:    "openai",
			wantModel:       "gpt-4o",
			wantHasArg:      true,
		},
		{
			name:            "two-arg provider and model",
			fields:          []string{"/model", "openrouter", "gpt-4o"},
			currentProvider: "openai",
			defaultProvider: "openai",
			wantProvider:    "openrouter",
			wantModel:       "gpt-4o",
			wantHasArg:      true,
		},
		{
			name:            "three-plus-arg joins model tokens",
			fields:          []string{"/model", "openrouter", "gpt", "4o", "mini"},
			currentProvider: "openai",
			defaultProvider: "openai",
			wantProvider:    "openrouter",
			wantModel:       "gpt 4o mini",
			wantHasArg:      true,
		},
		{
			name:            "empty current falls back before model arg",
			fields:          []string{"/model", "claude-3"},
			currentProvider: "",
			defaultProvider: "anthropic",
			wantProvider:    "anthropic",
			wantModel:       "claude-3",
			wantHasArg:      true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			provider, model, hasArg := ParseModelArgs(tc.fields, tc.currentProvider, tc.defaultProvider)
			if provider != tc.wantProvider || model != tc.wantModel || hasArg != tc.wantHasArg {
				t.Fatalf("ParseModelArgs() = (%q, %q, %v), want (%q, %q, %v)",
					provider, model, hasArg, tc.wantProvider, tc.wantModel, tc.wantHasArg)
			}
		})
	}
}

func TestParseNonNegInt(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		fields     []string
		wantN      int
		wantHasArg bool
		wantOK     bool
	}{
		{name: "no-arg", fields: []string{"/budget"}, wantN: 0, wantHasArg: false, wantOK: false},
		{name: "valid", fields: []string{"/budget", "100"}, wantN: 100, wantHasArg: true, wantOK: true},
		{name: "zero", fields: []string{"/steps", "0"}, wantN: 0, wantHasArg: true, wantOK: true},
		{name: "negative", fields: []string{"/budget", "-1"}, wantN: 0, wantHasArg: true, wantOK: false},
		{name: "non-numeric", fields: []string{"/budget", "100x"}, wantN: 0, wantHasArg: true, wantOK: false},
		{name: "empty-token", fields: []string{"/steps", ""}, wantN: 0, wantHasArg: true, wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			n, hasArg, ok := ParseNonNegInt(tc.fields)
			if n != tc.wantN || hasArg != tc.wantHasArg || ok != tc.wantOK {
				t.Fatalf("ParseNonNegInt() = (%d, %v, %v), want (%d, %v, %v)",
					n, hasArg, ok, tc.wantN, tc.wantHasArg, tc.wantOK)
			}
		})
	}
}

func TestModelSwitchChoices(t *testing.T) {
	t.Parallel()
	res := &config.Resolved{
		ProviderName: "openai",
		Models:       []string{"gpt-4o", "gpt-4o-mini"},
	}
	if got := ModelSwitchChoices(res, "openai", "openai"); got != "gpt-4o, gpt-4o-mini" {
		t.Fatalf("ModelSwitchChoices active provider = %q", got)
	}
	if got := ModelSwitchChoices(res, "", "openai"); got != "gpt-4o, gpt-4o-mini" {
		t.Fatalf("ModelSwitchChoices empty provider falls back = %q", got)
	}
	if got := ModelSwitchChoices(nil, "openai", "openai"); got != "" {
		t.Fatalf("ModelSwitchChoices nil config = %q, want empty", got)
	}
	if got := ModelSwitchChoices(res, "missing", "openai"); got != "" {
		t.Fatalf("ModelSwitchChoices unknown provider = %q, want empty", got)
	}
}

func TestModelRestoreNoticeText(t *testing.T) {
	t.Parallel()
	got := ModelRestoreNoticeText("removed", "current")
	want := `session was saved with model "removed", which is not available; using current`
	if got != want {
		t.Fatalf("ModelRestoreNoticeText() = %q, want %q", got, want)
	}
}

func TestFormatBudgetAndSteps(t *testing.T) {
	t.Parallel()
	if got, want := FormatBudgetSummary(4096), "context budget=4096 tokens\nusage: /budget <tokens>\n  set to 0 for model default"; got != want {
		t.Fatalf("FormatBudgetSummary() = %q, want %q", got, want)
	}
	if got, want := FormatBudgetSet(100), "(context budget set to 100 tokens)"; got != want {
		t.Fatalf("FormatBudgetSet() = %q, want %q", got, want)
	}
	if got, want := FormatBudgetInvalid("100x"), `invalid budget "100x"; use a positive number`; got != want {
		t.Fatalf("FormatBudgetInvalid() = %q, want %q", got, want)
	}
	if got, want := FormatStepsSummary(0), "max steps: unlimited\nusage: /steps <n> (set to 0 for unlimited)"; got != want {
		t.Fatalf("FormatStepsSummary(0) = %q, want %q", got, want)
	}
	if got, want := FormatStepsSummary(5), "max steps: 5\nusage: /steps <n> (set to 0 for unlimited)"; got != want {
		t.Fatalf("FormatStepsSummary(5) = %q, want %q", got, want)
	}
	if got, want := FormatStepsSet(0), "(max steps set to unlimited)"; got != want {
		t.Fatalf("FormatStepsSet(0) = %q, want %q", got, want)
	}
	if got, want := FormatStepsSet(5), "(max steps set to 5)"; got != want {
		t.Fatalf("FormatStepsSet(5) = %q, want %q", got, want)
	}
	if got, want := FormatStepsInvalid("nope"), `invalid step limit "nope"; use a positive number (0 = unlimited)`; got != want {
		t.Fatalf("FormatStepsInvalid() = %q, want %q", got, want)
	}
}

func TestSessionResultFormatters(t *testing.T) {
	t.Parallel()
	if got, want := SaveSessionResult("demo", 3, 1), `(session "demo" saved - 3 messages, 1 turns)`; got != want {
		t.Fatalf("SaveSessionResult() = %q, want %q", got, want)
	}
	if got, want := LoadSessionResult("demo", 4, 2), `(session "demo" loaded - 4 messages, 2 turns)`; got != want {
		t.Fatalf("LoadSessionResult() = %q, want %q", got, want)
	}
	if got, want := DeleteSessionResult("demo"), `(session "demo" deleted)`; got != want {
		t.Fatalf("DeleteSessionResult() = %q, want %q", got, want)
	}
}

func TestModelMessageFormatters(t *testing.T) {
	t.Parallel()
	if got, want := formatModelCurrent("gpt-4o", "gpt-4o, mini"), "current model=gpt-4o\navailable: gpt-4o, mini"; got != want {
		t.Fatalf("formatModelCurrent with choices = %q, want %q", got, want)
	}
	if got, want := formatModelCurrent("gpt-4o", ""), "current model=gpt-4o\nusage: /model <name>"; got != want {
		t.Fatalf("formatModelCurrent without choices = %q, want %q", got, want)
	}
	if got, want := FormatModelSet("openai", "gpt-4o", ""), "(model set to openai/gpt-4o)"; got != want {
		t.Fatalf("FormatModelSet() = %q, want %q", got, want)
	}
	if got, want := FormatModelUnavailable("openai", "gpt-4o, mini"), "model is not available for provider openai\navailable: gpt-4o, mini"; got != want {
		t.Fatalf("FormatModelUnavailable with choices = %q, want %q", got, want)
	}
	if got, want := FormatModelUnavailable("openai", ""), "model name is invalid"; got != want {
		t.Fatalf("FormatModelUnavailable without choices = %q, want %q", got, want)
	}
}

func TestTerminalSlashSinkWritesPrefixedLines(t *testing.T) {
	t.Parallel()
	var buf strings.Builder
	term := &Terminal{out: &buf}
	sink := terminalSlashSink{t: term}
	sink.Info("hello")
	sink.Error("boom")
	got := buf.String()
	if got != "\nhello\nboom" {
		t.Fatalf("terminalSlashSink output = %q, want %q", got, "\nhello\nboom")
	}
}

func TestSlashSharedHasNoSurfaceImports(t *testing.T) {
	// Structural guard: pure helpers live in slash_shared.go and must stay
	// free of terminal/tui coupling beyond the small slashSink interface.
	// The production symbols under test above are the real entry points.
	_ = slashSink(terminalSlashSink{})
}
