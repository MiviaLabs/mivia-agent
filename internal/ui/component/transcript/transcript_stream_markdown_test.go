package transcript

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// These tests pin C6: the streaming tail renders through
// render.StreamRenderer (markdown, styled) instead of render.Wrap
// (plain text), so nothing pops in style when the span commits.

// sendDeltas feeds text.delta events for each chunk of text in turn,
// threading the returned Model through HandleEvent the way the real
// event loop does.
func sendDeltas(t *testing.T, m Model, chunks ...string) Model {
	t.Helper()
	for _, c := range chunks {
		next, _ := m.HandleEvent(uievent.Event{Body: uievent.TextDeltaBody{Text: c}})
		m = next
	}
	return m
}

func TestTailRowsStreamsMarkdown(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)
	m.width = 80
	m = sendDeltas(t, m, "# Hea", "ding\n\nSome **bold** text.\n")

	rows := m.tailRows()
	joined := strings.Join(rows, "\n")
	plain := stripANSITranscript(joined)

	if !strings.Contains(plain, "Heading") {
		t.Fatalf("tail rows do not contain the heading text: %q", plain)
	}
	if !strings.Contains(joined, ";1m") {
		// Wrap's plain-text path styles every row with ONE uniform
		// foreground - it never emits bold. A bold SGR only appears if
		// this went through markdown, which makes h1 and **bold** bold.
		t.Errorf("tail rows carry no bold styling; want markdown rendering (headings/bold are bold), got what looks like plain-text Wrap: %q", joined)
	}
}

// TestTailRowsNoPopAtCommit is the defect this phase exists to close:
// the live tail and the committed block must render IDENTICALLY once
// the same text is complete, so nothing visibly changes shape at the
// moment a span ends.
func TestTailRowsNoPopAtCommit(t *testing.T) {
	th := loadTheme(t)
	full := "# Retry policy\n\nThe client retries a failed request with **backoff**.\n\n```go\nfunc f() int { return 1 }\n```\n"

	m := New(th, theme.TierTrueColor)
	m.width = 80
	m = sendDeltas(t, m, full[:10], full[10:25], full[25:])

	tailBeforeCommit := strings.Join(m.tailRows(), "\n")

	m, _ = m.HandleEvent(uievent.Event{Body: uievent.TextEndBody{Text: full}})
	if len(m.blocks) != 1 {
		t.Fatalf("expected exactly one committed block, got %d", len(m.blocks))
	}
	committed := strings.Join(m.blocks[0].Body, "\n")

	oneShot := render.Markdown(th, theme.TierTrueColor, m.proseRenderWidth(), full)
	oneShotLines := strings.Split(strings.TrimSuffix(oneShot, "\n"), "\n")
	wantCommitted := strings.Join(oneShotLines, "\n")

	if committed != wantCommitted {
		t.Errorf("committed block differs from a one-shot render of the same text\n got  %q\n want %q", committed, wantCommitted)
	}
	if tailBeforeCommit != committed {
		t.Errorf("the live tail popped at commit: it rendered differently from the block it turned into\n tail      %q\n committed %q", tailBeforeCommit, committed)
	}
}

// TestTailRowsResetsBetweenSpans guards against a stale cache: once one
// span commits, streaming a SECOND, unrelated span must not carry over
// any cached prefix from the first.
func TestTailRowsResetsBetweenSpans(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)
	m.width = 80

	m = sendDeltas(t, m, "# First span\n\nprose one.\n")
	m, _ = m.HandleEvent(uievent.Event{Body: uievent.TextEndBody{Text: "# First span\n\nprose one.\n"}})

	second := "# Second span\n\ncompletely different text.\n"
	m = sendDeltas(t, m, second[:8], second[8:])

	got := strings.Join(m.tailRows(), "\n")
	want := render.Markdown(th, theme.TierTrueColor, m.proseRenderWidth(), second)
	wantLines := strings.Join(strings.Split(strings.TrimSuffix(want, "\n"), "\n"), "\n")

	if got != wantLines {
		t.Errorf("second span's tail carries stale cache from the first\n got  %q\n want %q", got, wantLines)
	}
	if strings.Contains(got, "First span") {
		t.Errorf("second span's tail still contains the first span's text: %q", got)
	}
}

// stripANSITranscript removes SGR escape sequences for substring checks.
func stripANSITranscript(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		if r == 0x1b {
			in = true
			continue
		}
		if in {
			if r == 'm' {
				in = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// TestClearResetsStreamCache is the regression for a real bug found by
// bug-audit: Clear() inlined the pending-field reset instead of calling
// clearPending(), so it never reset m.stream. /clear is accepted while a
// turn is actively streaming (uiadapter's handleClear does not check
// s.active), and the turn keeps emitting deltas after Clear() runs
// (chat.Session.resetSystem's own doc comment: it invalidates the
// history writeback, not the running turn), so the stale m.stream keeps
// getting fed.
//
// This does NOT corrupt visible output for a reused matching prefix -
// Render is a pure function of its content bytes, so a cached render of
// bytes the new content happens to share is byte-identical to a fresh
// render of the same bytes regardless of which conversation produced
// them. The two REAL consequences are: (1) Clear() breaks its own
// stated invariant ("empties... the in-flight streaming tail") by
// leaving a non-empty cached prefix behind, and (2) if the pre-clear
// span had been poisoned, poisoned stays true forever, silently
// reintroducing the full-document-render-per-tick cost for the rest of
// the post-clear conversation with no visible symptom besides slower
// ticks.
func TestClearResetsStreamCache(t *testing.T) {
	th := loadTheme(t)

	t.Run("actually clears the cached prefix, not just m.pending", func(t *testing.T) {
		m := New(th, theme.TierTrueColor)
		m.width = 80
		// Long enough, with a blank-line block boundary, that
		// StreamRenderer confirms a non-empty stableContent before the
		// span ends.
		first := "# Old heading\n\nOld conversation prose that is long enough to force a confirmed boundary before the next block.\n\nSecond old paragraph.\n"
		m = sendDeltas(t, m, first)
		_ = m.tailRows() // drive the scan so stableContent actually advances
		if m.stream == nil || m.stream.CachedPrefixLen() == 0 {
			t.Fatal("precondition: expected a non-empty cached prefix after streaming a span")
		}

		m = m.Clear()

		// Render's own pure-function guarantee means a reused cache
		// entry can never produce visibly wrong bytes for content that
		// happens to share a prefix - Markdown(x) is Markdown(x)
		// regardless of which "conversation" x's prefix was first
		// rendered for. What Clear() must still guarantee, and what the
		// bug actually broke, is the STATED invariant that every
		// span-ending path resets the cache: a Clear() that leaves
		// CachedPrefixLen non-zero is carrying state forward it
		// promised to drop, the same way `m.blocks` or `m.pending`
		// would be if THEY survived Clear() by accident.
		if m.stream != nil && m.stream.CachedPrefixLen() != 0 {
			t.Errorf("Clear left %d bytes of cached prefix behind; want 0 (a fresh cache, matching every other field Clear resets)", m.stream.CachedPrefixLen())
		}
	})

	t.Run("does not leave a poisoned cache stuck across Clear", func(t *testing.T) {
		m := New(th, theme.TierTrueColor)
		m.width = 80
		// An HTML block poisons the cache (see
		// docs/development/stream-markdown-boundary-rules.md).
		poisoning := "prose\n\n<div>\nhtml\n</div>\n\nmore\n"
		m = sendDeltas(t, m, poisoning)
		_ = m.tailRows() // drive the scan so poisoning actually happens
		if m.stream == nil || !m.stream.Poisoned() {
			t.Fatal("precondition: expected the span to be poisoned")
		}

		m = m.Clear()

		if m.stream != nil && m.stream.Poisoned() {
			t.Error("Clear left the stream cache poisoned; the next conversation inherits the fallback path")
		}
	})
}
