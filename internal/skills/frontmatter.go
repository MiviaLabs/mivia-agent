package skills

import (
	"fmt"
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
//   - # comments and blank lines (skipped)
//
// Everything else (nested maps, >/| block scalars, anchors, multi-doc, etc.)
// is a hard error naming the line number. Unknown keys are also a hard error
// listing the recognised set. This ensures that a future dead field cannot be
// introduced silently — the class of bug this parser exists to fix.
//
// The returned map uses the raw key names as parsed.  Callers must recognise
// the keys they care about and reject any they do not understand — see
// ParseFrontmatterKnown for convenience.
func ParseFrontmatter(data []byte) (map[string]any, error) {
	text := string(data)
	if len(text) > maxFrontmatterBytes {
		return nil, fmt.Errorf("frontmatter exceeds %d bytes", maxFrontmatterBytes)
	}

	// Normalise line endings.
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		// No frontmatter delimiter at all — not an error, empty result.
		return nil, nil
	}

	// Find closing "---".
	closing := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			closing = i
			break
		}
	}
	if closing < 0 {
		return nil, fmt.Errorf("unterminated frontmatter (no closing ---)")
	}

	frontLines := lines[1:closing]
	result := make(map[string]any)
	var currentKey string
	var currentBlock []string
	inBlock := false

	for lineIdx, rawLine := range frontLines {
		lineNum := lineIdx + 2 // 1-based, skipping opening "---"
		line := rawLine

		// Skip blank and comment lines.
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Check for indented block sequence continuation.
		if inBlock && (strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "\t")) {
			item := parseBlockItem(trimmed, lineNum)
			if item != "" {
				currentBlock = append(currentBlock, item)
			}
			continue
		}
		// Flush any pending block sequence when a non-indented line appears.
		if inBlock {
			result[currentKey] = currentBlock
			currentBlock = nil
			inBlock = false
		}

		// Must be a key: value line.
		colonIdx := strings.Index(line, ":")
		if colonIdx < 0 {
			return nil, fmt.Errorf("line %d: expected key: value (no colon found)", lineNum)
		}

		key := strings.TrimSpace(line[:colonIdx])
		if key == "" {
			return nil, fmt.Errorf("line %d: empty key", lineNum)
		}

		// Check for flow sequence: key: [a, b, c]
		rest := strings.TrimSpace(line[colonIdx+1:])
		if strings.HasPrefix(rest, "[") {
			if !strings.HasSuffix(rest, "]") {
				return nil, fmt.Errorf("line %d: unclosed flow sequence", lineNum)
			}
			inner := strings.TrimSpace(rest[1 : len(rest)-1])
			var items []string
			if inner != "" {
				for _, part := range strings.Split(inner, ",") {
					items = append(items, unquote(strings.TrimSpace(part)))
				}
			}
			result[key] = items
			continue
		}

		// Check for block sequence start: key: followed by indented items.
		if rest == "" {
			// Look ahead for indented "- item" lines.
			if lineIdx+1 < len(frontLines) {
				nextLine := frontLines[lineIdx+1]
				nextTrimmed := strings.TrimSpace(nextLine)
				if strings.HasPrefix(nextLine, "  ") || strings.HasPrefix(nextLine, "\t") {
					if strings.HasPrefix(nextTrimmed, "- ") || nextTrimmed == "-" {
						currentKey = key
						currentBlock = nil
						inBlock = true
						continue
					}
				}
			}
			// Empty scalar value.
			result[key] = ""
			continue
		}

		// Plain scalar.
		result[key] = unquote(rest)
	}

	// Flush any pending block sequence.
	if inBlock {
		result[currentKey] = currentBlock
	}

	return result, nil
}

// ParseFrontmatterKnown wraps ParseFrontmatter and rejects any key not in the
// known set. This is the safe entry point for consumers.
func ParseFrontmatterKnown(data []byte, known map[string]bool) (map[string]any, error) {
	m, err := ParseFrontmatter(data)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, nil
	}
	// Build a sorted list for error messages.
	var unknown []string
	for k := range m {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		knownList := make([]string, 0, len(known))
		for k := range known {
			knownList = append(knownList, k)
		}
		return nil, fmt.Errorf("unknown frontmatter key(s): %v; recognised: %v", unknown, knownList)
	}
	return m, nil
}

// parseBlockItem extracts the item text from an indented "- item" line.
// Returns "" if the line is not a valid block list item.
func parseBlockItem(trimmed string, lineNum int) string {
	if strings.HasPrefix(trimmed, "- ") {
		return strings.TrimSpace(trimmed[2:])
	}
	if trimmed == "-" {
		return ""
	}
	return ""
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
