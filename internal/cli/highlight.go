// Package cli - lightweight syntax highlighter for terminal code blocks.
// Uses pattern matching per language (no external parsing library).
// Each line is processed independently so it works with streaming output.
package cli

import (
	"fmt"
	"regexp"
	"strings"
)

// ANSI SGR codes: theme.go (ansi*). Diff bg codes also live there.

// langDef defines keyword sets and patterns for one language.
type langDef struct {
	keywords     []string // language keywords
	builtins     []string // built-in functions/names
	types        []string // type names
	singleLine   string   // single-line comment marker (e.g. "//")
	multiLineL   string   // multi-line comment open
	multiLineR   string   // multi-line comment close
	stringChars  []string // string delimiters (e.g. "'", "\"")
	extraPattern []patternRule
}

type patternRule struct {
	re   *regexp.Regexp
	ansi string // ANSI code to apply to match (excluding group 1 if needed)
}

// highlightLine applies language-specific highlighting to one code line.
// It handles: comments > strings > keywords > types > builtins > numbers.
// inMulti tracks multi-line comment state; returns updated state.
func highlightLine(line string, lang string, inMulti bool) (string, bool) {
	if lang == "diff" || lang == "patch" || lang == "udiff" {
		return highlightDiffLine(line), inMulti
	}
	if lang == "" || lang == "text" || lang == "plain" {
		return fmt.Sprintf("  %s%s%s%s", ansiBgDark, ansiYellow, line, ansiReset), false
	}

	def, ok := langDefs[lang]
	if !ok {
		// Unknown language: try generic fallback
		return fmt.Sprintf("  %s%s%s%s", ansiBgDark, ansiYellow, line, ansiReset), false
	}

	// Check for multi-line comment open/close.
	if def.multiLineL != "" && def.multiLineR != "" {
		if inMulti {
			// Look for close.
			idx := strings.Index(line, def.multiLineR)
			if idx >= 0 {
				// Render commented portion dim, then process the rest normally.
				endIdx := idx + len(def.multiLineR)
				comment := line[:endIdx]
				rest, nextMulti := highlightLine(line[endIdx:], lang, false)
				rest = strings.TrimPrefix(rest, "  ")
				return fmt.Sprintf("  %s%s%s%s%s%s%s", ansiBgDark, ansiDim, ansiItalic, comment, ansiReset, rest, ansiReset), nextMulti
			}
			return fmt.Sprintf("  %s%s%s%s%s", ansiBgDark, ansiDim, ansiItalic, line, ansiReset), true
		}
		// Check for open.
		idx := strings.Index(line, def.multiLineL)
		if idx >= 0 {
			closeIdx := strings.Index(line[idx+len(def.multiLineL):], def.multiLineR)
			if closeIdx >= 0 {
				// Complete multi-line comment on one line.
				endIdx := idx + len(def.multiLineL) + closeIdx + len(def.multiLineR)
				before := line[:idx]
				comment := line[idx:endIdx]
				after := line[endIdx:]
				b, _ := highlightLineNoComment(before, def)
				c := fmt.Sprintf("%s%s%s", ansiDim, ansiItalic, comment)
				a, _ := highlightLineNoComment(after, def)
				return fmt.Sprintf("  %s%s%s%s%s%s", ansiBgDark, ansiBgSafe(b), ansiReset, c, ansiBgSafe(a), ansiReset), false
			}
			// Multi-line comment spans to next line.
			before := line[:idx]
			comment := line[idx:]
			b, _ := highlightLineNoComment(before, def)
			return fmt.Sprintf("  %s%s%s%s%s%s", ansiBgDark, ansiBgSafe(b), ansiReset, ansiDim, ansiItalic, comment), true
		}
	}

	return highlightLineNoCommentFull(line, def)
}

// ansiBgSafe replaces all ansiReset in s with ansiReset+ansiBgDark so that
// dark code-block background is preserved across colored spans.
func ansiBgSafe(s string) string {
	return strings.ReplaceAll(s, ansiReset, ansiReset+ansiBgDark)
}

func highlightLineNoCommentFull(line string, def langDef) (string, bool) {
	out, _ := highlightLineNoComment(line, def)
	// Replace ansiReset inside token output with ansiReset+ansiBgDark so the
	// background is re-asserted after each colored span.  The final ansiReset
	// terminates the whole line normally.
	safe := strings.ReplaceAll(out, ansiReset, ansiReset+ansiBgDark)
	return fmt.Sprintf("  %s%s%s", ansiBgDark, safe, ansiReset), false
}

// highlightLineNoComment applies highlighting to a line assuming no comment
// markers exist (or we've already stripped them).
func highlightLineNoComment(line string, def langDef) (string, bool) {
	if line == "" {
		return "", false
	}

	// Handle single-line comments first.
	if def.singleLine != "" {
		idx := strings.Index(line, def.singleLine)
		if idx >= 0 {
			code := line[:idx]
			comment := line[idx:]
			c := highlightTokens(code, def)
			return fmt.Sprintf("%s%s%s%s", c, ansiDim, ansiItalic, comment), false
		}
	}

	return highlightTokens(line, def), false
}

// highlightTokens applies keyword/builtin/type/string/number highlighting.
// strRegion is a [start,end) range of a string literal.
type strRegion struct{ start, end int }

func highlightTokens(line string, def langDef) string {
	if line == "" {
		return ""
	}
	strRegions := findStringRegions(line, def.stringChars)

	var out strings.Builder
	i := 0
	for i < len(line) {
		if end, ok := stringRegionStartingAt(strRegions, i); ok {
			out.WriteString(ansiGreen)
			out.WriteString(line[i:end])
			out.WriteString(ansiReset)
			i = end
			continue
		}

		if positionInString(strRegions, i) {
			// Inside string but not at start - should not happen after above.
			out.WriteByte(line[i])
			i++
			continue
		}

		matched, next := extraPatternMatch(line, i, def.extraPattern, strRegions, &out)
		if matched {
			i = next
			continue
		}

		// Try to match a keyword/builtin/type at this position.
		if n := matchKeywordToken(line, i, def, &out); n > 0 {
			i += n
			continue
		}

		// Number literal.
		if i+1 < len(line) && isDigit(line[i]) {
			num := matchNumber(line, i)
			if num != "" {
				out.WriteString(ansiMagenta + num + ansiReset)
				i += len(num)
				continue
			}
		}

		// Default: emit as-is.
		out.WriteByte(line[i])
		i++
	}

	return out.String()
}

// matchKeywordToken writes the span for a known keyword/type/builtin starting
// at i and returns the bytes consumed, or 0 when i is not the start of such a
// word. Plain non-digit words are emitted whole so the token loop never
// rescans a long identifier run at every byte position (O(n²) on a 1 MiB
// single word); digit-containing words keep the per-byte path so embedded
// numbers are still colored magenta.
func matchKeywordToken(line string, i int, def langDef, out *strings.Builder) int {
	word := matchWord(line, i)
	if word == "" {
		return 0
	}
	lower := strings.ToLower(word)
	switch {
	case contains(def.keywords, lower):
		out.WriteString(ansiCyan + word + ansiReset)
		return len(word)
	case contains(def.types, lower):
		out.WriteString(ansiBlue + word + ansiReset)
		return len(word)
	case contains(def.builtins, lower):
		out.WriteString(ansiYellow + word + ansiReset)
		return len(word)
	case !containsDigit(word):
		out.WriteString(word)
		return len(word)
	}
	return 0
}

func extraPatternMatch(line string, i int, rules []patternRule, regions []strRegion, out *strings.Builder) (bool, int) {
	for _, rule := range rules {
		loc := rule.re.FindStringIndex(line[i:])
		if loc == nil || loc[0] != 0 {
			continue
		}
		start, end := i+loc[0], i+loc[1]
		if end <= start {
			// Zero-width match (e.g. yaml's `^\s*-?\s*` on a line with no
			// leading whitespace). Accepting it would return the scan position
			// unchanged, so highlightTokens would loop forever, growing the
			// output buffer unboundedly. Skipping it lets the keyword/number/
			// emit-byte fallback advance the scan.
			continue
		}
		if regionsOverlap(regions, start, end) {
			continue
		}
		out.WriteString(rule.ansi + line[start:end] + ansiReset)
		return true, end
	}
	return false, i
}

// mergeRegions merges overlapping or adjacent [start,end) regions.
func mergeRegions(regions []strRegion) []strRegion {
	if len(regions) == 0 {
		return nil
	}
	// Sort by start.
	for i := 0; i < len(regions); i++ {
		for j := i + 1; j < len(regions); j++ {
			if regions[j].start < regions[i].start {
				regions[i], regions[j] = regions[j], regions[i]
			}
		}
	}
	merged := []strRegion{regions[0]}
	for _, r := range regions[1:] {
		last := &merged[len(merged)-1]
		if r.start <= last.end {
			if r.end > last.end {
				last.end = r.end
			}
		} else {
			merged = append(merged, r)
		}
	}
	return merged
}

// matchWord returns the word/identifier at position i, or "".
func matchWord(line string, i int) string {
	if i >= len(line) {
		return ""
	}
	c := line[i]
	if !isIdentStart(c) {
		return ""
	}
	end := i
	for end < len(line) && isIdentCont(line[end]) {
		end++
	}
	return line[i:end]
}

// matchNumber returns a number literal at position i, or "".
func matchNumber(line string, i int) string {
	if i >= len(line) || !isDigit(line[i]) {
		return ""
	}
	end := i
	if i+1 < len(line) && line[i] == '0' && (line[i+1] == 'x' || line[i+1] == 'X') {
		// Hex.
		end = i + 2
		for end < len(line) && isHexDigit(line[end]) {
			end++
		}
	} else {
		for end < len(line) && (isDigit(line[end]) || line[end] == '.' || line[end] == 'f' || line[end] == 'F') {
			end++
		}
	}
	return line[i:end]
}

func isIdentStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

func isIdentCont(c byte) bool {
	return isIdentStart(c) || isDigit(c)
}

func isDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

// containsDigit reports whether s contains an ASCII digit. It guards the
// whole-word fast path in highlightTokens: words with digits keep the per-byte
// fallback so embedded numbers are still colored magenta.
func containsDigit(s string) bool {
	for i := 0; i < len(s); i++ {
		if isDigit(s[i]) {
			return true
		}
	}
	return false
}

func isHexDigit(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func contains(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

// highlightDiffLine colors a line from a diff code block.
// Delegates to the package-shared theme-token renderer (diff_style.go).
func highlightDiffLine(line string) string {
	return renderDiffLine(line)
}

// highlightCodeBlock takes a code block's language tag and content (no fences),
// returns the ANSI-highlighted block with background, one line per newline.
// The output has a trailing newline.
func highlightCodeBlock(lang, code string) string {
	if code == "" {
		return ""
	}
	lines := strings.Split(code, "\n")
	var out strings.Builder
	// Determine effective lang.
	lang = strings.ToLower(strings.TrimSpace(lang))
	if lang == "diff" || lang == "patch" || lang == "udiff" {
		lang = "diff"
	}

	inMulti := false
	for _, line := range lines {
		if out.Len() > 0 {
			out.WriteByte('\n')
		}
		if lang == "diff" {
			out.WriteString(highlightDiffLine(line))
		} else if _, ok := langDefs[lang]; ok {
			hl, nextMulti := highlightLine(line, lang, inMulti)
			out.WriteString(hl)
			inMulti = nextMulti
		} else if lang != "" && lang != "text" && lang != "plain" {
			// Unknown but specified - use generic.
			out.WriteString(fmt.Sprintf("  %s%s%s%s", ansiBgDark, ansiYellow, line, ansiReset))
		} else {
			// No language specified - plain yellow.
			out.WriteString(fmt.Sprintf("  %s%s%s%s", ansiBgDark, ansiYellow, line, ansiReset))
		}
	}
	return out.String()
}

// langDefs maps language names to keyword/pattern definitions.
