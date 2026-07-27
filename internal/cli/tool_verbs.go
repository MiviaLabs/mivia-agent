package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// toolStatusMaxRunes caps visible status text (composer + system line).
const toolStatusMaxRunes = 80

// minInterimRunes is the minimum trimmed length for an interim assistant bubble.
// Shorter strings are ghost noise ("OK.", "…") and must not become speech.
const minInterimRunes = 8

// toolVerb returns a short progressive verb for a tool name.
func toolVerb(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "read_file":
		return "Reading"
	case "write_file":
		return "Writing"
	case "search_replace":
		return "Editing"
	case "grep", "search_local":
		return "Searching"
	case "search":
		return "Searching the web"
	case "glob":
		return "Finding files"
	case "list_dir":
		return "Listing"
	case "run_command":
		return "Running"
	case "delegate":
		return "Delegating"
	case "dispatch_tasks":
		return "Dispatching tasks"
	case "parallel":
		return "Running tools in parallel"
	case "prune":
		return "Pruning context"
	default:
		if name == "" {
			return "Working"
		}
		return "Using " + name
	}
}

// toolStatusLine returns a short human status for a tool start.
// Example: "Reading internal/foo.go…", "Searching for auth…".
// Never invents assistant speech; never leaks secrets from detail.
func toolStatusLine(name, detail string) string {
	name = strings.TrimSpace(name)
	detail = redactPreview(detail)
	if isBannerTool(name) {
		return capRunes(toolVerb(name)+"…", toolStatusMaxRunes)
	}
	verb := toolVerb(name)
	obj := toolObjectFromDetail(name, detail)
	if obj == "" {
		return capRunes(verb+"…", toolStatusMaxRunes)
	}
	return capRunes(verb+" "+obj+"…", toolStatusMaxRunes)
}

// realToolStarts filters a tool-event batch to non-banner Start events.
func realToolStarts(starts []bridgeToolEvt) []bridgeToolEvt {
	var real []bridgeToolEvt
	for _, e := range starts {
		if !e.Start || isBannerTool(e.Name) {
			continue
		}
		// Lifecycle-only restarts (queued→running) without args: still count name.
		real = append(real, e)
	}
	return real
}

// toolBatchStatusLine summarizes a wave of tool starts (one line, not N).
func toolBatchStatusLine(starts []bridgeToolEvt) string {
	real := realToolStarts(starts)
	if len(real) == 0 {
		// Only banners (parallel/prune).
		for _, e := range starts {
			if e.Start && isBannerTool(e.Name) {
				return toolStatusLine(e.Name, e.Detail)
			}
		}
		return ""
	}
	if len(real) == 1 {
		return toolStatusLine(real[0].Name, real[0].Detail)
	}
	return capRunes(fmt.Sprintf("Running %d tools…", len(real)), toolStatusMaxRunes)
}

// toolBatchStatusDetail is the expandable body for a multi-tool wave:
// first line is the one-line summary; following lines list each tool verb.
// Single-tool waves return the same string as toolBatchStatusLine (no extra rows).
func toolBatchStatusDetail(starts []bridgeToolEvt) string {
	real := realToolStarts(starts)
	if len(real) == 0 {
		return toolBatchStatusLine(starts)
	}
	if len(real) == 1 {
		return toolStatusLine(real[0].Name, real[0].Detail)
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Running %d tools…", len(real)))
	for _, e := range real {
		line := toolStatusLine(e.Name, e.Detail)
		if line == "" {
			line = e.Name
		}
		b.WriteByte('\n')
		b.WriteString("· " + line)
	}
	return b.String()
}

func isBannerTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "parallel", "prune":
		return true
	default:
		return false
	}
}

// toolObjectFromDetail extracts a short object phrase from tool args JSON/text.
func toolObjectFromDetail(name, detail string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" || isLifecycleStatus(detail) {
		return ""
	}
	// Prefer known path/pattern fields via existing parsers.
	if p := parseToolPath(detail, ""); p != "" {
		return shortenPath(p, 40)
	}
	if s := jsonStringField(detail, "pattern"); s != "" {
		return "for " + capRunes(s, 36)
	}
	if s := jsonStringField(detail, "query"); s != "" {
		return "for " + capRunes(s, 36)
	}
	if s := jsonStringField(detail, "glob"); s != "" {
		return capRunes(s, 36)
	}
	if name == "run_command" {
		if s := jsonStringField(detail, "argv"); s != "" {
			return capRunes(s, 40)
		}
		// argv may be array — first element.
		if s := jsonFirstArrayString(detail, "argv"); s != "" {
			return capRunes(s, 40)
		}
	}
	if name == "delegate" || name == "dispatch_tasks" {
		if s := jsonStringField(detail, "task"); s != "" {
			return capRunes(s, 40)
		}
	}
	// Last resort: first non-secret token of detail (not full dump).
	if strings.HasPrefix(detail, "{") {
		return ""
	}
	line := detail
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	return capRunes(strings.TrimSpace(line), 40)
}

func jsonStringField(raw, key string) string {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "{") {
		// Cheap scan for "key":"value"
		needle := `"` + key + `"`
		i := strings.Index(raw, needle)
		if i < 0 {
			return ""
		}
		rest := strings.TrimSpace(raw[i+len(needle):])
		if !strings.HasPrefix(rest, ":") {
			return ""
		}
		rest = strings.TrimSpace(rest[1:])
		if len(rest) == 0 || rest[0] != '"' {
			return ""
		}
		rest = rest[1:]
		var b strings.Builder
		for j := 0; j < len(rest); j++ {
			c := rest[j]
			if c == '\\' && j+1 < len(rest) {
				b.WriteByte(rest[j+1])
				j++
				continue
			}
			if c == '"' {
				return b.String()
			}
			b.WriteByte(c)
		}
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return ""
	}
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func jsonFirstArrayString(raw, key string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return ""
	}
	v, ok := m[key]
	if !ok {
		return ""
	}
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return ""
	}
	parts := make([]string, 0, len(arr))
	for _, el := range arr {
		if s, ok := el.(string); ok && s != "" {
			parts = append(parts, s)
		}
		if len(parts) >= 3 {
			break
		}
	}
	return strings.Join(parts, " ")
}

func shortenPath(p string, max int) string {
	p = strings.TrimSpace(p)
	if max < 8 {
		max = 8
	}
	if utf8.RuneCountInString(p) <= max {
		return p
	}
	runes := []rune(p)
	return "…" + string(runes[len(runes)-(max-1):])
}

func capRunes(s string, max int) string {
	s = strings.TrimSpace(s)
	if max < 1 {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	if max <= 1 {
		return string(runes[:max])
	}
	return string(runes[:max-1]) + "…"
}

// shouldCommitInterim reports whether text is real assistant speech worth a bubble.
// Rejects empty, whitespace, pure punctuation, lifecycle tokens, and very short ghosts.
func shouldCommitInterim(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if isLifecycleStatus(s) {
		return false
	}
	if utf8.RuneCountInString(s) < minInterimRunes {
		return false
	}
	// Pure punctuation / symbols (no letters or digits).
	hasWord := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			hasWord = true
			break
		}
	}
	return hasWord
}

// shouldFollowOutput decides whether the viewport should stick to the bottom.
// follow is the sticky flag; atBottom is viewport.AtBottom(); scrolledUp is an
// explicit user scroll-away gesture in this update.
func shouldFollowOutput(follow bool, atBottom bool, scrolledUp bool) bool {
	if scrolledUp {
		return false
	}
	if atBottom {
		return true
	}
	return follow
}
