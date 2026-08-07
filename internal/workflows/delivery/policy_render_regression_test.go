package delivery

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestRenderLargeBindingRegression pins that a single binding larger than
// template.Render's DefaultMaxRenderedBytes cap (32768) still renders and is
// truncated by the delivery layer instead of returning an error.
func TestRenderLargeBindingRegression(t *testing.T) {
	t.Run("commit message truncates to MaxCommitMessageBytes", func(t *testing.T) {
		const cap = 512
		big := strings.Repeat("a", 40000)
		p := Policy{CommitMessageTemplate: "{{ inputs.task }}", MaxCommitMessageBytes: cap}
		got, err := p.RenderCommitMessage(map[string]string{"task": big})
		if err != nil {
			t.Fatalf("RenderCommitMessage: unexpected error: %v", err)
		}
		if len(got) != cap {
			t.Errorf("truncated commit message = %d bytes, want %d", len(got), cap)
		}
		if !strings.HasSuffix(got, "...") {
			t.Errorf("truncated commit message should end with ..., got tail %q", got[len(got)-5:])
		}
	})

	t.Run("title truncates within MaxTitleBytes and MaxTitleRunes", func(t *testing.T) {
		const cap = 512
		big := strings.Repeat("a", 40000)
		p := Policy{TitleTemplate: "{{ inputs.task }}", MaxTitleBytes: cap}
		got, err := p.RenderTitle(map[string]string{"task": big})
		if err != nil {
			t.Fatalf("RenderTitle: unexpected error: %v", err)
		}
		if len(got) > cap {
			t.Errorf("title %d bytes exceeds MaxTitleBytes %d", len(got), cap)
		}
		if n := utf8.RuneCountInString(got); n > MaxTitleRunes {
			t.Errorf("title %d runes exceeds MaxTitleRunes %d", n, MaxTitleRunes)
		}
	})
}
