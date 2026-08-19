package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

func TestResolveTiersExplicit(t *testing.T) {
	cases := map[string]theme.Tier{
		"truecolor": theme.TierTrueColor,
		"256":       theme.Tier256,
		"16":        theme.Tier16,
		"ascii":     theme.TierASCII,
		"no-colour": theme.TierASCII,
		"NO-COLOR":  theme.TierASCII,
		"TrueColor": theme.TierTrueColor,
	}
	for input, want := range cases {
		got, err := resolveTiers(&bytes.Buffer{}, input, nil)
		if err != nil {
			t.Fatalf("resolveTiers(%q): %v", input, err)
		}
		if len(got) != 1 || got[0] != want {
			t.Errorf("resolveTiers(%q) = %v, want [%v]", input, got, want)
		}
	}
}

func TestResolveTiersAuto(t *testing.T) {
	got, err := resolveTiers(&bytes.Buffer{}, "", []string{"NO_COLOR=1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] == theme.TierTrueColor {
		t.Errorf("NO_COLOR should not auto-detect to truecolor, got %v", got)
	}
}

func TestResolveTiersUnknown(t *testing.T) {
	if _, err := resolveTiers(&bytes.Buffer{}, "not-a-tier", nil); err == nil {
		t.Fatal("expected error for unknown tier")
	}
}

func TestTierLabel(t *testing.T) {
	cases := []struct {
		tier theme.Tier
		want string
	}{
		{theme.TierTrueColor, "truecolor"},
		{theme.Tier256, "256"},
		{theme.Tier16, "16"},
		{theme.TierASCII, "ascii/no-colour"},
		{theme.TierNoTTY, "no-tty"},
	}
	for _, c := range cases {
		if got := tierLabel(c.tier); got != c.want {
			t.Errorf("tierLabel(%v) = %q, want %q", c.tier, got, c.want)
		}
	}
}

func TestHexRGB(t *testing.T) {
	r, g, b, err := hexRGB("#fafafa")
	if err != nil {
		t.Fatal(err)
	}
	if r != 250 || g != 250 || b != 250 {
		t.Errorf("hexRGB(#fafafa) = %d,%d,%d, want 250,250,250", r, g, b)
	}
	if _, _, _, err := hexRGB("bad"); err == nil {
		t.Fatal("expected error for invalid hex")
	}
	if _, _, _, err := hexRGB("#zzzzzz"); err == nil {
		t.Fatal("expected error for non-hex digits")
	}
}

func TestAnsi16Code(t *testing.T) {
	if got := ansi16Code(0); got != "30" {
		t.Errorf("ansi16Code(0) = %q, want 30", got)
	}
	if got := ansi16Code(7); got != "37" {
		t.Errorf("ansi16Code(7) = %q, want 37", got)
	}
	if got := ansi16Code(8); got != "90" {
		t.Errorf("ansi16Code(8) = %q, want 90", got)
	}
	if got := ansi16Code(15); got != "97" {
		t.Errorf("ansi16Code(15) = %q, want 97", got)
	}
}

func TestAnsiPrefixAndReset(t *testing.T) {
	truecolor := theme.Style{Hex: "#fafafa", ANSI16: -1}
	if got := ansiPrefix(truecolor); !strings.Contains(got, "38;2;250;250;250") {
		t.Errorf("ansiPrefix(truecolor) = %q, missing 24-bit escape", got)
	}
	if got := ansiReset(truecolor); got != reset {
		t.Errorf("ansiReset(truecolor) = %q, want %q", got, reset)
	}

	sixteen := theme.Style{ANSI16: 9}
	if got := ansiPrefix(sixteen); !strings.Contains(got, "91") {
		t.Errorf("ansiPrefix(ansi16=9) = %q, missing code 91", got)
	}

	bold := theme.Style{ANSI16: -1, Bold: true}
	if got := ansiPrefix(bold); got != "\x1b[1m" {
		t.Errorf("ansiPrefix(bold, no-colour) = %q, want bold-only escape", got)
	}

	noColor := theme.Style{ANSI16: -1}
	if got := ansiPrefix(noColor); got != "" {
		t.Errorf("ansiPrefix(plain no-colour) = %q, want empty", got)
	}
	if got := ansiReset(noColor); got != "" {
		t.Errorf("ansiReset(plain no-colour) = %q, want empty", got)
	}
}

func TestRunThemesAllThemesNoError(t *testing.T) {
	var out bytes.Buffer
	if err := runThemes(&out, []string{"--tier", "truecolor"}, nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Mivia Dark", "Mivia Light", "Mivia High Contrast"} {
		if !strings.Contains(out.String(), name) {
			t.Errorf("output missing theme %q", name)
		}
	}
}

func TestRunThemesUnparseableFlag(t *testing.T) {
	var out bytes.Buffer
	if err := runThemes(&out, []string{"--not-a-real-flag"}, nil); err == nil {
		t.Fatal("expected error for an unparseable flag")
	}
}

// TestRunThemesUnknownThemeNameIsSilentlyEmpty pins the current
// behaviour of an unmatched --theme filter: no error, no output. This is
// deliberate (a filter, not a lookup), but the test-review found nothing
// pinned it - a future change to the filter condition could silently
// alter this without any test noticing.
func TestRunThemesUnknownThemeNameIsSilentlyEmpty(t *testing.T) {
	var out bytes.Buffer
	if err := runThemes(&out, []string{"--theme", "does-not-exist"}, nil); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("expected empty output for an unmatched --theme filter, got %q", out.String())
	}
}
