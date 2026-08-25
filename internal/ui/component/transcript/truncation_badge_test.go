package transcript

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// TestToolEndRendersDegradeNoticeAsBadge pins the human-facing rewrite of a
// batch/turn-budget degrade (internal/remainder.TruncationNotice): the raw
// "kept N of M bytes (remainder: ref, use read_output)" trailer must not
// reach the transcript verbatim - it reads as a failure to a human even
// though the model just calls read_output and moves on. The badge replaces
// it with a calm one-liner instead.
func TestToolEndRendersDegradeNoticeAsBadge(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 200)

	result := "internal/config/provider.go:15:type ProviderMeta struct {" +
		remainder.TruncationNotice(0, 955, "ref:output:abc123")
	m = drain(t, m, []uievent.Event{
		{Kind: uievent.KindToolEnd, Body: uievent.ToolEndBody{ToolCallID: "c1", Name: "grep", OK: true, Result: result}},
	})

	got := ansi.Strip(m.Dump())
	if strings.Contains(got, "kept 0 of 955 bytes") {
		t.Errorf("raw TruncationNotice trailer reached the transcript verbatim:\n%s", got)
	}
	if strings.Contains(got, "use read_output") {
		t.Errorf("raw model-facing read_output instruction leaked into the transcript:\n%s", got)
	}
	if !strings.Contains(got, "955 B stored") {
		t.Errorf("expected the compact stored badge, got:\n%s", got)
	}
	if !strings.Contains(got, "read_output") {
		t.Errorf("expected the badge to still name read_output, got:\n%s", got)
	}
	// The content preceding the notice must still render.
	if !strings.Contains(got, "type ProviderMeta struct") {
		t.Errorf("expected the pre-notice content to still render, got:\n%s", got)
	}
}

// TestToolEndPartialDegradeBadgeShowsKeptCount covers the partial-kept case
// (some content survived pass 1's own recut before the batch budget forced a
// degrade), distinct from the "kept 0" full-degrade case above.
func TestToolEndPartialDegradeBadgeShowsKeptCount(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 200)

	result := "line one\nline two" + remainder.TruncationNotice(17, 4000, "ref:output:def456")
	m = drain(t, m, []uievent.Event{
		{Kind: uievent.KindToolEnd, Body: uievent.ToolEndBody{ToolCallID: "c1", Name: "read_file", OK: true, Result: result}},
	})

	got := ansi.Strip(m.Dump())
	if !strings.Contains(got, "showing 17 of 4000 B") {
		t.Errorf("expected the partial-degrade badge, got:\n%s", got)
	}
}

// TestTruncationBadgeNoRef covers the store-failed / no-spool case, where
// ParseTruncationNotice recovers no ref and the badge falls back to a plain
// kept/total line instead of pointing at read_output.
func TestTruncationBadgeNoRef(t *testing.T) {
	got := truncationBadge(0, 955, "")
	want := "· truncated: kept 0 of 955 B"
	if got != want {
		t.Errorf("truncationBadge(no ref) = %q, want %q", got, want)
	}
}
