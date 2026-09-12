package transcript

import (
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

func TestReasoningBlockEmptyBody(t *testing.T) {
	th := loadTheme(t)
	b := Block{
		Kind:      uievent.KindReasoning,
		Collapsed: false,
		Body:      nil,
	}
	if h := b.Height(80); h != 1 {
		t.Fatalf("expected height 1 for empty reasoning body, got %d", h)
	}
	rendered := b.Render(th, theme.TierTrueColor, 80)
	if !strings.Contains(rendered, "Thought") {
		t.Fatalf("expected Thought in rendered block, got %q", rendered)
	}
}

func TestUsageBlockBodyRows(t *testing.T) {
	b := Block{
		Kind: uievent.KindUsage,
		Body: []string{"usage line 1", "usage line 2"},
	}
	rows := b.bodyRows(80)
	if len(rows) != 2 || rows[0] != "usage line 1" {
		t.Fatalf("unexpected bodyRows for usage block: %v", rows)
	}
}

func TestReasoningFocusedHeader(t *testing.T) {
	th := loadTheme(t)
	b := Block{
		Kind:      uievent.KindReasoning,
		Focused:   true,
		ElapsedMS: 1500,
	}
	rendered := b.renderHeader(th, theme.TierTrueColor, 80)
	if !strings.Contains(rendered, "Thought") {
		t.Fatalf("expected Thought in rendered header, got %q", rendered)
	}
}

func TestHeaderPlainDetailSuffix(t *testing.T) {
	b1 := Block{
		Kind: uievent.KindToolPending,
		Header: Header{
			Label:  "tool",
			Detail: "arg1",
			State:  "pending",
		},
	}
	plain1 := b1.headerPlain()
	if !strings.Contains(plain1, "waiting to run") || !strings.Contains(plain1, "arg1") {
		t.Fatalf("expected detail and suffix, got %q", plain1)
	}

	b2 := Block{
		Kind: uievent.KindToolPending,
		Header: Header{
			Label: "tool",
			State: "pending",
		},
	}
	plain2 := b2.headerPlain()
	if !strings.Contains(plain2, "waiting to run") {
		t.Fatalf("expected suffix without detail, got %q", plain2)
	}
}

func TestCardSpinnerAndPadNegative(t *testing.T) {
	if g := spinnerGlyph(-1); g != "-" {
		t.Fatalf("expected frame 0 glyph, got %q", g)
	}
	if p := padToWidth("hello", 0); p != "hello" {
		t.Fatalf("expected unchanged for w<=0, got %q", p)
	}
	if p := padToWidth("hello", -5); p != "hello" {
		t.Fatalf("expected unchanged for w<=0, got %q", p)
	}
}

func TestChildrenSetEmptyAndNarrowWidth(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierASCII)
	m.SetChildren("", []ChildCall{{Name: "test"}})

	b := Block{
		Kind: uievent.KindToolEnd,
		Children: []ChildCall{
			{Name: "child1", Detail: "ok", OK: true},
			{Name: "child2", Detail: "fail", OK: false},
		},
	}
	// width = 1 will make inner <= 0, so inner becomes width
	rows := b.childRows(th, theme.TierASCII, 1)
	if len(rows) != 2 {
		t.Fatalf("expected 2 child rows, got %d", len(rows))
	}
}

func TestUsageBlockRenderFallbackAndZeroWidth(t *testing.T) {
	th := loadTheme(t)
	bNil := Block{
		Kind: uievent.KindUsage,
		Body: []string{"raw fallback line"},
	}
	outNil := bNil.renderUsage(th, theme.TierTrueColor, 80)
	if outNil != "raw fallback line" {
		t.Fatalf("expected raw fallback, got %q", outNil)
	}

	bUsage := Block{
		Kind:       uievent.KindUsage,
		UsageModel: "gpt-4",
		Usage:      &uievent.UsageBody{InputTokens: 10, OutputTokens: 20, CostUSD: 0.01},
	}
	outZero := bUsage.renderUsage(th, theme.TierASCII, 0)
	if !strings.Contains(outZero, "gpt-4") {
		t.Fatalf("expected usage text, got %q", outZero)
	}
}

func TestReasoningBlockFromZeroStarted(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	blk := m.reasoningBlockFrom("some thought", 2, time.Time{})
	if blk.ElapsedMS != 0 {
		t.Fatalf("expected 0 ElapsedMS when started is zero, got %d", blk.ElapsedMS)
	}
}

func TestTailRowsNilStream(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.pending = "streamed text"
	m.pendingKind = uievent.KindTextDelta
	m.stream = nil
	rows := m.tailRows()
	if len(rows) == 0 {
		t.Fatalf("expected non-empty rows from tailRows with nil stream")
	}
}

func TestUsageEventNegativeElapsed(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.turnStartedAt = time.Now().Add(10 * time.Minute)
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindUsage,
		Body: uievent.UsageBody{ElapsedSeconds: 5},
	})
	if len(m.blocks) == 0 {
		t.Fatalf("expected usage block added")
	}
	blk := m.blocks[len(m.blocks)-1]
	if blk.UsageElapsedMS != 0 {
		t.Fatalf("expected UsageElapsedMS clamped to 0, got %d", blk.UsageElapsedMS)
	}
}

func TestFocusedTextRunningSpinner(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.blocks = []Block{
		{
			Kind:   uievent.KindToolStart,
			Header: Header{Label: "bash", State: "running"},
		},
	}
	m.focus = 0
	m.spinnerFrame = 2
	txt, ok := m.FocusedText()
	if !ok || !strings.Contains(txt, "/") {
		t.Fatalf("expected FocusedText to include frame 2 spinner '/', got %q (ok=%v)", txt, ok)
	}
}
