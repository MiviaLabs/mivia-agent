package cli

import (
	"fmt"
	"regexp"
	"strings"
)

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
			{regexp.MustCompile(`"[^"]*"\s*:`), ansiCyan},           // keys
			{regexp.MustCompile(`\b(true|false|null)\b`), ansiBlue}, // literals
			{regexp.MustCompile(`\b-?\d+\.?\d*\b`), ansiMagenta},    // numbers
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
			{regexp.MustCompile(`^\s*-?\s*`), ansiYellow}, // list markers
			{regexp.MustCompile(`[\w_/-]+:`), ansiCyan},   // keys
			{regexp.MustCompile(`\|[ \t]*$`), ansiBlue},   // block scalar
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
		out.WriteString(fmt.Sprintf("%s ╭%s╮ %s %s%s\n", ansiDim, barOpen, getCodeIcon(lang), lang, ansiReset))
		out.WriteString(highlighted)
		if highlighted != "" && !strings.HasSuffix(highlighted, "\n") {
			out.WriteByte('\n')
		}
		barClose := strings.Repeat("─", min(48, 48))
		out.WriteString(fmt.Sprintf("%s ╰%s╯%s%s\n", ansiDim, barClose, ansiReset, ""))
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
