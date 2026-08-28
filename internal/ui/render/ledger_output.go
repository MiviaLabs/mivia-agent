package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

type ledgerEnvelope struct {
	Status        string `json:"status"`
	Ref           string `json:"ref"`
	Kind          string `json:"kind"`
	Bytes         int64  `json:"bytes"`
	Offset        int    `json:"offset"`
	Limit         int    `json:"limit"`
	ReturnedBytes int64  `json:"returned_bytes"`
	NextOffset    *int   `json:"next_offset"`
	HasMore       bool   `json:"has_more"`
	Content       string `json:"content"`
}

// ledgerErrorEnvelope matches the raw {"error":"...","detail":"..."} shapes
// ledger_read returns for a malformed or missing ref - those don't share
// ledgerEnvelope's shape (no "ref"/"status"/"content" fields), so they need
// their own recognizer ahead of the generic raw-dump fallback.
type ledgerErrorEnvelope struct {
	Error  string `json:"error"`
	Detail string `json:"detail"`
}

// maxLedgerContentLines caps how many content lines FormatLedgerOutput shows
// before collapsing the middle into an omitted-lines notice, matching the
// truncate-with-notice idiom FormatCommandOutput already uses for long
// output.
const maxLedgerContentLines = 40

var (
	ledgerSalvageStatus     = regexp.MustCompile(`"status"\s*:\s*"([^"]*)"`)
	ledgerSalvageRef        = regexp.MustCompile(`"ref"\s*:\s*"([^"]*)"`)
	ledgerSalvageKind       = regexp.MustCompile(`"kind"\s*:\s*"([^"]*)"`)
	ledgerSalvageBytes      = regexp.MustCompile(`"bytes"\s*:\s*(\d+)`)
	ledgerSalvageHasMore    = regexp.MustCompile(`"has_more"\s*:\s*true`)
	ledgerSalvageNextOffset = regexp.MustCompile(`"next_offset"\s*:\s*(\d+)`)
	ledgerSalvageContent    = regexp.MustCompile(`"content"\s*:\s*"`)
)

// parseLedgerEnvelope decodes a ledger_read result. A byte-level result cap
// upstream (capToolResult / remainder.CapWithSpoolRef) can slice a large
// envelope mid-JSON, leaving syntactically invalid trailing bytes; ok=false
// only when even a best-effort salvage finds no recognizable ref, so a
// truncated payload still renders structured framing instead of falling
// all the way back to a raw dump.
func parseLedgerEnvelope(trimmed string) (env ledgerEnvelope, salvaged bool, ok bool) {
	if json.Unmarshal([]byte(trimmed), &env) == nil && env.Ref != "" {
		return env, false, true
	}

	refMatch := ledgerSalvageRef.FindStringSubmatch(trimmed)
	if refMatch == nil || refMatch[1] == "" {
		return ledgerEnvelope{}, false, false
	}
	env = ledgerEnvelope{Ref: refMatch[1]}
	if m := ledgerSalvageStatus.FindStringSubmatch(trimmed); m != nil {
		env.Status = m[1]
	}
	if m := ledgerSalvageKind.FindStringSubmatch(trimmed); m != nil {
		env.Kind = m[1]
	}
	if m := ledgerSalvageBytes.FindStringSubmatch(trimmed); m != nil {
		if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			env.Bytes = n
		}
	}
	env.HasMore = ledgerSalvageHasMore.MatchString(trimmed)
	if m := ledgerSalvageNextOffset.FindStringSubmatch(trimmed); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			env.NextOffset = &n
		}
	}
	if loc := ledgerSalvageContent.FindStringIndex(trimmed); loc != nil {
		env.Content, _ = decodeJSONStringPrefix(trimmed[loc[1]:])
	}
	return env, true, true
}

// decodeJSONStringPrefix decodes a JSON string body starting just after its
// opening quote, stopping at the first unescaped closing quote. closed is
// false when the input ends without one - i.e. the string itself was cut
// off mid-escape by an upstream byte truncation - in which case the decoded
// prefix up to that point is still returned.
func decodeJSONStringPrefix(s string) (decoded string, closed bool) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' {
			return b.String(), true
		}
		if c != '\\' || i+1 >= len(s) {
			b.WriteByte(c)
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '"', '\\', '/':
			b.WriteByte(s[i])
		case 'u':
			if i+4 < len(s) {
				if r, err := strconv.ParseUint(s[i+1:i+5], 16, 32); err == nil {
					b.WriteRune(rune(r))
					i += 4
					continue
				}
			}
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String(), false
}

// unwrapLedgerContent recursively unwraps ledger_read content shaped as
// {"output": "..."} (a recorded subagent/tool result nested inside the
// envelope) up to a small fixed depth, then reports whether the final value
// is itself structured JSON (object/array) rather than prose, so the caller
// can route it through the JSON pretty-printer instead of showing an
// escaped string.
func unwrapLedgerContent(content string) (final string, structured bool) {
	cur := strings.TrimSpace(content)
	for range [5]struct{}{} {
		var obj map[string]any
		if err := json.Unmarshal([]byte(cur), &obj); err != nil {
			break
		}
		outVal, ok := obj["output"].(string)
		if !ok || outVal == "" {
			return cur, true
		}
		cur = strings.TrimSpace(outVal)
	}
	var probe any
	if err := json.Unmarshal([]byte(cur), &probe); err == nil {
		switch probe.(type) {
		case map[string]any, []any:
			return cur, true
		}
	}
	return cur, false
}

// collapseLedgerLines caps very long content to a head/tail window with an
// omitted-lines notice, mirroring FormatCommandOutput's collapse idiom.
func collapseLedgerLines(t theme.Theme, tier theme.Tier, lines []string) []string {
	if len(lines) <= maxLedgerContentLines {
		return lines
	}
	subtle := Role(t, tier, theme.RoleFGSubtle)
	head := maxLedgerContentLines - 5
	hidden := len(lines) - head - 5
	out := make([]string, 0, head+6)
	out = append(out, lines[:head]...)
	out = append(out, subtle.Render(fmt.Sprintf("... %d lines omitted ...", hidden)))
	out = append(out, lines[len(lines)-5:]...)
	return out
}

// FormatLedgerOutput formats ledger/output responses into clean content
// blocks without envelope metadata, differentiating not-found refs,
// recorded errors/messages, and malformed-ref shapes instead of treating
// every response identically. The summary doubles as the tool-end
// header detail, so it carries the facts a reader needs without
// expanding: ref, size, and the truncation/paging state
// (tool-output-polish.md R3/R4).
func FormatLedgerOutput(t theme.Theme, tier theme.Tier, output string, width int) (string, []string) {
	trimmed := UnwrapJSONString(strings.TrimSpace(output))

	var errEnv ledgerErrorEnvelope
	if json.Unmarshal([]byte(trimmed), &errEnv) == nil && errEnv.Error != "" {
		summary := "✖ " + errEnv.Error
		if errEnv.Detail == "" {
			return summary, nil
		}
		return summary, []string{Role(t, tier, theme.RoleFGSubtle).Render(errEnv.Detail)}
	}

	env, salvaged, ok := parseLedgerEnvelope(trimmed)
	if !ok {
		return "", rawToolFallback(t, tier, output)
	}

	subtle := Role(t, tier, theme.RoleFGSubtle)

	if env.Status == "not_found" {
		return "✖ ref not found", []string{subtle.Render(shortenRef(env.Ref))}
	}

	summary := shortenRef(env.Ref) + " · " + humanBytes(env.Bytes)
	switch env.Kind {
	case "error":
		summary = "✖ recorded error · " + summary
	case "message":
		summary = "✉ " + summary
	}
	if salvaged {
		summary += " · truncated"
	} else if env.HasMore && env.NextOffset != nil {
		summary += fmt.Sprintf(" · more · offset=%d", *env.NextOffset)
	}

	content, structured := unwrapLedgerContent(env.Content)
	content, thinkWords := stripThink(content)

	var lines []string
	if structured {
		var pretty bytes.Buffer
		if json.Indent(&pretty, []byte(content), "", "  ") == nil {
			lines = HighlightCode(t, tier, "json", pretty.String())
		}
	}
	if lines == nil {
		lines = strings.Split(strings.TrimRight(content, "\n"), "\n")
	}
	// A raw reasoning dump never reaches the transcript: the badge leads
	// the body so the fact survives even a collapsed block's head window
	// (tool-output-polish.md R2).
	if thinkWords > 0 {
		lines = append([]string{subtle.Render(ThinkBadge(thinkWords))}, lines...)
	}
	lines = collapseLedgerLines(t, tier, lines)

	if salvaged {
		lines = append(lines, subtle.Render("⚠ result was truncated before it could be parsed as complete JSON — showing partial content"))
	} else if env.HasMore && env.NextOffset != nil {
		lines = append(lines, subtle.Render(fmt.Sprintf("… more remains — call again with offset=%d to continue", *env.NextOffset)))
	}
	return summary, lines
}
