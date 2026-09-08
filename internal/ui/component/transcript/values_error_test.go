package transcript

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// TestErrorBlockValueLongFirstLineMovesToBody pins the never-clip-the-cause
// rule: a single-line error longer than the header budget must land in the
// wrapping Body, not in the one-row Header.Detail whose renderer truncates
// to the panel width and replaces the tail - usually the actual cause at
// the end of a wrapped error chain - with the clip marker.
func TestErrorBlockValueLongFirstLineMovesToBody(t *testing.T) {
	long := "failed to resume session \"UOYZM46E7C23TDR4VXSNWG2VA4\": resume context session: context session is live in another process"
	b := errorBlockValue(uievent.ErrorBody{Text: long + "\nsecond line"})

	joined := strings.Join(b.Body, "\n")
	if !strings.Contains(joined, "live in another process") {
		t.Fatalf("the cause is not in the wrapping body; it would be clipped: body=%q header=%q", joined, b.Header.Detail)
	}
	if !strings.Contains(joined, "second line") {
		t.Fatalf("later lines lost: %q", joined)
	}
	if b.Header.Detail != "" && len(b.Header.Detail) > errorHeaderDetailMax {
		t.Fatalf("Header.Detail still carries the over-budget line: %q", b.Header.Detail)
	}

	// A short error keeps the existing single-row shape.
	short := errorBlockValue(uievent.ErrorBody{Text: "boom"})
	if short.Header.Detail != "boom" || len(short.Body) != 0 {
		t.Fatalf("short error changed shape: %+v", short)
	}
}

func TestToolEndFailureKeepsCommandOutput(t *testing.T) {
	b := toolEndBlockValue(loadTheme(t), theme.TierASCII, 80, uievent.ToolEndBody{
		Name:   "run_command",
		OK:     false,
		Err:    "failed",
		Result: "command: go test\ncwd: /workspace\nexit=1\nstderr:\ncompile error",
	}, nil)

	got := strings.Join(b.Body, "\n")
	for _, want := range []string{"command: go test", "exit=1", "compile error"} {
		if !strings.Contains(got, want) {
			t.Errorf("failed command body does not contain %q: %q", want, got)
		}
	}
	if got == "failed" || strings.TrimSpace(got) == "" {
		t.Fatalf("failed command output was replaced by status: %q", got)
	}
}
