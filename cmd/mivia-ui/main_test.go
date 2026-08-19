package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

func TestParseFlagsDefaults(t *testing.T) {
	cfg, err := parseFlags(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.demo || cfg.themeName != "mivia-dark" || cfg.themeExplicit || cfg.outputJSON {
		t.Errorf("got %+v, want the documented defaults", cfg)
	}
}

func TestParseFlagsTracksExplicitTheme(t *testing.T) {
	cfg, err := parseFlags([]string{"--theme", "mivia-light"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.themeName != "mivia-light" || !cfg.themeExplicit {
		t.Errorf("got %+v, want themeName=mivia-light themeExplicit=true", cfg)
	}
}

func TestParseFlagsOutputJSON(t *testing.T) {
	cfg, err := parseFlags([]string{"--output", "json"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.outputJSON {
		t.Error("expected --output json to set outputJSON")
	}
}

func TestParseFlagsUnknownFlagErrors(t *testing.T) {
	var stderr bytes.Buffer
	if _, err := parseFlags([]string{"--bogus"}, &stderr); err == nil {
		t.Error("expected an error for an unrecognised flag")
	}
}

func themes(t *testing.T) []theme.Theme {
	t.Helper()
	th, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	return th
}

func TestResolveThemeExplicitName(t *testing.T) {
	got, err := resolveTheme(themes(t), config{themeName: "mivia-light", themeExplicit: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "mivia-light" {
		t.Errorf("got %q, want mivia-light", got.Name)
	}
}

func TestResolveThemeUnknownNameErrors(t *testing.T) {
	if _, err := resolveTheme(themes(t), config{themeName: "nope", themeExplicit: true}); err == nil {
		t.Error("expected an error for an unknown theme name")
	}
}

func TestResolveThemeLightDefaultsWhenNotExplicit(t *testing.T) {
	got, err := resolveTheme(themes(t), config{themeName: "mivia-dark", light: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Dark {
		t.Errorf("got dark theme %q, want a light default for --light", got.Name)
	}
}

func TestResolveThemeDarkDefaultsWhenNotExplicit(t *testing.T) {
	got, err := resolveTheme(themes(t), config{themeName: "mivia-dark", dark: true})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Dark {
		t.Errorf("got light theme %q, want a dark default for --dark", got.Name)
	}
}

func TestResolveThemeExplicitOverridesLight(t *testing.T) {
	// --theme mivia-dark --light: explicit --theme wins, no light variant swap.
	got, err := resolveTheme(themes(t), config{themeName: "mivia-dark", themeExplicit: true, light: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "mivia-dark" {
		t.Errorf("got %q, want the explicit --theme to win over --light", got.Name)
	}
}

func TestFirstByDarkNoMatch(t *testing.T) {
	if _, ok := firstByDark(nil, true); ok {
		t.Error("expected no match against an empty theme list")
	}
}

func TestRunOutputJSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--output", "json"}, &stdout, &stderr, nil)
	if code != 0 {
		t.Fatalf("got exit code %d, stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"kind":"turn.start"`) {
		t.Errorf("expected NDJSON fixture output, got:\n%s", stdout.String())
	}
}

func TestRunNonTTYFallsBackToPlainStream(t *testing.T) {
	var stdout, stderr bytes.Buffer
	// A *bytes.Buffer is not *os.File, so run's isFile check fails
	// closed to the plain renderer - exactly the non-TTY / piped path.
	code := run(nil, &stdout, &stderr, nil)
	if code != 0 {
		t.Fatalf("got exit code %d, stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "> Add retry") {
		t.Errorf("expected the plain-stream fixture rendering, got:\n%s", stdout.String())
	}
}

func TestRunUnknownThemeErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--theme", "nope"}, &stdout, &stderr, nil)
	if code != 1 {
		t.Errorf("got exit code %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "unknown --theme") {
		t.Errorf("expected the theme error on stderr, got %q", stderr.String())
	}
}

// errWriter fails every Write, to exercise run's output-error paths for
// both the JSON and plain-stream renderers.
type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errWriteFailed }

var errWriteFailed = errors.New("write failed")

func TestRunOutputJSONWriteErrorExits1(t *testing.T) {
	var stderr bytes.Buffer
	code := run([]string{"--output", "json"}, errWriter{}, &stderr, nil)
	if code != 1 {
		t.Errorf("got exit code %d, want 1", code)
	}
}

func TestRunPlainStreamWriteErrorExits1(t *testing.T) {
	var stderr bytes.Buffer
	code := run(nil, errWriter{}, &stderr, nil)
	if code != 1 {
		t.Errorf("got exit code %d, want 1", code)
	}
}

func TestRunFlagParseErrorExits2(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--bogus"}, &stdout, &stderr, nil); code != 2 {
		t.Errorf("got exit code %d, want 2", code)
	}
}
