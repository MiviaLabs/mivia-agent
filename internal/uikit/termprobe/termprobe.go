// Package termprobe turns terminal environment facts into cockpit
// decisions. Every function here is pure over an env slice (and, where
// a live query is needed, over the query's result), so the decisions
// are testable without a terminal - the effects on a real terminal are
// not testable here and are exercised only by the integration tests.
//
// The hazard table is docs/design/cockpit-research.md section 4.
package termprobe

import (
	"fmt"
	"strconv"
	"strings"
)

// Report is the set of cockpit decisions one startup probe produces.
type Report struct {
	// RefuseReason, when non-empty, names a hazard that breaks the
	// cockpit outright. The caller must not enter the cockpit and must
	// say why on one line.
	RefuseReason string

	// Warnings are one-time startup hints for hazards the cockpit can
	// run through: old tmux without synchronized output, iTerm2 with
	// mouse reporting off, tmux owning the wheel.
	Warnings []string

	// FullRepaint is set when the terminal is known to coalesce
	// positioned writes wrongly (Windows Terminal / ConPTY), so the
	// caller should enable the full-repaint mode.
	FullRepaint bool

	// MouseHint names the key that overrides mouse capture in the
	// detected terminal (rule 6.5), or lists the candidates when the
	// terminal cannot be identified.
	MouseHint string
}

// Probe inspects env plus, when inside tmux, the reported tmux version
// ("tmux 3.4"; empty when unknown) and returns the cockpit decisions.
func Probe(env []string, tmuxVersion string) Report {
	var r Report
	if InTmuxControlMode(env) {
		r.RefuseReason = "tmux control mode (tmux -CC) breaks the cockpit screen and mouse modes; run without -CC"
	}
	inTmux := InTmux(env)
	if w, warn := OldTmuxWarning(tmuxVersion); inTmux && warn {
		r.Warnings = append(r.Warnings, w)
	}
	if getenv(env, "TERM_PROGRAM") == "iTerm.app" && !InTmuxControlMode(env) {
		r.Warnings = append(r.Warnings,
			"iTerm2's default profile has mouse reporting off; turn it on for the wheel and clicks to reach mivia")
	}
	if inTmux {
		r.Warnings = append(r.Warnings,
			"if the wheel does not scroll, tmux owns it: set 'mouse on' in tmux.conf or run 'set -g mouse on'")
	}
	if IsConPTY(env) {
		r.FullRepaint = true
	}
	r.MouseHint = MouseOverrideHint(env)
	return r
}

// InTmux reports whether the environment runs inside tmux. tmux sets
// $TMUX in client sessions.
func InTmux(env []string) bool {
	return getenv(env, "TMUX") != ""
}

// InTmuxControlMode detects the tmux -CC hazard: iTerm2 integration
// mode runs tmux with TERM in the screen family while iTerm2 owns the
// outer terminal. That combination breaks the alternate screen and
// mouse tracking, and double-click can corrupt the terminal state, so
// the cockpit refuses rather than corrupting.
//
// This is a heuristic over env: no variable names control mode
// directly. iTerm2 always sets TERM_PROGRAM=iTerm.app, and tmux's
// default TERM is screen or tmux prefixed, so the pair is the signal.
func InTmuxControlMode(env []string) bool {
	if getenv(env, "TERM_PROGRAM") != "iTerm.app" {
		return false
	}
	term := getenv(env, "TERM")
	return strings.HasPrefix(term, "screen") || strings.HasPrefix(term, "tmux")
}

// OldTmuxWarning parses a "tmux X.Y" version string and warns when the
// version is 3.6 or older: those have no synchronized output, so
// redraws can tear. An unparseable or empty version warns not.
func OldTmuxWarning(version string) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(version))
	if len(fields) != 2 || fields[0] != "tmux" {
		return "", false
	}
	parts := strings.Split(fields[1], ".")
	if len(parts) < 2 {
		return "", false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return "", false
	}
	if major > 3 || (major == 3 && minor > 6) {
		return "", false
	}
	return fmt.Sprintf("tmux %d.%d has no synchronized output, so redraws can tear; upgrade tmux for smooth repaints", major, minor), true
}

// IsConPTY reports the Windows Terminal / ConPTY hazard: WT_SESSION is
// set by Windows Terminal. ConPTY coalesces positioned writes wrongly
// and leaves stale cells, which the full-repaint mode corrects.
func IsConPTY(env []string) bool {
	return getenv(env, "WT_SESSION") != ""
}

// MouseOverrideHint names the terminal's own key that overrides mouse
// capture (rule 6.5). Over SSH the terminal often cannot be identified,
// so the candidates are listed rather than guessing one wrong.
func MouseOverrideHint(env []string) string {
	switch getenv(env, "TERM_PROGRAM") {
	case "iTerm.app":
		return "Option"
	case "Apple_Terminal":
		return "Fn"
	}
	if getenv(env, "SSH_TTY") != "" || getenv(env, "SSH_CONNECTION") != "" {
		return "Fn, Option or Shift, depending on the terminal"
	}
	return "Shift"
}

// ScreenReader reports screen-reader mode (rule 6.4): MIVIA_SCREEN_READER
// set to any value other than 0 or false. The cockpit must never start
// in this mode - an app-owned viewport is unreadable to a screen reader
// (ux-rules.md rule 9.1) - so the caller renders the plain stream and
// says why.
func ScreenReader(env []string) bool {
	v := strings.TrimSpace(getenv(env, "MIVIA_SCREEN_READER"))
	if v == "" {
		return false
	}
	switch strings.ToLower(v) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}

// DumbTerminal reports TERM=dumb (ux-rules.md rule 9.6): a dumb
// terminal has no cursor addressing, so repaint-in-place is invalid and
// the plain stream must render instead.
func DumbTerminal(env []string) bool {
	return getenv(env, "TERM") == "dumb"
}

// LookupTmuxVersion runs the caller-supplied `tmux -V` query and
// returns its trimmed output ("" on error). The runner is injected so
// tests never shell out; the caller wires exec.Command("tmux", "-V").
func LookupTmuxVersion(run func() ([]byte, error)) string {
	out, err := run()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func getenv(env []string, key string) string {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return kv[len(prefix):]
		}
	}
	return ""
}
