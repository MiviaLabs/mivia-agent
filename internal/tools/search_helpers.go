package tools

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// stripHTMLTags removes HTML/XML tags and decodes common entities.
// Block-level closing tags (</p>, </li>, </div>, etc.) and self-closing
// tags (<br>, <hr>) produce newlines in the output.
func stripHTMLTags(s string) string {
	var out strings.Builder
	inTag := false
	inEntity := false
	var entity strings.Builder
	var tagBuf strings.Builder

	for i, r := range s {
		if inTag {
			tagBuf.WriteRune(r)
			if r == '>' {
				inTag = false
				tagContent := strings.TrimSpace(tagBuf.String())
				tagBuf.Reset()
				// Remove trailing '>' left by the WriteRune above
				tagContent = strings.TrimSuffix(tagContent, ">")
				tagContent = strings.TrimSpace(tagContent)
				processTag(&out, tagContent)
			}
			continue
		}
		if r == '<' {
			// Only treat as tag if followed by a valid HTML tag-start character.
			if i+1 < len(s) {
				next := s[i+1]
				// HTML tags are lowercase; uppercase is typically template syntax (<T>, <U>).
				if (next >= 'a' && next <= 'z') || next == '/' || next == '!' || next == '?' {
					inTag = true
					tagBuf.Reset()
					continue
				}
			}
			// Literal '<' - not a tag.
			out.WriteRune(r)
			continue
		}
		if r == '&' {
			// Only enter entity mode if followed by a letter (valid entity name).
			if i+1 < len(s) {
				next := s[i+1]
				if !((next >= 'a' && next <= 'z') || (next >= 'A' && next <= 'Z') || next == '#') {
					out.WriteRune(r)
					continue
				}
			}
			inEntity = true
			entity.Reset()
			continue
		}
		if inEntity {
			if r == ';' {
				inEntity = false
				writeEntity(&out, entity.String())
				continue
			}
			entity.WriteRune(r)
			continue
		}
		// Normalize whitespace: collapse runs, but preserve newlines from block tags.
		if isWhitespace(r) {
			if out.Len() > 0 {
				last := out.String()[out.Len()-1]
				if last != ' ' && last != '\n' {
					out.WriteRune(' ')
				}
			}
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

func writeEntity(out *strings.Builder, code string) {
	switch code {
	case "amp":
		out.WriteRune('&')
	case "lt":
		out.WriteRune('<')
	case "gt":
		out.WriteRune('>')
	case "quot":
		out.WriteRune('"')
	case "nbsp":
		out.WriteRune(' ')
	default:
		if strings.HasPrefix(code, "#") {
			var num int
			fmt.Sscanf(code[1:], "%d", &num)
			if num > 0 && num < 0x10FFFF {
				out.WriteRune(rune(num))
			}
		} else {
			out.WriteRune('&')
			out.WriteString(code)
			out.WriteRune(';')
		}
	}
}

// processTag handles a single parsed HTML tag (without angle brackets) and may
// write structural newlines into out for block-level and self-closing tags.
func processTag(out *strings.Builder, raw string) {
	// Extract the tag name (before any space, slash, or newline).
	tagName := raw
	if idx := strings.IndexAny(tagName, " \t\n\r/"); idx > 0 {
		tagName = tagName[:idx]
	}
	tagName = strings.ToLower(tagName)

	// Self-closing structural tags: <br>, <br/>, <hr>, <hr/>.
	if tagName == "br" || tagName == "hr" {
		out.WriteRune('\n')
		return
	}

	// Closing block tags: </p>, </li>, </div>, etc.
	if len(raw) > 0 && raw[0] == '/' {
		closeName := strings.TrimSpace(raw[1:])
		if idx := strings.IndexAny(closeName, " \t\n\r"); idx > 0 {
			closeName = closeName[:idx]
		}
		closeName = strings.ToLower(closeName)
		if isBlockTagName(closeName) {
			out.WriteRune('\n')
		}
	}
}

// isBlockTagName returns true for HTML block-level tags where a newline
// should follow the closing tag.
func isBlockTagName(name string) bool {
	switch name {
	case "p", "li", "div", "h1", "h2", "h3", "h4", "h5", "h6",
		"blockquote", "pre", "table", "tr", "ol", "ul":
		return true
	}
	return false
}

func decodeHTMLEntities(s string) string {
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", `"`)
	s = strings.ReplaceAll(s, "&#x27;", "'")
	s = strings.ReplaceAll(s, "&#39;", "'")
	return s
}

func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	// Truncate at byte boundary for valid UTF-8.
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}

func isTextContentType(ct string) bool {
	if ct == "" {
		return false
	}
	ct = strings.ToLower(ct)
	return strings.HasPrefix(ct, "text/") ||
		strings.HasPrefix(ct, "application/json") ||
		strings.HasPrefix(ct, "application/xml") ||
		strings.HasPrefix(ct, "application/javascript") ||
		strings.HasPrefix(ct, "application/xhtml") ||
		strings.Contains(ct, "charset=utf") ||
		strings.Contains(ct, "charset=iso")
}

func isWhitespace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\f'
}
