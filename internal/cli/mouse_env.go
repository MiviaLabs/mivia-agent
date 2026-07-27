package cli

import (
	"os"
	"strings"

	"golang.org/x/term"
)

// mouseAvailable reports whether the host looks capable of mouse cell-motion
// tracking for the TUI. Used to auto-enable mouse on startup when safe.
//
// Rules:
//   - MIVIA_MOUSE=0/false/off forces off; =1/true/on forces on
//   - stdin must be a TTY
//   - stdout or stderr must be a TTY (alt-screen apps need a real terminal)
//   - TERM empty/dumb/unknown → unavailable
//
// Most modern terminal emulators ignore mouse enable sequences if unsupported;
// we still avoid enabling on clearly non-interactive or dumb environments.
func mouseAvailable() bool {
	if v := strings.TrimSpace(os.Getenv("MIVIA_MOUSE")); v != "" {
		switch strings.ToLower(v) {
		case "0", "false", "off", "no", "disable", "disabled":
			return false
		case "1", "true", "on", "yes", "enable", "enabled":
			return true
		}
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false
	}
	outTTY := term.IsTerminal(int(os.Stdout.Fd())) || term.IsTerminal(int(os.Stderr.Fd()))
	if !outTTY {
		return false
	}
	termName := strings.ToLower(strings.TrimSpace(os.Getenv("TERM")))
	if termName == "" || termName == "dumb" || termName == "unknown" {
		return false
	}
	if strings.HasPrefix(termName, "dumb") {
		return false
	}
	return true
}
