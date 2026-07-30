// Package cli — markdown → ANSI rendering for terminal chat UX.
package cli

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// ANSI formatting constants for markdown rendering.
const (
	ansiBold      = "\033[1m"
	ansiBoldEnd   = "\033[22m"
	ansiItalic    = "\033[3m"
	ansiUnderline = "\033[4m"
	ansiYellow    = "\033[33m"
	ansiCyan      = "\033[36m"
	ansiBlue      = "\033[34m"
	ansiGreen     = "\033[32m"
	ansiDim       = "\033[2m"
	ansiDimEnd    = "\033[22m"
	ansiRed       = "\033[31m"
	ansiMagenta   = "\033[35m"
	ansiBgDark    = "\033[48;5;236m"
	ansiBgReset   = "\033[49m"
	ansiReset     = "\033[0m"
)

// MarkdownWriter wraps an io.Writer and converts markdown to ANSI.
// Streaming: complete lines are formatted as they arrive.
// Table rows are buffered until a non-table line or Flush so columns can align.
type MarkdownWriter struct {
	w           io.Writer
	buf         strings.Builder
	tableBuf    []string
	inCodeBlock bool
	cbLang      string
	diffMode    bool
	width       int
}

// NewMarkdownWriter creates a markdown-to-ANSI streaming converter.
func NewMarkdownWriter(w io.Writer) *MarkdownWriter {
	return &MarkdownWriter{w: w, width: 80}
}

// SetWidth sets wrap/hr width hint.
func (mw *MarkdownWriter) SetWidth(w int) {
	if w > 20 {
		mw.width = w
	}
}

// Write implements io.Writer.
func (mw *MarkdownWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	mw.buf.Write(p)
	total := len(p)
	input := mw.buf.String()

	var out strings.Builder
	for {
		idx := strings.IndexByte(input, '\n')
		if idx < 0 {
			break
		}
		line := input[:idx]
		rest := input[idx+1:]
		if s := mw.processLine(line); s != "" {
			out.WriteString(s)
			out.WriteByte('\n')
		}
		input = rest
	}
	mw.buf.Reset()
	mw.buf.WriteString(input)

	if out.Len() > 0 {
		if _, err := io.WriteString(mw.w, out.String()); err != nil {
			return 0, err
		}
	}
	return total, nil
}

// Flush writes remaining buffered content (partial line + open table block).
func (mw *MarkdownWriter) Flush() error {
	var out strings.Builder
	if mw.buf.Len() > 0 {
		line := mw.buf.String()
		mw.buf.Reset()
		if s := mw.processLine(line); s != "" {
			out.WriteString(s)
		}
	}
	if len(mw.tableBuf) > 0 {
		if out.Len() > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(mw.flushTable())
	}
	if out.Len() == 0 {
		return nil
	}
	_, err := io.WriteString(mw.w, out.String())
	return err
}

// RenderMarkdown formats a full markdown document to ANSI.
func RenderMarkdown(s string, width int) string {
	var b strings.Builder
	mw := NewMarkdownWriter(&b)
	mw.SetWidth(width)
	_, _ = mw.Write([]byte(s))
	_ = mw.Flush()
	return strings.TrimRight(b.String(), "\n")
}

// processLine handles one logical line: buffers table rows, flushes tables on
// non-table boundaries, and formats everything else. May return a multi-line
// table block (joined with '\n', no trailing newline). Empty string means
// nothing to emit yet (buffered).
func (mw *MarkdownWriter) processLine(line string) string {
	trimmed := strings.TrimSpace(line)

	// Code fences and code-block body: flush any open table first, never buffer as table.
	if mw.inCodeBlock || strings.HasPrefix(trimmed, "```") {
		var prefix string
		if len(mw.tableBuf) > 0 {
			prefix = mw.flushTable()
		}
		formatted := mw.formatLine(line)
		return joinNonEmpty(prefix, formatted)
	}

	if isTableLine(trimmed) {
		mw.tableBuf = append(mw.tableBuf, trimmed)
		return ""
	}

	var prefix string
	if len(mw.tableBuf) > 0 {
		prefix = mw.flushTable()
	}
	return joinNonEmpty(prefix, mw.formatLine(line))
}

// Alignment: left (default), right, center.
func padCell(plain, formatted string, width int, align tableAlign) string {
	w := visibleWidth(plain)
	if w >= width {
		return formatted
	}
	pad := width - w
	switch align {
	case alignRight:
		return strings.Repeat(" ", pad) + formatted
	case alignCenter:
		left := pad / 2
		right := pad - left
		return strings.Repeat(" ", left) + formatted + strings.Repeat(" ", right)
	default:
		return formatted + strings.Repeat(" ", pad)
	}
}

// truncateVisible hard-truncates plain text to max visible columns, appending … if cut.
func truncateVisible(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if visibleWidth(s) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	budget := max - 1 // reserve for …
	var b strings.Builder
	w := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		rw := 1
		if isWideRune(r) {
			rw = 2
		}
		if w+rw > budget {
			break
		}
		b.WriteString(s[i : i+size])
		w += rw
		i += size
	}
	b.WriteString("…")
	return b.String()
}

func (mw *MarkdownWriter) formatLine(line string) string {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "```") {
		return mw.formatCodeFence(trimmed)
	}
	if mw.inCodeBlock {
		return mw.formatCodeLine(line)
	}
	return mw.formatNonCodeLine(line, trimmed)
}

func (mw *MarkdownWriter) formatCodeFence(trimmed string) string {
	if mw.inCodeBlock {
		mw.inCodeBlock = false
		lang := mw.cbLang
		mw.cbLang = ""
		mw.diffMode = false
		bar := strings.Repeat("─", min(mw.width-4, 48))
		return fmt.Sprintf("%s ╰%s╯ %s%s", ansiDim, bar, lang, ansiReset)
	}
	mw.inCodeBlock = true
	mw.cbLang = strings.ToLower(strings.TrimSpace(trimmed[3:]))
	mw.diffMode = mw.cbLang == "diff" || mw.cbLang == "patch" || mw.cbLang == "udiff"
	lang := mw.cbLang
	if lang == "" {
		lang = "code"
	}
	icon := "◆"
	if mw.diffMode {
		icon = "±"
	}
	bar := strings.Repeat("─", min(mw.width-4, 48))
	return fmt.Sprintf("%s ╭%s╮ %s %s%s", ansiDim, bar, icon, lang, ansiReset)
}

func (mw *MarkdownWriter) formatNonCodeLine(line, trimmed string) string {

	// Table rows are handled via processLine buffering — not here.

	// Task lists
	if strings.HasPrefix(trimmed, "- [ ] ") {
		return fmt.Sprintf("  %s☐%s %s", ansiDim, ansiReset, mw.formatInline(trimmed[6:]))
	}
	if strings.HasPrefix(trimmed, "- [x] ") || strings.HasPrefix(trimmed, "- [X] ") {
		return fmt.Sprintf("  %s✓%s %s", ansiGreen, ansiReset, mw.formatInline(trimmed[6:]))
	}

	// Headings
	if trimmed != "" && trimmed[0] == '#' {
		level := 0
		for level < len(trimmed) && trimmed[level] == '#' {
			level++
		}
		if level <= 6 && level < len(trimmed) && isSpace(trimmed[level]) {
			text := strings.TrimSpace(trimmed[level:])
			switch level {
			case 1:
				return fmt.Sprintf("\n%s%s%s%s", ansiBold, ansiCyan, mw.formatInline(text), ansiReset)
			case 2:
				return fmt.Sprintf("%s%s%s%s", ansiBold, ansiBlue, mw.formatInline(text), ansiReset)
			default:
				return fmt.Sprintf("%s%s%s", ansiBold, mw.formatInline(text), ansiBoldEnd)
			}
		}
	}

	// Blockquote
	if strings.HasPrefix(trimmed, "> ") || trimmed == ">" {
		content := strings.TrimPrefix(trimmed, "> ")
		content = strings.TrimPrefix(content, ">")
		return fmt.Sprintf("  %s│%s %s%s%s", ansiGreen, ansiReset, ansiDim, mw.formatInline(content), ansiReset)
	}

	if isHorizontalRule(trimmed) {
		n := min(mw.width-2, 56)
		if n < 8 {
			n = 8
		}
		return fmt.Sprintf(" %s%s%s", ansiDim, strings.Repeat("─", n), ansiDimEnd)
	}

	// Unordered list
	if len(trimmed) >= 2 && (trimmed[0] == '-' || trimmed[0] == '*') && isSpace(trimmed[1]) {
		content := strings.TrimSpace(trimmed[1:])
		return fmt.Sprintf("  %s•%s %s", ansiCyan, ansiReset, mw.formatInline(content))
	}

	// Ordered list
	if len(trimmed) >= 3 && trimmed[0] >= '1' && trimmed[0] <= '9' {
		dot := strings.IndexByte(trimmed, '.')
		if dot > 0 && dot+1 < len(trimmed) && isSpace(trimmed[dot+1]) {
			num := trimmed[:dot+1]
			content := strings.TrimSpace(trimmed[dot+1:])
			return fmt.Sprintf("  %s%s%s %s", ansiDim, num, ansiDimEnd, mw.formatInline(content))
		}
	}

	return mw.formatInline(line)
}

func (mw *MarkdownWriter) formatCodeLine(line string) string {
	// If we have a known language, use syntax highlighting.
	lang := mw.cbLang
	if lang != "" {
		if mw.diffMode || lang == "diff" || lang == "patch" || lang == "udiff" {
			return highlightDiffLine(line)
		}
		if _, ok := langDefs[lang]; ok {
			// Use the highlighted version — it includes background and reset.
			hl, _ := highlightLine(line, lang, false)
			return hl
		}
	}

	// Diff-aware coloring for heuristic diff lines (no language tag).
	trim := line
	if len(trim) > 0 && (mw.diffMode || looksLikeDiffLine(trim)) {
		switch {
		case strings.HasPrefix(trim, "+++") || strings.HasPrefix(trim, "---"):
			return fmt.Sprintf("  %s%s%s%s%s", ansiBgDark, ansiBold, ansiCyan, trim, ansiReset)
		case strings.HasPrefix(trim, "@@"):
			return fmt.Sprintf("  %s%s%s%s", ansiBgDark, ansiMagenta, trim, ansiReset)
		case strings.HasPrefix(trim, "+"):
			return fmt.Sprintf("  %s%s+%s%s%s", ansiBgDark, ansiGreen, trim[1:], ansiReset, "")
		case strings.HasPrefix(trim, "-"):
			return fmt.Sprintf("  %s%s-%s%s", ansiBgDark, ansiRed, trim[1:], ansiReset)
		default:
			return fmt.Sprintf("  %s%s%s%s", ansiBgDark, ansiDim, trim, ansiReset)
		}
	}
	return fmt.Sprintf("  %s%s%s%s", ansiBgDark, ansiYellow, line, ansiReset)
}

func looksLikeDiffLine(s string) bool {
	if len(s) == 0 {
		return false
	}
	if strings.HasPrefix(s, "+++ ") || strings.HasPrefix(s, "--- ") || strings.HasPrefix(s, "@@ ") {
		return true
	}
	// Single leading + or - with following content (not list)
	if (s[0] == '+' || s[0] == '-') && (len(s) == 1 || s[1] != ' ') {
		return true
	}
	if (s[0] == '+' || s[0] == '-') && len(s) > 1 {
		// "+ foo" in diff vs "- item" list — if we're in code block already, treat as diff
		return true
	}
	return false
}

func (mw *MarkdownWriter) formatInline(s string) string {
	var out strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '`' {
			end := strings.IndexByte(s[i+1:], '`')
			if end >= 0 {
				code := s[i+1 : i+1+end]
				out.WriteString(fmt.Sprintf("%s%s%s%s", ansiDim, ansiYellow, escANSI(code), ansiReset))
				i += end + 2
				continue
			}
		}
		if s[i] == '[' {
			closeB := strings.IndexByte(s[i+1:], ']')
			if closeB >= 0 && i+1+closeB+1 < len(s) && s[i+1+closeB+1] == '(' {
				closeP := strings.IndexByte(s[i+2+closeB:], ')')
				if closeP >= 0 {
					text := s[i+1 : i+1+closeB]
					url := s[i+2+closeB : i+2+closeB+closeP]
					out.WriteString(fmt.Sprintf("%s%s%s%s %s(%s)%s",
						ansiUnderline, ansiBlue, escANSI(text), ansiReset,
						ansiDim, escANSI(url), ansiDimEnd))
					i += closeB + closeP + 3
					continue
				}
			}
		}
		if i+1 < len(s) && s[i] == '*' && s[i+1] == '*' {
			end := findMarker(s[i+2:], "**")
			if end >= 0 {
				inner := s[i+2 : i+2+end]
				out.WriteString(fmt.Sprintf("%s%s%s", ansiBold, mw.formatInline(inner), ansiBoldEnd))
				i += end + 4
				continue
			}
		}
		if s[i] == '*' && (i+1 >= len(s) || s[i+1] != '*') {
			end := strings.IndexByte(s[i+1:], '*')
			if end >= 0 && (i+1+end+1 >= len(s) || s[i+1+end+1] != '*') {
				inner := s[i+1 : i+1+end]
				out.WriteString(fmt.Sprintf("%s%s%s", ansiItalic, escANSI(inner), ansiReset))
				i += end + 2
				continue
			}
		}
		if s[i] == '\\' && i+1 < len(s) {
			out.WriteByte(s[i+1])
			i += 2
			continue
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String()
}

func findMarker(s string, marker string) int {
	return strings.Index(s, marker)
}

func escANSI(s string) string {
	return strings.ReplaceAll(s, "\033", "␛")
}

func isHorizontalRule(s string) bool {
	if len(s) < 3 {
		return false
	}
	var ch byte
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' {
			continue
		}
		if ch == 0 {
			ch = s[i]
			if ch != '-' && ch != '*' && ch != '_' {
				return false
			}
		} else if s[i] != ch {
			return false
		}
	}
	return ch != 0
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t'
}
