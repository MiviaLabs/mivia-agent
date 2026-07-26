// Package cli implements mivia command handlers.
package cli

import (
	"fmt"
	"io"
	"strings"
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
	ansiReset     = "\033[0m"
)

// MarkdownWriter wraps an io.Writer and converts markdown formatting
// to ANSI escape sequences in streaming fashion. It buffers partial
// lines and processes formatting per complete line, maintaining state
// for multi-line constructs (code blocks).
type MarkdownWriter struct {
	w           io.Writer
	buf         strings.Builder
	inCodeBlock bool
	cbLang      string
}

// NewMarkdownWriter creates a markdown-to-ANSI streaming converter.
func NewMarkdownWriter(w io.Writer) *MarkdownWriter {
	return &MarkdownWriter{w: w}
}

// Write implements io.Writer. It buffers data until newlines, then
// processes complete lines with markdown formatting.
func (mw *MarkdownWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	mw.buf.Write(p)
	total := len(p)
	if mw.buf.Len() == 0 {
		return total, nil
	}

	input := mw.buf.String()

	// Process all complete lines.
	var out strings.Builder
	for {
		idx := strings.IndexByte(input, '\n')
		if idx < 0 {
			break
		}
		line := input[:idx]
		rest := input[idx+1:]
		out.WriteString(mw.formatLine(line))
		out.WriteByte('\n')
		input = rest
	}

	// Keep remaining incomplete line in buffer.
	mw.buf.Reset()
	mw.buf.WriteString(input)

	if out.Len() > 0 {
		_, err := io.WriteString(mw.w, out.String())
		if err != nil {
			return 0, err
		}
	}
	return total, nil
}

// Flush writes any remaining buffered content without a trailing newline.
func (mw *MarkdownWriter) Flush() error {
	if mw.buf.Len() == 0 {
		return nil
	}
	line := mw.buf.String()
	mw.buf.Reset()
	_, err := io.WriteString(mw.w, mw.formatLine(line))
	return err
}

// formatLine processes a single complete line of text (without trailing newline).
func (mw *MarkdownWriter) formatLine(line string) string {
	trimmed := strings.TrimSpace(line)

	// --- Code block fence open/close ---
	if strings.HasPrefix(trimmed, "```") {
		if mw.inCodeBlock {
			mw.inCodeBlock = false
			return fmt.Sprintf("%s```%s", ansiDim, ansiDimEnd)
		}
		mw.inCodeBlock = true
		mw.cbLang = strings.TrimSpace(trimmed[3:])
		lang := mw.cbLang
		if lang == "" {
			lang = "code"
		}
		return fmt.Sprintf("%s%s%s %s%s", ansiDim, ansiDimEnd, ansiYellow, lang, ansiReset)
	}

	// Inside code block — render in amber/dim.
	if mw.inCodeBlock {
		return fmt.Sprintf("  %s%s%s", ansiYellow, line, ansiReset)
	}

	// --- Block-level formatting ---

	// Task list: - [ ] / - [x] (must check BEFORE unordered list).
	if strings.HasPrefix(trimmed, "- [ ] ") {
		return fmt.Sprintf("  %s☐%s %s", ansiDim, ansiDimEnd, mw.formatInline(trimmed[6:]))
	}
	if strings.HasPrefix(trimmed, "- [x] ") || strings.HasPrefix(trimmed, "- [X] ") {
		return fmt.Sprintf("  %s☑%s %s", ansiGreen, ansiReset, mw.formatInline(trimmed[6:]))
	}

	// Headings
	if trimmed != "" && trimmed[0] == '#' {
		level := 0
		for level < len(trimmed) && trimmed[level] == '#' {
			level++
		}
		if level <= 6 && level < len(trimmed) && isSpace(trimmed[level]) {
			text := strings.TrimSpace(trimmed[level:])
			switch {
			case level == 1:
				return fmt.Sprintf("%s%s%s", ansiBold, mw.formatInline(text), ansiBoldEnd)
			case level <= 3:
				return fmt.Sprintf("%s%s%s", ansiBold, mw.formatInline(text), ansiBoldEnd)
			default:
				return fmt.Sprintf("%s%s%s%s", ansiDim, ansiBold, mw.formatInline(text), ansiBoldEnd)
			}
		}
	}

	// Blockquote
	if strings.HasPrefix(trimmed, "> ") || trimmed == ">" {
		content := strings.TrimPrefix(trimmed, "> ")
		content = strings.TrimPrefix(content, ">")
		return fmt.Sprintf(" %s▎%s %s%s", ansiGreen, ansiReset, mw.formatInline(content), ansiReset)
	}

	// Horizontal rule
	if isHorizontalRule(trimmed) {
		return fmt.Sprintf(" %s%s%s", ansiDim, strings.Repeat("─", 40), ansiDimEnd)
	}

	// Unordered list
	if len(trimmed) >= 2 && (trimmed[0] == '-' || trimmed[0] == '*') && isSpace(trimmed[1]) {
		content := strings.TrimSpace(trimmed[1:])
		return fmt.Sprintf("  %s•%s %s", ansiDim, ansiDimEnd, mw.formatInline(content))
	}

	// Ordered list
	if len(trimmed) >= 3 && trimmed[0] >= '1' && trimmed[0] <= '9' && trimmed[1] == '.' && isSpace(trimmed[2]) {
		content := strings.TrimSpace(trimmed[2:])
		return fmt.Sprintf("  %s%s%s %s", ansiDim, trimmed[:2], ansiDimEnd, mw.formatInline(content))
	}

	return mw.formatInline(line)
}

// formatInline handles inline markdown formatting within a line.
// Processes **bold**, *italic*, `code`, and [links](url).
// All user text is passed through escANSI to prevent raw ESC bytes.
func (mw *MarkdownWriter) formatInline(s string) string {
	var out strings.Builder
	i := 0
	for i < len(s) {
		// Code span: `code`
		if s[i] == '`' {
			end := strings.IndexByte(s[i+1:], '`')
			if end >= 0 {
				code := s[i+1 : i+1+end]
				out.WriteString(fmt.Sprintf("%s%s%s%s", ansiDim, ansiYellow, escANSI(code), ansiReset))
				i += end + 2
				continue
			}
		}

		// Link: [text](url)
		if s[i] == '[' {
			closeB := strings.IndexByte(s[i+1:], ']')
			if closeB >= 0 && i+1+closeB+1 < len(s) && s[i+1+closeB+1] == '(' {
				closeP := strings.IndexByte(s[i+2+closeB:], ')')
				if closeP >= 0 {
					text := s[i+1 : i+1+closeB]
					url := s[i+2+closeB : i+2+closeB+closeP]
					out.WriteString(fmt.Sprintf("%s%s%s%s%s(%s)%s",
						ansiUnderline, ansiBlue, escANSI(text), ansiReset,
						ansiDim, url, ansiDimEnd))
					i += closeB + closeP + 3
					continue
				}
			}
		}

		// Bold: **text**
		if i+1 < len(s) && s[i] == '*' && s[i+1] == '*' {
			end := findMarker(s[i+2:], "**")
			if end >= 0 {
				inner := s[i+2 : i+2+end]
				out.WriteString(fmt.Sprintf("%s%s%s", ansiBold, mw.formatInline(inner), ansiBoldEnd))
				i += end + 4
				continue
			}
		}

		// Italic: *text* (not part of **)
		if s[i] == '*' && (i+1 >= len(s) || s[i+1] != '*') {
			end := strings.IndexByte(s[i+1:], '*')
			if end >= 0 && (i+1+end+1 >= len(s) || s[i+1+end+1] != '*') {
				inner := s[i+1 : i+1+end]
				out.WriteString(fmt.Sprintf("%s%s%s", ansiItalic, escANSI(inner), ansiReset))
				i += end + 2
				continue
			}
		}

		// Escaped character
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

// findMarker finds the next occurrence of marker (e.g., "**") in s.
// Returns the position before the marker, or -1 if not found.
func findMarker(s string, marker string) int {
	return strings.Index(s, marker)
}

// escANSI replaces literal ESC bytes with a visible symbol to prevent
// terminal escape sequence injection from model output.
func escANSI(s string) string {
	return strings.ReplaceAll(s, "\033", "␛")
}

// isHorizontalRule checks if a trimmed line is a horizontal rule
// (at least 3 identical characters from - * _).
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

// isSpace checks if a byte is whitespace.
func isSpace(b byte) bool {
	return b == ' ' || b == '\t'
}
