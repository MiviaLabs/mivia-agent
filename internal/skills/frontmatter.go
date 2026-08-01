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
//   - # comments and blank lines, skipped anywhere including inside a sequence
//
// Everything else (nested maps, >/| block scalars, anchors, multi-doc, etc.)
// is a hard error naming the line number. Rejecting beats guessing: a silently
// dropped key is the class of bug this parser exists to prevent.
//
// Unknown keys are NOT rejected here - the returned map uses raw key names.
// Callers must reject keys they do not understand; use ParseFrontmatterKnown,
// which is the safe entry point.
func ParseFrontmatter(data []byte) (map[string]any, error) {
	front, ok, err := frontmatterLines(data)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return parseFrontLines(front)
}

// ParseFrontmatterKnown wraps ParseFrontmatter and rejects any key not in the
// known set, so a field that nothing consumes cannot be introduced silently.
func ParseFrontmatterKnown(data []byte, known map[string]bool) (map[string]any, error) {
	m, err := ParseFrontmatter(data)
	if err != nil || m == nil {
		return nil, err
	}
	var unknown []string
	for k := range m {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) == 0 {
		return m, nil
	}
	sort.Strings(unknown)
	knownList := make([]string, 0, len(known))
	for k := range known {
		knownList = append(knownList, k)
	}
	sort.Strings(knownList)
	return nil, fmt.Errorf("unknown frontmatter key(s) %v; recognised: %v", unknown, knownList)
}

// ParseFrontmatterKnownWithClosing is like ParseFrontmatterKnown but also
// returns the line index of the closing "---" delimiter. When no frontmatter
// is present, closing is -1.
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
	var unknown []string
	for k := range m {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) == 0 {
		return m, closingLine, nil
	}
	sort.Strings(unknown)
	knownList := make([]string, 0, len(known))
	for k := range known {
		knownList = append(knownList, k)
	}
	sort.Strings(knownList)
	return nil, -1, fmt.Errorf("unknown frontmatter key(s) %v; recognised: %v", unknown, knownList)
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

// frontmatterLines returns the lines between the opening and closing "---".
// ok is false when the document has no frontmatter block at all, which is not
// an error.
func frontmatterLines(data []byte) (front []string, ok bool, err error) {
	if len(data) > maxFrontmatterBytes {
		return nil, false, fmt.Errorf("frontmatter exceeds %d bytes", maxFrontmatterBytes)
	}
	lines := strings.Split(normalizeNewlines(string(data)), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil, false, nil
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return lines[1:i], true, nil
		}
	}
	return nil, false, fmt.Errorf("unterminated frontmatter (no closing ---)")
}

// fmParser holds block-sequence accumulation state across lines.
type fmParser struct {
	result  map[string]any
	key     string
	block   []string
	inBlock bool
}

// flush commits a pending block sequence to the result map.
func (p *fmParser) flush() {
	if p.inBlock {
		p.result[p.key] = p.block
		p.block = nil
		p.inBlock = false
	}
}

func parseFrontLines(front []string) (map[string]any, error) {
	p := &fmParser{result: make(map[string]any)}
	for i, raw := range front {
		lineNum := i + 2 // 1-based, accounting for the opening "---"
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if isIndented(raw) {
			if !p.inBlock {
				return nil, fmt.Errorf("line %d: unexpected indented line (nested maps are not supported)", lineNum)
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
	rest := strings.TrimSpace(trimmed[colon+1:])
	switch {
	case strings.HasPrefix(rest, "["):
		if !strings.HasSuffix(rest, "]") {
			return fmt.Errorf("line %d: unclosed flow sequence", lineNum)
		}
		p.result[key] = splitFlowSequence(strings.TrimSpace(rest[1 : len(rest)-1]))
	case rest == "":
		if startsBlockSequence(front, idx) {
			p.key, p.block, p.inBlock = key, nil, true
		} else {
			p.result[key] = ""
		}
	default:
		p.result[key] = unquote(rest)
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

// blockItem extracts the value from an indented "- item" line. Anything else
// indented inside a block sequence is a hard error rather than a silent skip.
func blockItem(trimmed string, lineNum int) (string, error) {
	if trimmed == "-" {
		return "", fmt.Errorf("line %d: empty list item", lineNum)
	}
	if !strings.HasPrefix(trimmed, "- ") {
		return "", fmt.Errorf("line %d: expected list item %q inside block sequence, got %q", lineNum, "- value", trimmed)
	}
	item := unquote(strings.TrimSpace(trimmed[2:]))
	if item == "" {
		return "", fmt.Errorf("line %d: empty list item", lineNum)
	}
	return item, nil
}

// unquote removes surrounding single or double quotes from s.
func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// splitFlowSequence splits a comma-separated flow sequence inner string
// with awareness of quoted values, so commas inside quotes are preserved.
func splitFlowSequence(inner string) []string {
	inner = strings.TrimSpace(inner)
	if inner == "" {
		return nil
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
			items = append(items, unquote(strings.TrimSpace(current.String())))
			current.Reset()
		default:
			current.WriteByte(ch)
		}
	}
	if current.Len() > 0 || len(items) > 0 {
		items = append(items, unquote(strings.TrimSpace(current.String())))
	}
	return items
}
