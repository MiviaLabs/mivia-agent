package render

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// TestFormatLedgerOutput_DoubleEncodedEnvelope pins the screenshot
// regression (tool-output-polish.md R1): a ledger envelope delivered as
// a JSON STRING wrapping the whole object - the shape one production
// session rendered as a raw escaped blob - still parses structured, with
// the escapes decoded and no literal backslash sequences on screen.
func TestFormatLedgerOutput_DoubleEncodedEnvelope(t *testing.T) {
	th := loadTheme(t)
	sub := map[string]any{"elapsed": "59.229s", "output": "final report body\nsecond line"}
	inner := mustJSON(sub)
	envelope := mustJSON(map[string]any{
		"status": "ok", "ref": "ref:output:4817bc72cafe", "kind": "output",
		"bytes": len(inner), "offset": 0, "limit": 8192, "content": inner,
	})
	summary, lines := FormatLedgerOutput(th, theme.TierTrueColor, envelope, 100)
	if !strings.Contains(ansi.Strip(summary), "4817bc72") {
		t.Errorf("expected the ref in the summary, got %q", summary)
	}
	plain := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "final report body") || !strings.Contains(plain, "second line") {
		t.Errorf("content must render decoded:\n%s", plain)
	}
	if strings.Contains(plain, `\n`) || strings.Contains(plain, `\u003c`) || strings.Contains(plain, `\"`) {
		t.Errorf("literal escape sequences leaked into the rendering:\n%s", plain)
	}
	if !strings.Contains(plain, "unparsed") && strings.Contains(plain, `"output"`) && strings.Contains(plain, `\`) {
		t.Errorf("raw dump without the unparsed label:\n%s", plain)
	}
}

// TestFormatLedgerOutput_HidesThinkDumps pins R2: a recorded subagent
// output that opens with a raw <think> stream renders the badge and
// never the dump itself; a truncated record whose closing tag was cut
// hides the unclosed tail too.
func TestFormatLedgerOutput_HidesThinkDumps(t *testing.T) {
	th := loadTheme(t)
	cases := map[string]string{
		"closed":    "<think>secret reasoning words</think>\nfinal answer",
		"truncated": "<think>secret reasoning words cut mid",
	}
	for name, inner := range cases {
		envelope := fmt.Sprintf(`{"status":"ok","ref":"ref:output:4817bc72cafe","kind":"output","bytes":99,"content":%s}`, mustJSON(inner))
		_, lines := FormatLedgerOutput(th, theme.TierTrueColor, envelope, 100)
		plain := ansi.Strip(strings.Join(lines, "\n"))
		if !strings.Contains(plain, "thinking") || !strings.Contains(plain, "words hidden") {
			t.Errorf("%s: expected the think badge in:\n%s", name, plain)
		}
		if strings.Contains(plain, "secret reasoning") {
			t.Errorf("%s: raw model dump leaked into the transcript:\n%s", name, plain)
		}
		if name == "closed" && !strings.Contains(plain, "final answer") {
			t.Errorf("%s: the content after the think block went missing:\n%s", name, plain)
		}
	}
}

// TestFormatLedgerOutput_UnparsedCarriesALabel pins the ladder's last
// rung: bytes no parse can recover still render, but behind the dim
// unparsed label instead of posing as content (R1).
func TestFormatLedgerOutput_UnparsedCarriesALabel(t *testing.T) {
	th := loadTheme(t)
	summary, lines := FormatLedgerOutput(th, theme.TierTrueColor, "not json at all, just noise", 100)
	if summary != "" {
		t.Errorf("expected empty summary, got %q", summary)
	}
	if len(lines) < 2 || !strings.Contains(ansi.Strip(lines[0]), "unparsed tool result") {
		t.Errorf("expected the unparsed label as body row one, got %v", lines)
	}
}

// TestFormatInspectRepositoryOutput_DoubleEncodedEnvelope pins the same
// ladder for the inspect_repository formatter.
func TestFormatInspectRepositoryOutput_DoubleEncodedEnvelope(t *testing.T) {
	th := loadTheme(t)
	inner := `{"result_count":1,"results":[{"path":"a/b.go","line":7,"text":"hit"}]}`
	envelope := mustJSON(inner)
	summary, lines := FormatInspectRepositoryOutput(th, theme.TierTrueColor, envelope, 100)
	if !strings.Contains(ansi.Strip(summary), "1 matches") {
		t.Errorf("expected the match summary, got %q", summary)
	}
	plain := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "b.go") {
		t.Errorf("expected the grouped match:\n%s", plain)
	}
}

// TestFormatMemoryOutput_WrapperAndUnparsed pins R6's slice of the
// ladder: a {"results": [...]} wrapper renders cards, and structured
// bytes that fail to parse carry the unparsed label instead of a blob.
func TestFormatMemoryOutput_WrapperAndUnparsed(t *testing.T) {
	th := loadTheme(t)
	wrapped := `{"results":[{"id":"abcdef123456","scope":"project","summary":"ship the gate"}]}`
	summary, lines := FormatMemoryOutput(th, theme.TierTrueColor, wrapped, 100)
	if !strings.Contains(ansi.Strip(summary), "1 memory item") {
		t.Errorf("expected one card, got %q", summary)
	}
	plain := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "ship the gate") {
		t.Errorf("expected the card summary:\n%s", plain)
	}

	_, lines = FormatMemoryOutput(th, theme.TierTrueColor, `{"truncated":tru`, 100)
	if len(lines) < 1 || !strings.Contains(ansi.Strip(lines[0]), "unparsed tool result") {
		t.Errorf("expected the unparsed label for structured garbage, got %v", lines)
	}

	// Plain sentences keep passing through untouched.
	summary, _ = FormatMemoryOutput(th, theme.TierTrueColor, "saved memory \"gate\" (project, id abc)", 100)
	if summary != "" {
		t.Errorf("plain sentence should not synthesize a summary, got %q", summary)
	}
}

// TestFormatMemoryOutput_TitleAndVerdict pins R6: a search result's
// title carries the card body with the snippet dimmed under it, and the
// agent's verdict marks the identity row.
func TestFormatMemoryOutput_TitleAndVerdict(t *testing.T) {
	th := loadTheme(t)
	raw := `[{"id":"abcdef123456","scope":"project","verdict":"good","title":"gate ordering fix","summary":"the hook gate must arm before the first tool call","created":"2026-08-27"}]`
	_, lines := FormatMemoryOutput(th, theme.TierTrueColor, raw, 100)
	plain := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "gate ordering fix") || !strings.Contains(plain, "· good") {
		t.Errorf("expected the title row and the verdict marker:\n%s", plain)
	}
	if !strings.Contains(plain, "hook gate must arm") {
		t.Errorf("expected the dim snippet row under the title:\n%s", plain)
	}
}
