package main

import (
	"flag"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

const reset = "\x1b[0m"

func runThemes(w io.Writer, args []string, env []string) error {
	fs := flag.NewFlagSet("themes", flag.ContinueOnError)
	themeName := fs.String("theme", "", "only print this theme (default: all embedded themes)")
	tierFlag := fs.String("tier", "", "truecolor|256|16|ascii (default: auto-detect from TERM/NO_COLOR/CLICOLOR)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	themes, err := theme.Embedded()
	if err != nil {
		return err
	}
	sort.Slice(themes, func(i, j int) bool { return themes[i].Name < themes[j].Name })

	tiers, err := resolveTiers(w, *tierFlag, env)
	if err != nil {
		return err
	}

	for _, th := range themes {
		if *themeName != "" && th.Name != *themeName {
			continue
		}
		for _, tier := range tiers {
			printThemeAtTier(w, th, tier)
		}
	}
	return nil
}

func resolveTiers(w io.Writer, flagVal string, env []string) ([]theme.Tier, error) {
	if flagVal == "" {
		return []theme.Tier{theme.Detect(w, env)}, nil
	}
	switch strings.ToLower(flagVal) {
	case "truecolor":
		return []theme.Tier{theme.TierTrueColor}, nil
	case "256":
		return []theme.Tier{theme.Tier256}, nil
	case "16":
		return []theme.Tier{theme.Tier16}, nil
	case "ascii", "no-colour", "no-color":
		return []theme.Tier{theme.TierASCII}, nil
	default:
		return nil, fmt.Errorf("unknown --tier %q (want truecolor|256|16|ascii)", flagVal)
	}
}

func printThemeAtTier(w io.Writer, th theme.Theme, tier theme.Tier) {
	fmt.Fprintf(w, "== %s (%s) ==\n", th.Label, tierLabel(tier))

	fmt.Fprintln(w, "-- roles --")
	for _, r := range theme.AllRoles() {
		style := th.Resolve(r, tier)
		fmt.Fprintf(w, "  %s%-18s ████ sample text%s\n", ansiPrefix(style), r, ansiReset(style))
	}

	fmt.Fprintln(w, "-- diff pair --")
	printSwatchLine(w, th, tier, theme.RoleDiffAddFG, "+ added line")
	printSwatchLine(w, th, tier, theme.RoleDiffDelFG, "- removed line")

	fmt.Fprintln(w, "-- status set --")
	for _, r := range theme.StatusRoles() {
		printSwatchLine(w, th, tier, r, string(r))
	}
	fmt.Fprintln(w)
}

func printSwatchLine(w io.Writer, th theme.Theme, tier theme.Tier, r theme.Role, label string) {
	style := th.Resolve(r, tier)
	fmt.Fprintf(w, "  %s%s%s\n", ansiPrefix(style), label, ansiReset(style))
}

func tierLabel(tier theme.Tier) string {
	switch tier {
	case theme.TierTrueColor:
		return "truecolor"
	case theme.Tier256:
		return "256"
	case theme.Tier16:
		return "16"
	case theme.TierASCII:
		return "ascii/no-colour"
	case theme.TierNoTTY:
		return "no-tty"
	default:
		return "unknown"
	}
}

// ansiPrefix renders style.Bold/Dim (which survive NO_COLOR) plus colour
// where the tier carries one. This is a demo-only, hand-rolled SGR writer;
// internal/ui/** itself stays free of raw ANSI per the package rule.
func ansiPrefix(s theme.Style) string {
	var codes []string
	if s.Bold {
		codes = append(codes, "1")
	}
	if s.Dim {
		codes = append(codes, "2")
	}
	switch {
	case s.Hex != "":
		r, g, b, err := hexRGB(s.Hex)
		if err == nil {
			codes = append(codes, fmt.Sprintf("38;2;%d;%d;%d", r, g, b))
		}
	case s.ANSI16 >= 0:
		codes = append(codes, ansi16Code(s.ANSI16))
	}
	if len(codes) == 0 {
		return ""
	}
	return "\x1b[" + strings.Join(codes, ";") + "m"
}

func ansiReset(s theme.Style) string {
	if s.Bold || s.Dim || s.Hex != "" || s.ANSI16 >= 0 {
		return reset
	}
	return ""
}

func ansi16Code(idx int) string {
	if idx < 8 {
		return strconv.Itoa(30 + idx)
	}
	return strconv.Itoa(90 + (idx - 8))
}

func hexRGB(hex string) (r, g, b int, err error) {
	h := strings.TrimPrefix(hex, "#")
	if len(h) != 6 {
		return 0, 0, 0, fmt.Errorf("invalid hex colour %q", hex)
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return 0, 0, 0, err
	}
	return int((v >> 16) & 0xff), int((v >> 8) & 0xff), int(v & 0xff), nil
}
