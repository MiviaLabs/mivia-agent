package skills

import (
	"fmt"
	"sort"
	"strings"
)

// maxFrontmatterBytes is the maximum size of frontmatter we will parse,
// mirroring maxSkillBytes for consistency.
const maxFrontmatterBytes = 256 << 10

// ParseFrontmatter parses a strict YAML-subset frontmatter block delimited
// by "---" markers on their own lines. It recognises:
//
//   - key: scalar
//   - key: [a, b, c]        (flow sequence)
//   - key:                  (block sequence, subsequent indented "- item" lines)
//   - key:                  (nested map, subsequent indented "k: v" lines)
//   - key: > / >- / | / |- / |+  (block scalar; indented body lines follow)
//   - # comments and blank lines, skipped anywhere including inside a sequence
//
// Everything else (anchors, multi-doc, quoted keys, etc.)
// is a hard error naming the line number. Rejecting beats guessing: a silently
// dropped key is the class of bug this parser exists to prevent.
//
// Unknown keys are NOT rejected here - the returned map uses raw key names.
// Callers must reject keys they do not understand; use
// ParseFrontmatterKnownWithClosing, which is the safe entry point.
func ParseFrontmatter(data []byte) (map[string]any, error) {
	front, _, ok, err := frontmatterLinesWithClosing(data)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return parseFrontLines(front)
}

// checkUnknownKeys returns an error naming every key of m that is not in the
// known set, listing the recognised keys for the author.
func checkUnknownKeys(m map[string]any, known map[string]bool) error {
	var unknown []string
	for k := range m {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	knownList := make([]string, 0, len(known))
	for k := range known {
		knownList = append(knownList, k)
	}
	sort.Strings(knownList)
	return fmt.Errorf("unknown frontmatter key(s) %v; recognised: %v", unknown, knownList)
}

// ParseFrontmatterKnownWithClosing parses the frontmatter and rejects any key
// not in the known set, so a field that nothing consumes cannot be introduced
// silently. It also returns the line index of the closing "---" delimiter.
// When no frontmatter is present, closing is -1.
func ParseFrontmatterKnownWithClosing(data []byte, known map[string]bool) (map[string]any, int, error) {
	front, closingLine, ok, err := frontmatterLinesWithClosing(data)
	if err != nil {
		return nil, -1, err
	}
	if !ok {
		return nil, -1, nil
	}
	m, err := parseFrontLines(front)
	if err != nil {
		return nil, -1, err
	}
	if err := checkUnknownKeys(m, known); err != nil {
		return nil, -1, err
	}
	return m, closingLine, nil
}

// frontmatterLinesWithClosing returns the lines between the opening and
// closing "---" plus the line index of the closing delimiter (1-based in the
// original document). ok is false when the document has no frontmatter block.
func frontmatterLinesWithClosing(data []byte) (front []string, closingLine int, ok bool, err error) {
	if len(data) > maxFrontmatterBytes {
		return nil, -1, false, fmt.Errorf("frontmatter exceeds %d bytes", maxFrontmatterBytes)
	}
	lines := strings.Split(normalizeNewlines(string(data)), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil, -1, false, nil
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return lines[1:i], i, true, nil
		}
	}
	return nil, -1, false, fmt.Errorf("unterminated frontmatter (no closing ---)")
}

// normalizeNewlines collapses CRLF and lone CR to LF.
func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// fmParser holds block-sequence, nested-map, and block-scalar accumulation
// state across lines.
type fmParser struct {
	result  map[string]any
	key     string
	block   []string
	inBlock bool
	// nested-map collection: indented "k: v" lines under a bare "key:"
	inMap     bool
	mapData   map[string]any
	mapIndent int
	// block-scalar collection: indented body lines under "key: >-" etc.
	scalarStyle string
	scalarLines []string
}

func (p *fmParser) flush() {
	if p.inBlock {
		p.result[p.key] = p.block
		p.block = nil
		p.inBlock = false
	}
	if p.inMap {
		p.result[p.key] = p.mapData
		p.mapData = nil
		p.inMap = false
	}
	if p.scalarStyle != "" {
		p.result[p.key] = foldBlockScalar(p.scalarStyle, p.scalarLines)
		p.scalarStyle = ""
		p.scalarLines = nil
	}
}

// foldBlockScalar renders collected body lines per YAML folded (>) or literal
// (|) rules. Indentation was preserved in scalarLines; the common indent is
// stripped here. Chomping: "-" strips trailing newlines; default clips to one.
func foldBlockScalar(style string, lines []string) string {
	indent := -1
	for _, l := range lines {
		trimmed := strings.TrimLeft(l, " ")
		if trimmed == "" {
			continue
		}
		n := len(l) - len(trimmed)
		if indent < 0 || n < indent {
			indent = n
		}
	}
	if indent < 0 {
		indent = 0
	}
	stripped := make([]string, len(lines))
	for i, l := range lines {
		if len(l) >= indent {
			stripped[i] = l[indent:]
		} else {
			stripped[i] = strings.TrimLeft(l, " ")
		}
	}
	var b strings.Builder
	if strings.HasPrefix(style, "|") {
		for _, l := range stripped {
			b.WriteString(l)
			b.WriteString("\n")
		}
	} else {
		for i, l := range stripped {
			if l == "" {
				b.WriteString("\n")
				continue
			}
			if i > 0 && stripped[i-1] != "" {
				b.WriteString(" ")
			}
			b.WriteString(l)
		}
		b.WriteString("\n")
	}
	out := b.String()
	if strings.HasSuffix(style, "-") {
		out = strings.TrimRight(out, "\n")
	} else {
		out = strings.TrimRight(out, "\n") + "\n"
	}
	return out
}

// blockScalarBody reports whether rest is a YAML block-scalar header: a
// '>' or '|' indicator optionally followed by one chomping (+/-) and/or one
// explicit indentation digit, in either order. A leading digit is NOT an
// indicator: "2|" is a plain scalar, not a header.
func blockScalarBody(rest string) (string, bool) {
	s := strings.TrimRight(rest, " ")
	if s == "" || (s[0] != '>' && s[0] != '|') {
		return "", false
	}
	style := string(s[0])
	var hasChomp, hasDigit bool
	for _, ch := range s[1:] {
		switch {
		case ch == '+' || ch == '-':
			if hasChomp {
				return "", false
			}
			hasChomp = true
			style += string(ch)
		case ch >= '1' && ch <= '9':
			if hasDigit {
				return "", false
			}
			hasDigit = true
			// The digit is validated but never appended to style: the fold
			// derives indentation from the body, and style's prefix/suffix
			// probes encode the indicator and chomping only.
		default:
			return "", false
		}
	}
	return style, true
}

// nextIsIndentedMapLine reports whether the next meaningful line after idx
// is an indented "k: v" line (a nested map), as opposed to a list item or
// nothing at all, and returns that line's indent.
func nextIsIndentedMapLine(front []string, idx int) (int, bool) {
	for j := idx + 1; j < len(front); j++ {
		trimmed := strings.TrimSpace(front[j])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if isIndented(front[j]) && trimmed != "-" && !strings.HasPrefix(trimmed, "- ") {
			return indentOf(front[j]), true
		}
		return 0, false
	}
	return 0, false
}

// mapLine parses one indented "k: v" line inside a nested map. Values are
// plain scalars; deeper nesting fails closed.
func (p *fmParser) mapLine(trimmed string, lineNum int, indent int) error {
	if indent > p.mapIndent {
		return fmt.Errorf("line %d: nested map %q does not support deeper nesting", lineNum, p.key)
	}
	colon := strings.Index(trimmed, ":")
	if colon <= 0 {
		return fmt.Errorf("line %d: expected indented \"key: value\" inside nested map %q", lineNum, p.key)
	}
	sub := strings.TrimSpace(trimmed[:colon])
	val, err := unquote(strings.TrimSpace(trimmed[colon+1:]))
	if err != nil {
		return fmt.Errorf("line %d: key %q: %v", lineNum, sub, err)
	}
	if _, exists := p.mapData[sub]; exists {
		return fmt.Errorf("line %d: duplicate key %q in nested map %q", lineNum, sub, p.key)
	}
	p.mapData[sub] = val
	return nil
}

func parseFrontLines(front []string) (map[string]any, error) {
	p := &fmParser{result: make(map[string]any)}
	for i, raw := range front {
		lineNum := i + 2 // 1-based, accounting for the opening "---"
		trimmed := strings.TrimSpace(raw)
		if p.scalarStyle != "" {
			// Inside a block-scalar body blank lines and indented lines are
			// content: they must not be skipped, folded away, or comment-
			// stripped. A non-indented, non-blank line terminates the block
			// scalar (as in YAML) and is parsed as the next frontmatter key -
			// folding it into the scalar would silently swallow keys and
			// defeat the duplicate- and unknown-key contracts below.
			if trimmed == "" || isIndented(raw) {
				p.scalarLines = append(p.scalarLines, raw)
				continue
			}
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if isIndented(raw) {
			if !p.inBlock {
				if p.inMap {
					if err := p.mapLine(trimmed, lineNum, indentOf(raw)); err != nil {
						return nil, err
					}
					continue
				}
				return nil, fmt.Errorf("line %d: unexpected indented line (no enclosing block sequence, nested map, or block scalar)", lineNum)
			}
			item, err := blockItem(trimmed, lineNum)
			if err != nil {
				return nil, err
			}
			p.block = append(p.block, item)
			continue
		}
		p.flush()
		if err := p.keyLine(trimmed, lineNum, front, i); err != nil {
			return nil, err
		}
	}
	p.flush()
	return p.result, nil
}

// keyLine parses a non-indented "key: ..." line.
func (p *fmParser) keyLine(trimmed string, lineNum int, front []string, idx int) error {
	colon := strings.Index(trimmed, ":")
	if colon < 0 {
		return fmt.Errorf("line %d: expected key: value (no colon found)", lineNum)
	}
	key := strings.TrimSpace(trimmed[:colon])
	if key == "" {
		return fmt.Errorf("line %d: empty key", lineNum)
	}
	// A repeated key is ambiguous and must not silently last-win.
	if _, exists := p.result[key]; exists {
		return fmt.Errorf("line %d: duplicate frontmatter key %q", lineNum, key)
	}
	rest := strings.TrimSpace(trimmed[colon+1:])
	switch {
	case strings.HasPrefix(rest, "["):
		if !strings.HasSuffix(rest, "]") {
			return fmt.Errorf("line %d: unclosed flow sequence", lineNum)
		}
		items, err := splitFlowSequence(strings.TrimSpace(rest[1 : len(rest)-1]))
		if err != nil {
			return fmt.Errorf("line %d: key %q: %v", lineNum, key, err)
		}
		p.result[key] = items
	case rest == "":
		if startsBlockSequence(front, idx) {
			p.key, p.block, p.inBlock = key, nil, true
		} else if indent, ok := nextIsIndentedMapLine(front, idx); ok {
			p.key, p.mapData, p.inMap = key, make(map[string]any), true
			p.mapIndent = indent
		} else {
			p.result[key] = ""
		}
	default:
		if style, ok := blockScalarBody(rest); ok {
			p.key, p.scalarStyle, p.scalarLines = key, style, nil
			return nil
		}
		val, err := unquote(rest)
		if err != nil {
			return fmt.Errorf("line %d: key %q: %v", lineNum, key, err)
		}
		if val == "" {
			return fmt.Errorf("line %d: empty scalar value for key %q", lineNum, key)
		}
		p.result[key] = val
	}
	return nil
}

// startsBlockSequence reports whether the next meaningful line after idx is an
// indented list item. Comments and blank lines between the key and its first
// item are skipped, so they do not break the sequence.
func startsBlockSequence(front []string, idx int) bool {
	for j := idx + 1; j < len(front); j++ {
		trimmed := strings.TrimSpace(front[j])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		return isIndented(front[j]) && (trimmed == "-" || strings.HasPrefix(trimmed, "- "))
	}
	return false
}

func isIndented(s string) bool {
	return strings.HasPrefix(s, " ") || strings.HasPrefix(s, "\t")
}

// indentOf counts leading spaces of a raw line (tabs count as one column).
func indentOf(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] != ' ' && s[i] != '\t' {
			return i
		}
	}
	return len(s)
}

// blockItem extracts the value from an indented "- item" line. Anything else
// indented inside a block sequence is a hard error rather than a silent skip.
func blockItem(trimmed string, lineNum int) (string, error) {
	if trimmed == "-" {
		return "", fmt.Errorf("line %d: empty list item", lineNum)
	}
	if !strings.HasPrefix(trimmed, "- ") {
		return "", fmt.Errorf("line %d: expected list item %q inside block sequence, got %q", lineNum, "- value", trimmed)
	}
	item, err := unquote(strings.TrimSpace(trimmed[2:]))
	if err != nil {
		return "", fmt.Errorf("line %d: %v", lineNum, err)
	}
	if item == "" {
		return "", fmt.Errorf("line %d: empty list item", lineNum)
	}
	return item, nil
}

// unquote removes surrounding single or double quotes from s. A value whose
// first byte is a quote delimiter but which has no matching closing delimiter
// is malformed - the stray leading quote would otherwise be silently kept in
// the value - so it returns an error naming the delimiter kind, never the
// value itself.
func unquote(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	switch s[0] {
	case '"':
		if len(s) >= 2 && s[len(s)-1] == '"' {
			return s[1 : len(s)-1], nil
		}
		return "", fmt.Errorf("unbalanced double-quoted value (missing closing quote)")
	case '\'':
		if len(s) >= 2 && s[len(s)-1] == '\'' {
			return s[1 : len(s)-1], nil
		}
		return "", fmt.Errorf("unbalanced single-quoted value (missing closing quote)")
	}
	return s, nil
}

// splitFlowSequence splits a comma-separated flow sequence inner string
// with awareness of quoted values, so commas inside quotes are preserved.
// A scan that ends inside an open quote is a hard error rather than a
// silently kept stray delimiter. An item that unquotes to "" is rejected
// exactly like blockItem rejects an empty block item: a trailing comma
// ([a, ]), a bare comma ([,]), a doubled comma ([a,,b]), or a quoted-empty
// item ([""]) is malformed, not a silently dropped entry. The genuinely
// empty inner string (from "[]") stays valid and yields nil.
func splitFlowSequence(inner string) ([]string, error) {
	inner = strings.TrimSpace(inner)
	if inner == "" {
		return nil, nil
	}
	var items []string
	var current strings.Builder
	inSingle, inDouble := false, false
	for i := 0; i < len(inner); i++ {
		ch := inner[i]
		switch {
		case ch == '"' && !inSingle:
			inDouble = !inDouble
			current.WriteByte(ch)
		case ch == '\'' && !inDouble:
			inSingle = !inSingle
			current.WriteByte(ch)
		case ch == ',' && !inSingle && !inDouble:
			item, err := unquote(strings.TrimSpace(current.String()))
			if err != nil {
				return nil, err
			}
			if item == "" {
				return nil, fmt.Errorf("empty list item")
			}
			items = append(items, item)
			current.Reset()
		default:
			current.WriteByte(ch)
		}
	}
	if inSingle || inDouble {
		return nil, fmt.Errorf("unbalanced quote in flow sequence")
	}
	if current.Len() > 0 || len(items) > 0 {
		item, err := unquote(strings.TrimSpace(current.String()))
		if err != nil {
			return nil, err
		}
		if item == "" {
			return nil, fmt.Errorf("empty list item")
		}
		items = append(items, item)
	}
	return items, nil
}
