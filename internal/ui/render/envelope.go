package render

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// The recorded-result tools (ledger_read, read_output, inspect_repository,
// memory_*) share one failure shape: a fixed-struct unmarshal that misses,
// and the transcript prints raw escaped JSON. envelope.go holds the shared
// defenses (transcript-polish sibling doc tool-output-polish.md R1/R2):
//
//   - UnwrapJSONString peels JSON-string layers off a payload, so an
//     envelope delivered as a string of JSON still parses.
//   - stripThink removes raw model reasoning dumps and reports how many
//     words they held, so no <think> stream reaches the transcript.
//   - rawToolFallback labels the unparsable remnant instead of presenting
//     it as content.

// maxEnvelopeUnwrapDepth bounds the string-peeling loop: one layer per
// recorded-encode level is generous, and a bound is what stops a hostile
// payload from driving the renderer in a loop.
const maxEnvelopeUnwrapDepth = 3

// UnwrapJSONString returns payload with up to maxEnvelopeUnwrapDepth
// JSON string layers peeled off: a payload that unmarshals to a string
// is replaced by that string until it stops being one. Values that are
// not JSON strings pass through unchanged.
func UnwrapJSONString(payload string) string {
	cur := strings.TrimSpace(payload)
	for range [maxEnvelopeUnwrapDepth]struct{}{} {
		var s string
		if err := json.Unmarshal([]byte(cur), &s); err != nil {
			return cur
		}
		cur = strings.TrimSpace(s)
	}
	return cur
}

var thinkBlock = regexp.MustCompile(`(?s)<think>.*?</think>`)

// stripThink removes raw model reasoning dumps from recorded content:
// every closed <think>…</think> block, and one leading unclosed block
// (the tail-truncation case, where the closing tag was cut). It reports
// the word count it removed so the UI can state what it hid. The model
// keeps receiving the raw bytes; this shapes only the transcript.
func stripThink(content string) (string, int) {
	words := 0
	count := func(s string) int { return len(strings.Fields(s)) }
	content = thinkBlock.ReplaceAllStringFunc(content, func(m string) string {
		inner := strings.TrimSuffix(strings.TrimPrefix(m, "<think>"), "</think>")
		words += count(inner)
		return ""
	})
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(strings.ToLower(trimmed), "<think>") {
		inner := trimmed[len("<think>"):]
		words += count(inner)
		trimmed = ""
	}
	if words == 0 {
		return content, 0
	}
	return trimmed, words
}

// ThinkBadge is the dim line that replaces a removed <think> dump.
func ThinkBadge(words int) string {
	return fmt.Sprintf("· thinking %d words hidden", words)
}

// rawToolFallback is the loud-but-calm end of every decode ladder: the
// bytes the formatter could not parse still render (the model saw them,
// and the reader may need them), but a dim first line says what they
// are, so a blob is never mistaken for formatted content.
func rawToolFallback(t theme.Theme, tier theme.Tier, output string) []string {
	label := Role(t, tier, theme.RoleFGSubtle).
		Render(fmt.Sprintf("unparsed tool result · %s", humanBytes(int64(len(output)))))
	return append([]string{label}, strings.Split(strings.TrimRight(output, "\n"), "\n")...)
}
