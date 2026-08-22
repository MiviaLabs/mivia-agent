// Command mivia-ui is the new terminal UI (build spec step 7/first
// deliverable). It runs in two modes:
//
//	--demo (default)        the demo harness's scripted, multi-turn
//	                        conversation (internal/uikit/demoharness).
//	--demo=false --workspace <root>
//	                        a real chat.Session wired through
//	                        internal/uiadapter.New: every provider,
//	                        tool, and checkpoint configured in the
//	                        workspace is live.
//
// --scenario picks which scripted conversation --demo plays. The four
// port slots the conversation screen accepts (commands, command-runner,
// subagent-threads, settings) are wired from the demo harness in --demo
// and remain nil in --demo=false; future phases surface them as the
// CLI refactor lands (docs/design/ui-isolation.md).
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/composer"
	"github.com/MiviaLabs/mivia-agent/internal/ui/jsonout"
	"github.com/MiviaLabs/mivia-agent/internal/ui/screen/conversation"
	"github.com/MiviaLabs/mivia-agent/internal/ui/stream"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/demoharness"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/termprobe"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
	"golang.org/x/term"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, os.Environ()))
}

// The interactive cockpit paces its streamed events with
// uikitconfig.DemoScenarioPace (internal/uikit/demoharness), so a human
// watching --demo sees a stream rather than an instant dump. The
// non-interactive renderers below (--output json, non-TTY plain
// stream) render the whole fixture at once and use no pacing at all.

// initialComposerWidth is a placeholder only: Bubble Tea sends a
// WindowSizeMsg at startup before the first render, and the real width
// takes over there. It exists so the composer is constructible before
// the terminal size is known, not as an assumption about it.
const initialComposerWidth = 80

// mockCommands is the slash-command completion set for the demo build.
// Every name here is a command demoharness.Harness.Run actually
// implements (cmd/mivia-ui/main.go wires SetCommandRunner to it); the
// real list comes from the harness once the CLI refactor lands
// (docs/design/ui-isolation.md). It is defined here rather than inside
// the composer so the component stays free of any assumption about who
// supplies it.
func mockCommands() []composer.Command {
	return []composer.Command{
		{Name: "agents", Desc: "pick the agent (Mivia is the default orchestrator)"},
		{Name: "clear", Desc: "clear the transcript"},
		{Name: "compact", Desc: "compact the context"},
		{Name: "context", Desc: "show context usage"},
		{Name: "cost", Desc: "show the session spend"},
		{Name: "help", Desc: "show the keymap"},
		{Name: "model", Desc: "pick the model"},
		{Name: "quit", Desc: "exit mivia-ui"},
		{Name: "resume", Desc: "resume a previous session"},
		{Name: "settings", Desc: "open the settings screen"},
		{Name: "theme", Desc: "pick a theme"},
	}
}

type cfg struct {
	demo          bool
	scenario      string
	workspace     string
	themeName     string
	themeExplicit bool
	light         bool
	dark          bool
	noColor       bool
	outputJSON    bool
	noMouse       bool
	scrollLines   int
	fullRepaint   bool
}

func parseFlags(args []string, stderr io.Writer) (cfg, error) {
	fs := flag.NewFlagSet("mivia-ui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c cfg
	fs.BoolVar(&c.demo, "demo", true,
		"run the interactive cockpit against the demo harness's scripted, multi-turn "+
			"conversation; --demo=false selects a real chat.Session via internal/uiadapter "+
			"and requires --workspace to point at a configured project")
	fs.StringVar(&c.scenario, "scenario", demoharness.DefaultScenario,
		"which scripted conversation --demo plays: "+strings.Join(demoharness.Scenarios(), ", "))
	workspaceDefault := ""
	if cwd, err := os.Getwd(); err == nil {
		workspaceDefault = cwd
	}
	fs.StringVar(&c.workspace, "workspace", workspaceDefault,
		"workspace root for --demo=false; defaults to the current working directory")
	fs.StringVar(&c.themeName, "theme", "mivia-dark", "theme name (see --output json for available themes)")
	fs.BoolVar(&c.light, "light", false, "default to a light theme instead of --theme's default")
	fs.BoolVar(&c.dark, "dark", false, "default to a dark theme instead of --theme's default")
	fs.BoolVar(&c.noColor, "no-color", false, "force the no-colour/ASCII degradation tier")
	fs.BoolVar(&c.noMouse, "no-mouse", false, "keep the cockpit but release mouse capture (rule 6.5)")
	fs.IntVar(&c.scrollLines, "scroll-lines", uikitconfig.CockpitScrollLines,
		"rows one mouse-wheel notch scrolls (rule 6.6)")
	fs.BoolVar(&c.fullRepaint, "full-repaint", false,
		"force a full redraw on resize (Windows Terminal / ConPTY hazard)")
	outputFlag := fs.String("output", "", "\"json\" for NDJSON instead of the interactive TUI")
	if err := fs.Parse(args); err != nil {
		return cfg{}, err
	}
	if c.scrollLines < 1 {
		return cfg{}, fmt.Errorf("--scroll-lines must be 1 or more, got %d", c.scrollLines)
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "theme" {
			c.themeExplicit = true
		}
	})
	c.outputJSON = *outputFlag == "json"
	return c, nil
}

func run(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer, env []string) int {
	cfg, err := parseFlags(args, stderr)
	if err != nil {
		return 2
	}

	// Rule 6.6: the wheel speed is settable because terminals disagree
	// on how many events one notch produces. Applied once, before any
	// renderer path can read it; nothing writes it again.
	uikitconfig.CockpitScrollLines = cfg.scrollLines

	themes, err := theme.Embedded()
	if err != nil {
		fmt.Fprintln(stderr, "mivia-ui:", err)
		return 1
	}
	th, err := resolveTheme(themes, cfg)
	if err != nil {
		fmt.Fprintln(stderr, "mivia-ui:", err)
		return 1
	}

	events, err := stream.DefaultFixture()
	if err != nil {
		fmt.Fprintln(stderr, "mivia-ui:", err)
		return 1
	}

	if cfg.outputJSON {
		if err := jsonout.Render(stdout, events); err != nil {
			fmt.Fprintln(stderr, "mivia-ui:", err)
			return 1
		}
		return 0
	}

	f, isFile := stdout.(*os.File)
	isTTY := isFile && term.IsTerminal(int(f.Fd()))

	// Rule 6.4 and the section 4 hazard table: some environments must
	// never enter the cockpit. Each refusal names itself on one line
	// and falls back to the plain stream renderer, so the session still
	// works instead of silently showing something unusable.
	refuse := plainStreamReason(env, termprobe.Probe(env, tmuxVersion(env)))
	if refuse != "" || !isTTY {
		return renderPlain(stdout, stderr, events, refuse)
	}

	tier := theme.TierTrueColor
	if cfg.noColor {
		tier = theme.TierASCII
	} else {
		tier = theme.Detect(f, env)
	}
	return runCockpit(ctx, cfg, th, tier, themes, env, stdout, stderr)
}

// renderPlain writes the optional refusal line plus the plain stream.
func renderPlain(stdout, stderr io.Writer, events []uievent.Event, reason string) int {
	if reason != "" {
		fmt.Fprintln(stdout, "mivia-ui:", reason)
	}
	if err := stream.Render(stdout, events); err != nil {
		fmt.Fprintln(stderr, "mivia-ui:", err)
		return 1
	}
	return 0
}

// runCockpit starts the interactive cockpit on a terminal that passed
// every probe, against the named demo scenario. Startup warnings become
// permanent transcript notices, shown once.
//
// In --demo mode the cockpit runs against the demo harness
// (internal/uikit/demoharness), with all four port slots wired from the
// harness. In --demo=false mode the cockpit runs against a real
// chat.Session wired through internal/uiadapter.New; the four port
// slots stay nil and render as "unavailable" until a later phase
// surfaces them.
func runCockpit(ctx context.Context, c cfg, th theme.Theme, tier theme.Tier, themes []theme.Theme,
	env []string, stdout, stderr io.Writer) int {
	probe := termprobe.Probe(env, tmuxVersion(env))
	var (
		screen conversation.Screen
		err    error
	)
	if c.demo {
		screen, err = newDemoScreen(c, th, tier, themes, probe)
	} else {
		screen, err = newRealScreen(ctx, c, th, tier, themes)
	}
	if err != nil {
		fmt.Fprintln(stderr, "mivia-ui:", err)
		return 1
	}
	for _, w := range probe.Warnings {
		screen.Notice(w)
	}
	if !c.noMouse {
		screen.SetMouseOverrideHint(probe.MouseHint)
	}
	root := app.New(screen, th, tier, themes).
		WithOptions(app.Options{
			Mouse:       !c.noMouse,
			FullRepaint: c.fullRepaint || probe.FullRepaint,
		})

	p := tea.NewProgram(root, tea.WithOutput(stdout))
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(stderr, "mivia-ui:", err)
		return 1
	}
	return 0
}

// newDemoScreen wires the cockpit against the demo harness. The four
// port slots (commands, command-runner, subagent-threads, settings) are
// all backed by the harness so every slash command and subagent row is
// demonstrable in --demo. ObserveAgent seeds one synthetic subagent so
// the transcript has a visible row from the moment the cockpit renders.
func newDemoScreen(c cfg, th theme.Theme, tier theme.Tier, themes []theme.Theme, probe termprobe.Report) (conversation.Screen, error) {
	harness, err := demoharness.New(c.scenario, uikitconfig.DemoScenarioPace)
	if err != nil {
		return conversation.Screen{}, err
	}
	// initialComposerWidth only holds until Bubble Tea delivers the first
	// WindowSizeMsg, which it does at startup before the first render.
	screen := conversation.New(th, tier, themes, harness, harness, initialComposerWidth, nil)
	screen.SetCommands(mockCommands())
	screen.SetCommandRunner(harness)
	screen.SetSubagentThreads(harness)
	screen.SetSettings(harness.SettingsAdapters())
	screen.ObserveAgent("sa-1", &uievent.Progress{
		Status:     "running",
		Step:       2,
		TotalSteps: 3,
		Log: []string{
			"read internal/uikit/config/defaults.go",
			"listed 14 exported constants",
		},
	})
	return screen, nil
}

// newRealScreen wires the cockpit against a real chat.Session through
// internal/uiadapter.New. The four port slots are left nil: the
// conversation screen renders them as "unavailable" until the CLI
// refactor lands and the harness-side ports surface.
//
// WorkspaceRoot is resolved to an absolute path so the store path is
// canonical and the SQLite checkpoint file lands at a deterministic
// location regardless of how the caller invoked mivia-ui.
func newRealScreen(ctx context.Context, c cfg, th theme.Theme, tier theme.Tier, themes []theme.Theme) (conversation.Screen, error) {
	workspaceRoot, err := filepath.Abs(c.workspace)
	if err != nil {
		return conversation.Screen{}, fmt.Errorf("resolve workspace: %w", err)
	}
	resolved, err := config.Load(config.LoadOptions{
		WorkspaceRoot:      workspaceRoot,
		AllowMissingConfig: false,
	})
	if err != nil {
		return conversation.Screen{}, fmt.Errorf("load workspace config: %w", err)
	}
	completer, err := provider.New(resolved)
	if err != nil {
		return conversation.Screen{}, fmt.Errorf("build completer: %w", err)
	}
	storePath := resolved.StorePath
	if storePath == "" {
		storePath = workspace.NamespacePath(workspaceRoot, "session.sqlite")
	}
	adapter, cleanup, err := uiadapter.New(ctx, uiadapter.Input{
		Resolved:      resolved,
		WorkspaceRoot: workspaceRoot,
		MCPConfig:     resolved.MCP,
		SessionID:     "",
		StorePath:     storePath,
		Completer:     completer,
	})
	if err != nil {
		return conversation.Screen{}, fmt.Errorf("build adapter: %w", err)
	}
	// Cleanup is owned by the program lifetime; Bubble Tea's Run returns
	// when the user quits, so defer-close on cleanup is the right place.
	defer cleanup()
	return conversation.New(th, tier, themes, adapter, nil, initialComposerWidth, nil), nil
}

// plainStreamReason names the one reason the cockpit must not start, or
// "" when it may.
func plainStreamReason(env []string, probe termprobe.Report) string {
	switch {
	case termprobe.ScreenReader(env):
		return "screen-reader mode: the cockpit viewport is unreadable to a screen reader; rendering the plain transcript instead"
	case termprobe.DumbTerminal(env):
		return "TERM=dumb: a dumb terminal has no cursor addressing; rendering the plain transcript instead"
	case probe.RefuseReason != "":
		return probe.RefuseReason
	}
	return ""
}

// tmuxVersion reports the local tmux version when inside tmux, for the
// synchronized-output probe. Errors and absence mean unknown, which
// warns nothing.
func tmuxVersion(env []string) string {
	if !termprobe.InTmux(env) {
		return ""
	}
	return termprobe.LookupTmuxVersion(func() ([]byte, error) {
		return exec.Command("tmux", "-V").Output()
	})
}

// resolveTheme picks a theme by exact --theme name; --light/--dark only
// change the *default* name when --theme was not explicitly passed
// (there is no per-name light/dark variant grouping in the embedded set
// today - each theme is its own distinct Theme.Name).
func resolveTheme(themes []theme.Theme, c cfg) (theme.Theme, error) {
	name := c.themeName
	if !c.themeExplicit {
		switch {
		case c.light:
			if th, ok := firstByDark(themes, false); ok {
				name = th.Name
			}
		case c.dark:
			if th, ok := firstByDark(themes, true); ok {
				name = th.Name
			}
		}
	}
	for _, th := range themes {
		if th.Name == name {
			return th, nil
		}
	}
	return theme.Theme{}, fmt.Errorf("unknown --theme %q", name)
}

func firstByDark(themes []theme.Theme, dark bool) (theme.Theme, bool) {
	for _, th := range themes {
		if th.Dark == dark {
			return th, true
		}
	}
	return theme.Theme{}, false
}
