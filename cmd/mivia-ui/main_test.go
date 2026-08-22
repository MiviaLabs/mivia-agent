package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/termprobe"
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
	got, err := resolveTheme(themes(t), cfg{themeName: "mivia-light", themeExplicit: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "mivia-light" {
		t.Errorf("got %q, want mivia-light", got.Name)
	}
}

func TestResolveThemeUnknownNameErrors(t *testing.T) {
	if _, err := resolveTheme(themes(t), cfg{themeName: "nope", themeExplicit: true}); err == nil {
		t.Error("expected an error for an unknown theme name")
	}
}

func TestResolveThemeLightDefaultsWhenNotExplicit(t *testing.T) {
	got, err := resolveTheme(themes(t), cfg{themeName: "mivia-dark", light: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Dark {
		t.Errorf("got dark theme %q, want a light default for --light", got.Name)
	}
}

func TestResolveThemeDarkDefaultsWhenNotExplicit(t *testing.T) {
	got, err := resolveTheme(themes(t), cfg{themeName: "mivia-dark", dark: true})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Dark {
		t.Errorf("got light theme %q, want a dark default for --dark", got.Name)
	}
}

func TestResolveThemeExplicitOverridesLight(t *testing.T) {
	// --theme mivia-dark --light: explicit --theme wins, no light variant swap.
	got, err := resolveTheme(themes(t), cfg{themeName: "mivia-dark", themeExplicit: true, light: true})
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
	code := run(context.Background(), []string{"--output", "json"}, &stdout, &stderr, nil)
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
	code := run(context.Background(), nil, &stdout, &stderr, nil)
	if code != 0 {
		t.Fatalf("got exit code %d, stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "> Add retry") {
		t.Errorf("expected the plain-stream fixture rendering, got:\n%s", stdout.String())
	}
}

func TestRunUnknownThemeErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--theme", "nope"}, &stdout, &stderr, nil)
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
	code := run(context.Background(), []string{"--output", "json"}, errWriter{}, &stderr, nil)
	if code != 1 {
		t.Errorf("got exit code %d, want 1", code)
	}
}

func TestRunPlainStreamWriteErrorExits1(t *testing.T) {
	var stderr bytes.Buffer
	code := run(context.Background(), nil, errWriter{}, &stderr, nil)
	if code != 1 {
		t.Errorf("got exit code %d, want 1", code)
	}
}

func TestRunFlagParseErrorExits2(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--bogus"}, &stdout, &stderr, nil); code != 2 {
		t.Errorf("got exit code %d, want 2", code)
	}
}

func TestParseFlagsNewCockpitFlags(t *testing.T) {
	cfg, err := parseFlags([]string{
		"--no-mouse", "--scroll-lines", "7", "--full-repaint",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.noMouse || cfg.scrollLines != 7 || !cfg.fullRepaint {
		t.Errorf("got %+v, want no-mouse, 7 scroll lines, full-repaint", cfg)
	}
}

func TestParseFlagsScrollLinesMustBePositive(t *testing.T) {
	var stderr bytes.Buffer
	if _, err := parseFlags([]string{"--scroll-lines", "0"}, &stderr); err == nil {
		t.Error("expected --scroll-lines 0 to be rejected")
	}
}

// TestRunScreenReaderFallsBackToPlainStream pins rule 6.4: the cockpit
// never starts in screen-reader mode; one line says why and the plain
// stream renders.
func TestRunScreenReaderFallsBackToPlainStream(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), nil, &stdout, &stderr, []string{"MIVIA_SCREEN_READER=1"})
	if code != 0 {
		t.Fatalf("got exit code %d, stderr: %s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "screen-reader mode") {
		t.Errorf("expected the one-line explanation, got:\n%s", out)
	}
	if !strings.Contains(out, "> Add retry") {
		t.Errorf("expected the plain-stream fixture rendering, got:\n%s", out)
	}
}

// TestRunDumbTerminalFallsBackToPlainStream pins rule 9.6.
func TestRunDumbTerminalFallsBackToPlainStream(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), nil, &stdout, &stderr, []string{"TERM=dumb"})
	if code != 0 {
		t.Fatalf("got exit code %d, stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "TERM=dumb") {
		t.Errorf("expected the one-line explanation, got:\n%s", stdout.String())
	}
}

// TestRunTmuxControlModeRefused pins the section 4 hazard: tmux -CC
// corrupts the cockpit, so it is refused with one line and the plain
// stream renders.
func TestRunTmuxControlModeRefused(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), nil, &stdout, &stderr, []string{
		"TERM_PROGRAM=iTerm.app", "TERM=screen-256color", "TMUX=/tmp/tmux-0/default",
	})
	if code != 0 {
		t.Fatalf("got exit code %d, stderr: %s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "tmux control mode") {
		t.Errorf("expected the refusal line, got:\n%s", out)
	}
	if !strings.Contains(out, "> Add retry") {
		t.Errorf("expected the plain-stream fixture rendering, got:\n%s", out)
	}
}

// TestPlainStreamReasonPriority pins which refusal wins when more than
// one applies: the accessibility refusal outranks the hazard refusal.
func TestPlainStreamReasonPriority(t *testing.T) {
	env := []string{"MIVIA_SCREEN_READER=1", "TERM=dumb"}
	got := plainStreamReason(env, termprobe.Probe(env, ""))
	if !strings.Contains(got, "screen-reader") {
		t.Errorf("got %q, want the screen-reader reason first", got)
	}

	env = []string{"TERM=dumb"}
	got = plainStreamReason(env, termprobe.Probe(env, ""))
	if !strings.Contains(got, "TERM=dumb") {
		t.Errorf("got %q, want the dumb-terminal reason", got)
	}

	env = []string{"TERM=xterm-256color"}
	if got := plainStreamReason(env, termprobe.Probe(env, "")); got != "" {
		t.Errorf("got %q, want no refusal on a plain terminal", got)
	}
}

// TestScrollLinesFlagSetsConfig pins rule 6.6's settable wheel speed:
// the flag value reaches the config package before the Program starts.
func TestScrollLinesFlagSetsConfig(t *testing.T) {
	orig := uikitconfig.CockpitScrollLines
	t.Cleanup(func() { uikitconfig.CockpitScrollLines = orig })

	var stdout, stderr bytes.Buffer
	// A non-TTY stdout short-circuits into the plain renderer AFTER the
	// flag was parsed and applied, which is all this test needs.
	if code := run(context.Background(), []string{"--scroll-lines", "5"}, &stdout, &stderr, nil); code != 0 {
		t.Fatalf("got exit code %d, stderr: %s", code, stderr.String())
	}
	if uikitconfig.CockpitScrollLines != 5 {
		t.Errorf("CockpitScrollLines = %d, want 5 from --scroll-lines", uikitconfig.CockpitScrollLines)
	}
}

// TestTmuxVersionOutsideTmux: no tmux, no query.
func TestTmuxVersionOutsideTmux(t *testing.T) {
	if got := tmuxVersion([]string{"TERM=xterm"}); got != "" {
		t.Errorf("got %q, want empty outside tmux", got)
	}
}

// TestMockCommandsAreRealCandidates pins the replay command set: every
// row has a name and a description, because the menu renders both.
func TestMockCommandsAreRealCandidates(t *testing.T) {
	cmds := mockCommands()
	if len(cmds) == 0 {
		t.Fatal("expected replay commands")
	}
	for _, c := range cmds {
		if c.Name == "" || c.Desc == "" {
			t.Errorf("command %+v must have both a name and a description", c)
		}
	}
}

// TestParseFlagsDemoDefaultIsTrue pins the demo default: --demo is true
// when not explicitly set, matching the documented behaviour that the
// cockpit launches against the demo harness until --demo=false flips it.
func TestParseFlagsDemoDefaultIsTrue(t *testing.T) {
	cfg, err := parseFlags(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.demo {
		t.Error("--demo default should be true")
	}
}

// TestParseFlagsDemoFalseSwitchesMode pins --demo=false: the cockpit
// runs against a real chat.Session via internal/uiadapter.
func TestParseFlagsDemoFalseSwitchesMode(t *testing.T) {
	cfg, err := parseFlags([]string{"--demo=false"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.demo {
		t.Error("--demo=false should set demo=false")
	}
}

// TestParseFlagsWorkspaceDefaultsToCWD pins the --workspace default
// being the current working directory, not empty.
func TestParseFlagsWorkspaceDefaultsToCWD(t *testing.T) {
	cfg, err := parseFlags(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.workspace == "" {
		t.Error("--workspace should default to cwd, got empty string")
	}
}
