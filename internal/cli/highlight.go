// Package cli — lightweight syntax highlighter for terminal code blocks.
// Uses pattern matching per language (no external parsing library).
// Each line is processed independently so it works with streaming output.
package cli

import (
	"fmt"
	"regexp"
	"strings"
)

// highlightAnsi codes reused from markdown.go constants.
const (
	hlCyan    = "\033[36m" // keywords
	hlGreen   = "\033[32m" // strings
	hlYellow  = "\033[33m" // numbers, builtins
	hlBlue    = "\033[34m" // types
	hlMagenta = "\033[35m" // preprocessor, decorators
	hlRed     = "\033[31m" // special
	hlDim     = "\033[2m"  // comments
	hlDimEnd  = "\033[22m"
	hlBold    = "\033[1m"
	hlBoldEnd = "\033[22m"
	hlItalic  = "\033[3m"
	hlReset   = "\033[0m"
	hlBgDark  = "\033[48;5;236m"
	hlBgReset = "\033[49m"
)

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
		return fmt.Sprintf("  %s%s%s%s", hlBgDark, hlYellow, line, hlReset), false
	}

	def, ok := langDefs[lang]
	if !ok {
		// Unknown language: try generic fallback
		return fmt.Sprintf("  %s%s%s%s", hlBgDark, hlYellow, line, hlReset), false
	}

	// Check for multi-line comment open/close.
	if def.multiLineL != "" && def.multiLineR != "" {
		if inMulti {
			// Look for close.
			idx := strings.Index(line, def.multiLineR)
			if idx >= 0 {
				after := line[idx+len(def.multiLineR):]
				// Render commented portion dim, then process the rest normally.
				before := line[:idx]
				rest, _ := highlightLine(after, lang, false)
				return fmt.Sprintf("  %s%s%s%s%s%s", hlBgDark, hlDim, hlItalic, before, hlReset, rest), false
			}
			return fmt.Sprintf("  %s%s%s%s%s", hlBgDark, hlDim, hlItalic, line, hlReset), true
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
				c := fmt.Sprintf("%s%s%s", hlDim, hlItalic, comment)
				a, _ := highlightLineNoComment(after, def)
				return fmt.Sprintf("  %s%s%s%s%s%s", hlBgDark, hlBgSafe(b), hlReset, c, hlBgSafe(a), hlReset), false
			}
			// Multi-line comment spans to next line.
			before := line[:idx]
			comment := line[idx:]
			b, _ := highlightLineNoComment(before, def)
			return fmt.Sprintf("  %s%s%s%s%s%s", hlBgDark, hlBgSafe(b), hlReset, hlDim, hlItalic, comment), true
		}
	}

	return highlightLineNoCommentFull(line, def)
}

// hlBgSafe replaces all hlReset in s with hlReset+hlBgDark so that
// dark code-block background is preserved across colored spans.
func hlBgSafe(s string) string {
	return strings.ReplaceAll(s, hlReset, hlReset+hlBgDark)
}

func highlightLineNoCommentFull(line string, def langDef) (string, bool) {
	out, _ := highlightLineNoComment(line, def)
	// Replace hlReset inside token output with hlReset+hlBgDark so the
	// background is re-asserted after each colored span.  The final hlReset
	// terminates the whole line normally.
	safe := strings.ReplaceAll(out, hlReset, hlReset+hlBgDark)
	return fmt.Sprintf("  %s%s%s", hlBgDark, safe, hlReset), false
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
			return fmt.Sprintf("%s%s%s%s", c, hlDim, hlItalic, comment), false
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
			out.WriteString(hlGreen)
			out.WriteString(line[i:end])
			out.WriteString(hlReset)
			i = end
			continue
		}

		if positionInString(strRegions, i) {
			// Inside string but not at start — should not happen after above.
			out.WriteByte(line[i])
			i++
			continue
		}

		// Check extra patterns first (for JSON/YAML key matching, etc.).
		matched := false
		for _, rule := range def.extraPattern {
			loc := rule.re.FindStringIndex(line[i:])
			if loc != nil && loc[0] == 0 {
				// Verify match doesn't overlap with protected chars.
				matchStart := i + loc[0]
				matchEnd := i + loc[1]
				if !regionsOverlap(strRegions, matchStart, matchEnd) {
					out.WriteString(rule.ansi + line[matchStart:matchEnd] + hlReset)
					i = matchEnd
					matched = true
					break
				}
			}
		}
		if matched {
			continue
		}

		// Try to match a keyword/builtin/type at this position.
		word := matchWord(line, i)
		if word != "" {
			lower := strings.ToLower(word)
			if contains(def.keywords, lower) {
				out.WriteString(hlCyan + word + hlReset)
				i += len(word)
				continue
			}
			if contains(def.types, lower) {
				out.WriteString(hlBlue + word + hlReset)
				i += len(word)
				continue
			}
			if contains(def.builtins, lower) {
				out.WriteString(hlYellow + word + hlReset)
				i += len(word)
				continue
			}
		}

		// Number literal.
		if i+1 < len(line) && isDigit(line[i]) {
			num := matchNumber(line, i)
			if num != "" {
				out.WriteString(hlMagenta + num + hlReset)
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

// GitHub-style diff background colors.
const (
	hlBgDiffDel = "\033[48;5;88m" // dark red background for deletions
	hlBgDiffAdd = "\033[48;5;22m" // dark green background for additions
	hlFgDiffDel = "\033[31m"      // red foreground for deleted text
	hlFgDiffAdd = "\033[32m"      // green foreground for added text
)

// highlightDiffLine colors a line from a diff code block using GitHub-style
// full-width backgrounds: dark red for deletions, dark green for additions,
// dark bg for context/headers.
func highlightDiffLine(line string) string {
	trim := line
	switch {
	case strings.HasPrefix(trim, "+++") || strings.HasPrefix(trim, "---"):
		// File header: bold cyan on dark background — no extra prefix.
		return fmt.Sprintf("  %s%s%s%s", hlBgDark, hlBold, hlCyan, trim)
	case strings.HasPrefix(trim, "@@"):
		// Hunk header: magenta on dark background.
		return fmt.Sprintf("  %s%s%s", hlBgDark, hlMagenta, trim)
	case strings.HasPrefix(trim, "+"):
		// Added line: green text on dark green background. Keep + prefix.
		return fmt.Sprintf("  %s%s%s", hlBgDiffAdd, hlFgDiffAdd, trim)
	case strings.HasPrefix(trim, "-"):
		// Removed line: red text on dark red background. Keep - prefix.
		return fmt.Sprintf("  %s%s%s", hlBgDiffDel, hlFgDiffDel, trim)
	default:
		// Context line: dim text on dark background.
		return fmt.Sprintf("  %s%s%s", hlBgDark, hlDim, trim)
	}
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
			// Unknown but specified — use generic.
			out.WriteString(fmt.Sprintf("  %s%s%s%s", hlBgDark, hlYellow, line, hlReset))
		} else {
			// No language specified — plain yellow.
			out.WriteString(fmt.Sprintf("  %s%s%s%s", hlBgDark, hlYellow, line, hlReset))
		}
	}
	return out.String()
}

// langDefs maps language names to keyword/pattern definitions.
var langDefs = map[string]langDef{
	"go": {
		keywords: []string{
			"break", "case", "chan", "const", "continue", "default", "defer",
			"else", "fallthrough", "for", "func", "go", "goto", "if", "import",
			"interface", "map", "package", "range", "return", "select", "struct",
			"switch", "type", "var",
		},
		builtins: []string{
			"append", "cap", "close", "complex", "copy", "delete", "imag",
			"len", "make", "new", "panic", "print", "println", "real", "recover",
			"nil", "true", "false", "iota",
		},
		types: []string{
			"bool", "byte", "complex64", "complex128", "error", "float32",
			"float64", "int", "int8", "int16", "int32", "int64",
			"rune", "string", "uint", "uint8", "uint16", "uint32", "uint64",
			"uintptr",
		},
		singleLine:  "//",
		multiLineL:  "/*",
		multiLineR:  "*/",
		stringChars: []string{"\"", "`"},
	},
	"python": {
		keywords: []string{
			"and", "as", "assert", "async", "await", "break", "class", "continue",
			"def", "del", "elif", "else", "except", "finally", "for", "from",
			"global", "if", "import", "in", "is", "lambda", "nonlocal", "not",
			"or", "pass", "raise", "return", "try", "while", "with", "yield",
		},
		builtins: []string{
			"print", "len", "range", "type", "int", "str", "float", "list",
			"dict", "set", "tuple", "bool", "open", "input", "super", "self",
			"True", "False", "None", "isinstance", "enumerate", "zip", "map",
			"filter", "sorted", "reversed", "any", "all", "sum", "min", "max",
			"abs", "hasattr", "getattr", "setattr",
		},
		types:       []string{},
		singleLine:  "#",
		multiLineL:  `"""`,
		multiLineR:  `"""`,
		stringChars: []string{"\"", "'"},
	},
	"javascript": {
		keywords: []string{
			"async", "await", "break", "case", "catch", "class", "const",
			"continue", "debugger", "default", "delete", "do", "else", "export",
			"extends", "finally", "for", "function", "if", "import", "in",
			"instanceof", "let", "new", "of", "return", "static", "super",
			"switch", "this", "throw", "try", "typeof", "var", "void", "while",
			"with", "yield",
		},
		builtins: []string{
			"console", "require", "module", "exports", "process", "Buffer",
			"setTimeout", "setInterval", "clearTimeout", "clearInterval",
			"parseInt", "parseFloat", "isNaN", "JSON", "Math", "Date",
			"Array", "Object", "String", "Number", "Boolean", "Map", "Set",
			"Promise", "Symbol", "undefined", "null", "true", "false",
			"document", "window", "fetch",
		},
		types:       []string{},
		singleLine:  "//",
		multiLineL:  "/*",
		multiLineR:  "*/",
		stringChars: []string{"\"", "'", "`"},
	},
	"typescript": {
		keywords: []string{
			"async", "await", "break", "case", "catch", "class", "const",
			"continue", "debugger", "default", "delete", "do", "else", "enum",
			"export", "extends", "finally", "for", "function", "if", "import",
			"in", "instanceof", "interface", "let", "new", "of", "return",
			"static", "super", "switch", "this", "throw", "try", "type",
			"typeof", "var", "void", "while", "with", "yield",
		},
		builtins: []string{
			"console", "require", "module", "exports", "process", "Buffer",
			"setTimeout", "setInterval", "parseInt", "parseFloat", "JSON",
			"Math", "Date", "Array", "Object", "String", "Number", "Boolean",
			"Map", "Set", "Promise", "Symbol", "undefined", "null", "true",
			"false", "fetch", "unknown", "never", "any",
		},
		types: []string{
			"string", "number", "boolean", "void", "null", "undefined",
			"never", "any", "unknown", "object", "symbol", "bigint",
			"Record", "Partial", "Required", "Readonly", "Pick", "Omit",
			"Exclude", "Extract", "ReturnType", "Parameters",
		},
		singleLine:  "//",
		multiLineL:  "/*",
		multiLineR:  "*/",
		stringChars: []string{"\"", "'", "`"},
	},
	"rust": {
		keywords: []string{
			"as", "async", "await", "break", "const", "continue", "crate",
			"dyn", "else", "enum", "extern", "false", "fn", "for", "if",
			"impl", "in", "let", "loop", "match", "mod", "move", "mut",
			"pub", "ref", "return", "self", "Self", "static", "struct",
			"super", "trait", "true", "type", "union", "unsafe", "use",
			"where", "while",
		},
		builtins: []string{
			"Some", "None", "Ok", "Err", "Option", "Result", "Box", "Vec",
			"String", "HashMap", "println", "format", "panic", "assert",
			"assert_eq", "assert_ne", "unreachable", "unimplemented",
			"clone", "copy", "into", "from", "as_ref", "as_mut",
		},
		types: []string{
			"u8", "u16", "u32", "u64", "u128", "i8", "i16", "i32", "i64",
			"i128", "f32", "f64", "bool", "char", "usize", "isize", "str",
		},
		singleLine:  "//",
		multiLineL:  "/*",
		multiLineR:  "*/",
		stringChars: []string{"\"", "'"},
	},
	"json": {
		keywords:    []string{},
		builtins:    []string{},
		types:       []string{},
		singleLine:  "",
		multiLineL:  "",
		multiLineR:  "",
		stringChars: []string{"\""},
		extraPattern: []patternRule{
			{regexp.MustCompile(`"[^"]*"\s*:`), hlCyan},           // keys
			{regexp.MustCompile(`\b(true|false|null)\b`), hlBlue}, // literals
			{regexp.MustCompile(`\b-?\d+\.?\d*\b`), hlMagenta},    // numbers
		},
	},
	"yaml": {
		keywords:    []string{},
		builtins:    []string{},
		types:       []string{},
		singleLine:  "#",
		multiLineL:  "",
		multiLineR:  "",
		stringChars: []string{"\"", "'"},
		extraPattern: []patternRule{
			{regexp.MustCompile(`^\s*-?\s*`), hlYellow}, // list markers
			{regexp.MustCompile(`[\w_/-]+:`), hlCyan},   // keys
			{regexp.MustCompile(`\|[ \t]*$`), hlBlue},   // block scalar
		},
	},
	"shell": {
		keywords: []string{
			"if", "then", "else", "elif", "fi", "for", "while", "do", "done",
			"case", "esac", "in", "function", "return", "exit", "continue",
			"break", "select", "until",
		},
		builtins: []string{
			"echo", "printf", "export", "source", "set", "unset", "read",
			"shift", "declare", "typeset", "local", "cd", "pwd", "ls",
			"cat", "grep", "sed", "awk", "find", "xargs", "sort", "uniq",
			"wc", "cut", "tr", "head", "tail", "tee", "test", "let",
			"exec", "trap", "kill", "exit",
			"true", "false",
		},
		types:       []string{},
		singleLine:  "#",
		multiLineL:  "",
		multiLineR:  "",
		stringChars: []string{"\"", "'"},
	},
}

// highlightCodeBlockInMarkdown finds code fences in markdown text and
// replaces their content with syntax-highlighted ANSI. Returns the
// original text with code blocks enhanced.
func highlightCodeBlockInMarkdown(markdown string) string {
	var out strings.Builder
	i := 0
	for i < len(markdown) {
		// Find ``` marker at start of line.
		rest := markdown[i:]
		fenceIdx := strings.Index(rest, "```")
		if fenceIdx < 0 {
			out.WriteString(markdown[i:])
			break
		}
		// Write up to the fence.
		out.WriteString(markdown[i : i+fenceIdx])
		i += fenceIdx

		// Ensure we're at a code fence (start of line or after newline).
		if fenceIdx > 0 && rest[fenceIdx-1] != '\n' {
			// Not a code fence (```midline is inline code).
			out.WriteString("```")
			i += 3
			continue
		}

		// Read language tag.
		fenceStart := i + 3 // skip ```
		lineEnd := strings.IndexByte(markdown[fenceStart:], '\n')
		if lineEnd < 0 {
			// Incomplete fence at end of text.
			out.WriteString(markdown[i:])
			break
		}
		lang := strings.TrimSpace(markdown[fenceStart : fenceStart+lineEnd])
		i = fenceStart + lineEnd + 1 // past \n

		// Read content until closing fence.
		contentEnd := strings.Index(markdown[i:], "\n```")
		var content string
		if contentEnd < 0 {
			// No closing fence — rest is code.
			content = markdown[i:]
			i = len(markdown)
		} else {
			content = markdown[i : i+contentEnd]
			i = i + contentEnd + 4 // past \n + ```
			// Skip trailing characters on fence line.
			after := strings.IndexByte(markdown[i:], '\n')
			if after >= 0 {
				i += after + 1
			} else {
				i = len(markdown)
			}
		}

		// Highlight the code block content.
		highlighted := highlightCodeBlock(lang, content)

		// Rebuild fence: open + lang, then highlighted content, then close.
		barOpen := strings.Repeat("─", min(48, 48))
		out.WriteString(fmt.Sprintf("%s ╭%s╮ %s %s%s\n", hlDim, barOpen, getCodeIcon(lang), lang, hlReset))
		out.WriteString(highlighted)
		if highlighted != "" && !strings.HasSuffix(highlighted, "\n") {
			out.WriteByte('\n')
		}
		barClose := strings.Repeat("─", min(48, 48))
		out.WriteString(fmt.Sprintf("%s ╰%s╯%s%s\n", hlDim, barClose, hlReset, ""))
	}

	if out.Len() > 0 {
		return out.String()
	}
	return markdown
}

func getCodeIcon(lang string) string {
	switch lang {
	case "diff", "patch", "udiff":
		return "±"
	case "go":
		return "◆"
	case "python":
		return "▶"
	case "javascript", "typescript":
		return "⬡"
	case "rust":
		return "⚙"
	case "json":
		return "{}"
	case "yaml":
		return "‖"
	case "shell", "bash", "sh":
		return "$"
	default:
		return "◆"
	}
}
