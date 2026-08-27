package termprobe

import (
	"errors"
	"strings"
	"testing"
)

func TestInTmuxControlMode(t *testing.T) {
	cases := []struct {
		name string
		env  []string
		want bool
	}{
		{
			name: "iTerm2 with tmux screen TERM",
			env:  []string{"TERM_PROGRAM=iTerm.app", "TERM=screen-256color", "TMUX=/tmp/tmux-0/default"},
			want: true,
		},
		{
			name: "iTerm2 with tmux-prefixed TERM",
			env:  []string{"TERM_PROGRAM=iTerm.app", "TERM=tmux-256color", "TMUX=/tmp/tmux-0/default"},
			want: true,
		},
		{
			name: "plain iTerm2, no tmux",
			env:  []string{"TERM_PROGRAM=iTerm.app", "TERM=xterm-256color"},
			want: false,
		},
		{
			name: "tmux outside iTerm2",
			env:  []string{"TERM_PROGRAM=tmux", "TERM=screen", "TMUX=/tmp/tmux-0/default"},
			want: false,
		},
		{
			name: "plain terminal",
			env:  []string{"TERM=xterm-256color"},
			want: false,
		},
	}
	for _, c := range cases {
		if got := InTmuxControlMode(c.env); got != c.want {
			t.Errorf("%s: InTmuxControlMode = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestProbeRefusesControlMode(t *testing.T) {
	r := Probe([]string{"TERM_PROGRAM=iTerm.app", "TERM=screen", "TMUX=x"}, "")
	if r.RefuseReason == "" {
		t.Fatal("tmux control mode must produce a refusal")
	}
	if !strings.Contains(r.RefuseReason, "-CC") {
		t.Errorf("refusal %q must name tmux control mode", r.RefuseReason)
	}
}

func TestProbePlainTerminalIsClean(t *testing.T) {
	r := Probe([]string{"TERM=xterm-256color"}, "")
	if r.RefuseReason != "" {
		t.Errorf("plain terminal refused: %q", r.RefuseReason)
	}
	if len(r.Warnings) != 0 {
		t.Errorf("plain terminal warned: %v", r.Warnings)
	}
	if r.FullRepaint {
		t.Error("plain terminal must not force full repaint")
	}
	if r.MouseHint != "Shift" {
		t.Errorf("MouseHint = %q, want Shift", r.MouseHint)
	}
}

func TestOldTmuxWarning(t *testing.T) {
	cases := []struct {
		version string
		warn    bool
	}{
		{"tmux 3.6", true},
		{"tmux 3.4", true},
		{"tmux 2.9a", false}, // suffix breaks the minor parse; refuse to guess
		{"tmux 3", false},    // no minor part; refuse to guess
		{"tmux 3.7", false},
		{"tmux 4.0", false},
		{"", false},
		{"weird", false},
	}
	for _, c := range cases {
		if w, warn := OldTmuxWarning(c.version); warn != c.warn {
			t.Errorf("OldTmuxWarning(%q) = (%q,%v), want warn=%v", c.version, w, warn, c.warn)
		}
	}
	if w, _ := OldTmuxWarning("tmux 3.4"); !strings.Contains(w, "synchronized output") {
		t.Errorf("warning %q must say what the old version lacks", w)
	}
}

func TestProbeWarnsInsideTmux(t *testing.T) {
	r := Probe([]string{"TERM=screen", "TMUX=x"}, "tmux 3.4")
	found := 0
	for _, w := range r.Warnings {
		switch {
		case strings.Contains(w, "synchronized output"):
			found |= 1
		case strings.Contains(w, "mouse on"):
			found |= 2
		}
	}
	if found != 3 {
		t.Errorf("inside old tmux, want the version and wheel warnings, got %v", r.Warnings)
	}
}

func TestProbeUnknownTmuxVersionStillHintsWheel(t *testing.T) {
	r := Probe([]string{"TERM=screen", "TMUX=x"}, "")
	if len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "mouse on") {
		t.Errorf("unknown version should drop the version warning only, got %v", r.Warnings)
	}
}

func TestProbeITerm2HintsMouseReporting(t *testing.T) {
	r := Probe([]string{"TERM_PROGRAM=iTerm.app", "TERM=xterm-256color"}, "")
	found := false
	for _, w := range r.Warnings {
		if strings.Contains(w, "mouse reporting") {
			found = true
		}
	}
	if !found {
		t.Errorf("iTerm2 must produce the mouse-reporting hint, got %v", r.Warnings)
	}
}

func TestProbeConPTYForcesFullRepaint(t *testing.T) {
	r := Probe([]string{"TERM=xterm-256color", "WT_SESSION=abc"}, "")
	if !r.FullRepaint {
		t.Error("WT_SESSION must set FullRepaint")
	}
}

func TestMouseOverrideHint(t *testing.T) {
	cases := []struct {
		name string
		env  []string
		want string
	}{
		{"iTerm2", []string{"TERM_PROGRAM=iTerm.app"}, "Option"},
		{"Terminal.app", []string{"TERM_PROGRAM=Apple_Terminal"}, "Fn"},
		{"xterm", []string{"TERM=xterm"}, "Shift"},
		{"ssh unknown terminal", []string{"SSH_TTY=/dev/pts/3"}, "Fn, Option or Shift, depending on the terminal"},
		{"ssh connection", []string{"SSH_CONNECTION=1.2.3.4 5 6.7.8.9 10"}, "Fn, Option or Shift, depending on the terminal"},
	}
	for _, c := range cases {
		if got := MouseOverrideHint(c.env); got != c.want {
			t.Errorf("%s: hint = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestScreenReader(t *testing.T) {
	cases := []struct {
		name string
		env  []string
		want bool
	}{
		{"set to 1", []string{"MIVIA_SCREEN_READER=1"}, true},
		{"set to true", []string{"MIVIA_SCREEN_READER=true"}, true},
		{"set to a name", []string{"MIVIA_SCREEN_READER=nvda"}, true},
		{"set to 0", []string{"MIVIA_SCREEN_READER=0"}, false},
		{"set to false", []string{"MIVIA_SCREEN_READER=false"}, false},
		{"unset", nil, false},
	}
	for _, c := range cases {
		if got := ScreenReader(c.env); got != c.want {
			t.Errorf("%s: ScreenReader = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDumbTerminal(t *testing.T) {
	if !DumbTerminal([]string{"TERM=dumb"}) {
		t.Error("TERM=dumb must be detected")
	}
	if DumbTerminal([]string{"TERM=xterm"}) {
		t.Error("TERM=xterm must not be dumb")
	}
}

func TestInTmux(t *testing.T) {
	if !InTmux([]string{"TMUX=/tmp/x"}) {
		t.Error("TMUX set must report inside tmux")
	}
	if InTmux([]string{"TERM=xterm"}) {
		t.Error("no TMUX must report outside tmux")
	}
}

func TestLookupTmuxVersion(t *testing.T) {
	if got := LookupTmuxVersion(func() ([]byte, error) { return []byte("tmux 3.4\n"), nil }); got != "tmux 3.4" {
		t.Errorf("got %q, want the trimmed version line", got)
	}
	if got := LookupTmuxVersion(func() ([]byte, error) { return nil, errors.New("no tmux") }); got != "" {
		t.Errorf("got %q, want empty on error", got)
	}
}
