// Command mivia-ui is the new terminal UI (build spec step 7/first
// deliverable). It runs entirely against the replay fake today: real
// internal/chat harness wiring is deferred (build spec steps 2, 3, 8).
// A --demo flag exists for interface compatibility with that future
// state; there is currently no other mode to select.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/jsonout"
	"github.com/MiviaLabs/mivia-agent/internal/ui/screen/conversation"
	"github.com/MiviaLabs/mivia-agent/internal/ui/stream"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/replay"
	"golang.org/x/term"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Environ()))
}

// replayPace is how far apart replayed fixture events land in the
// interactive TUI, so a human watching --demo sees a stream rather than
// an instant dump. Non-interactive paths (--output json, non-TTY) don't
// use it: they render the whole fixture at once.
const replayPace = 30 * time.Millisecond

// initialComposerWidth is a placeholder only: Bubble Tea sends a
// WindowSizeMsg at startup before the first render, and the real width
// takes over there. It exists so the composer is constructible before
// the terminal size is known, not as an assumption about it.
const initialComposerWidth = 80

type config struct {
	demo          bool
	themeName     string
	themeExplicit bool
	light         bool
	dark          bool
	noColor       bool
	outputJSON    bool
}

func parseFlags(args []string, stderr io.Writer) (config, error) {
	fs := flag.NewFlagSet("mivia-ui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var cfg config
	fs.BoolVar(&cfg.demo, "demo", true, "run against the replayed fixture (currently the only mode)")
	fs.StringVar(&cfg.themeName, "theme", "mivia-dark", "theme name (see --output json for available themes)")
	fs.BoolVar(&cfg.light, "light", false, "default to a light theme instead of --theme's default")
	fs.BoolVar(&cfg.dark, "dark", false, "default to a dark theme instead of --theme's default")
	fs.BoolVar(&cfg.noColor, "no-color", false, "force the no-colour/ASCII degradation tier")
	outputFlag := fs.String("output", "", "\"json\" for NDJSON instead of the interactive TUI")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "theme" {
			cfg.themeExplicit = true
		}
	})
	cfg.outputJSON = *outputFlag == "json"
	return cfg, nil
}

func run(args []string, stdout io.Writer, stderr io.Writer, env []string) int {
	cfg, err := parseFlags(args, stderr)
	if err != nil {
		return 2
	}

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
	if !isFile || !term.IsTerminal(int(f.Fd())) {
		// Non-interactive: no alt-screen, no input loop to drive. Same
		// fallback shape as the classic REPL's non-TTY path.
		if err := stream.Render(stdout, events); err != nil {
			fmt.Fprintln(stderr, "mivia-ui:", err)
			return 1
		}
		return 0
	}

	tier := theme.TierTrueColor
	if cfg.noColor {
		tier = theme.TierASCII
	} else {
		tier = theme.Detect(f, env)
	}

	conv := replay.New(events, replayPace)
	// initialComposerWidth only holds until Bubble Tea delivers the first
	// WindowSizeMsg, which it does at startup before the first render.
	screen := conversation.New(th, tier, themes, conv, replay.NewApprover(), initialComposerWidth, nil)
	root := app.New(screen, th, tier, themes)

	p := tea.NewProgram(root, tea.WithOutput(stdout))
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(stderr, "mivia-ui:", err)
		return 1
	}
	return 0
}

// resolveTheme picks a theme by exact --theme name; --light/--dark only
// change the *default* name when --theme was not explicitly passed
// (there is no per-name light/dark variant grouping in the embedded set
// today - each theme is its own distinct Theme.Name).
func resolveTheme(themes []theme.Theme, cfg config) (theme.Theme, error) {
	name := cfg.themeName
	if !cfg.themeExplicit {
		switch {
		case cfg.light:
			if th, ok := firstByDark(themes, false); ok {
				name = th.Name
			}
		case cfg.dark:
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
