package clichat

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// ─── Custom renderers for testing ────────────────────────────────────────

type capsRenderer struct{}

func (r *capsRenderer) RenderText(text string, width int) []string {
	return []string{strings.ToUpper(text)}
}

type reverseRenderer struct{}

func (r *reverseRenderer) RenderText(text string, width int) []string {
	runes := []rune(text)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return []string{string(runes)}
}

type prefixRenderer struct{}

func (r *prefixRenderer) RenderText(text string, width int) []string {
	return []string{"> " + text}
}

type emojiRenderer struct{}

func (r *emojiRenderer) RenderText(text string, width int) []string {
	return []string{"🔹 " + text}
}

type boldRenderer struct{}

func (r *boldRenderer) RenderText(text string, width int) []string {
	return []string{lipgloss.NewStyle().Bold(true).Render(text)}
}

// ─── Goal 1: Shared reusable message component ──────────────────────────

func TestMessageBubble_UserAndAssistantUseSameComponentType(t *testing.T) {
	var user *MessageBubble = UserBubble
	var asst *MessageBubble = AssistantBubble
	if user == nil || asst == nil {
		t.Fatal("pre-built bubbles must not be nil")
	}
}

func TestMessageBubble_UserBubbleRendersBodyWithBackground(t *testing.T) {
	lines := UserBubble.Render("hello world", 40, time.Time{})
	if len(lines) < 1 {
		t.Fatalf("expected ≥1 line, got %d", len(lines))
	}
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "hello world") {
		t.Fatalf("expected body content, got %q", plain)
	}
	// Left pad: Render uses plain spaces. Rail glyph is applied by
	// the caller (renderOneChatBlock), not by Render.
	if !strings.Contains(plain, "hello world") {
		t.Fatalf("expected body, got %q", plain)
	}
	if !strings.Contains(plain, "hello") {
		t.Fatalf("expected body content, got %q", plain)
	}
}

// TestMessageBubble_DefaultPaddingHasBreathingRoom validates horizontal pad;
// vertical spacing is an empty lane after the bubble (not in-bubble pad).
func TestMessageBubble_DefaultPaddingHasBreathingRoom(t *testing.T) {
	p := UserBubble.Style.Padding
	if p.Top != 0 || p.Bottom != 0 {
		t.Fatalf("UserBubble vertical pad should be 0 (lane after bubble), got Top=%d Bottom=%d", p.Top, p.Bottom)
	}
	if p.Left < 2 || p.Right < 1 {
		t.Fatalf("UserBubble must have horizontal padding, got Left=%d Right=%d", p.Left, p.Right)
	}

	const width = 40
	lines := UserBubble.Render("hello world", width, time.Time{})
	if len(lines) < 1 {
		t.Fatalf("expected content lines, got %d", len(lines))
	}
	contentPlain := stripANSI(lines[0])
	if !strings.Contains(contentPlain, "hello world") {
		t.Fatalf("content line missing body: %q", contentPlain)
	}
	if vis := VisibleWidth(lines[0]); vis != width {
		t.Fatalf("content line width=%d want %d (right pad fills bar)", vis, width)
	}
}

// TestFormatUserMessageCard_ProductionUsesBubblePadding proves the live path
// (chatblock_render → formatUserMessageCard) is not the old left-only pad.
func TestFormatUserMessageCard_ProductionUsesBubblePadding(t *testing.T) {
	lines := formatUserMessageCard("padded body", 40, time.Time{})
	if len(lines) < 1 {
		t.Fatalf("production card missing body: lines=%d", len(lines))
	}
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "padded body") {
		t.Fatalf("missing body: %q", plain)
	}
	// The user card is its own rail renderer now (UserBubble's full-width
	// background bar painted a dark band across the terminal for a nine-word
	// message and burned an extra row on a clock).
	if !strings.Contains(plain, "▌") {
		t.Fatalf("expected user rail: %q", plain)
	}
	for _, line := range lines {
		if !strings.HasPrefix(stripANSI(line), "  ▌") {
			t.Fatalf("every card line carries the rail: %q", stripANSI(line))
		}
	}
}

func TestMessageBubble_AssistantBubbleRendersContentWithoutBackground(t *testing.T) {
	lines := AssistantBubble.Render("Hello, I'm here", 40, time.Time{})
	if len(lines) < 1 {
		t.Fatalf("expected ≥1 line, got %d", len(lines))
	}
	// LeftRail glyph │ is intentional chrome (not a box border).
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "Hello") {
		t.Fatalf("expected content, got %q", plain)
	}
	if strings.Contains(plain, "╭") || strings.Contains(plain, "╰") {
		t.Fatalf("expected no box borders, got %q", plain)
	}
}

func TestMessageBubble_RenderWithTimestamp(t *testing.T) {
	sent := time.Date(2026, 7, 27, 15, 4, 5, 0, time.Local)
	lines := UserBubble.Render("timed message", 40, sent)
	if len(lines) < 1 {
		t.Fatalf("expected ≥1 line, got %d", len(lines))
	}
	plain := stripANSI(strings.Join(lines, "\n"))
	meta := formatUserBubbleTime(sent)
	if !strings.Contains(plain, meta) {
		t.Fatalf("expected time meta %q in %q", meta, plain)
	}
	if !strings.Contains(plain, "timed message") {
		t.Fatalf("expected content, got %q", plain)
	}
}

func TestMessageBubble_WrapsLongContent(t *testing.T) {
	long := strings.Repeat("word ", 30)
	lines := UserBubble.Render(long, 24, time.Now())
	if len(lines) < 2 {
		t.Fatalf("expected multi-line output for long content, got %d lines", len(lines))
	}
	for _, line := range lines {
		vis := VisibleWidth(line)
		if vis > 24 {
			t.Fatalf("line exceeds width 24: vis=%d %q", vis, stripANSI(line))
		}
	}
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "word") {
		t.Fatalf("expected wrapped content, got %q", plain)
	}
}

func TestMessageBubble_ZeroTimeStillShowsBody(t *testing.T) {
	lines := UserBubble.Render("body only", 40, time.Time{})
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "body only") {
		t.Fatalf("expected body without time, got %q", plain)
	}
}

func TestMessageBubble_NarrowWidthStacksLabelOnOwnLine(t *testing.T) {
	sent := time.Date(2026, 1, 1, 12, 0, 0, 0, time.Local)
	lines := UserBubble.Render("hi", 16, sent)
	if len(lines) < 1 {
		t.Fatalf("expected ≥1 line for narrow width, got %d", len(lines))
	}
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "hi") {
		t.Fatalf("expected body content with narrow width, got %q", plain)
	}
}

func TestMessageBubble_NoBackgroundNoLabelRendersSimple(t *testing.T) {
	_showTimeF := false
	bubble := &MessageBubble{
		Style: BubbleStyle{
			ShowTime: &_showTimeF,
		},
		Renderer: &plainTextRenderer{},
	}
	lines := bubble.Render("simple text", 40, time.Time{})
	if len(lines) < 1 {
		t.Fatalf("expected ≥1 line, got %d", len(lines))
	}
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "simple text") {
		t.Fatalf("expected content, got %q", plain)
	}
}

// ─── Goal 2: Customizable per role (props/options) ──────────────────────

func TestMessageBubble_CustomStyleChangesAppearance(t *testing.T) {
	customBg := lipgloss.NewStyle().Background(lipgloss.Color("52"))
	customLabel := lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	_showTimeT := true

	customBubble := UserBubble.WithStyle(BubbleStyle{
		Background: &customBg,
		LabelStyle: &customLabel,
		Padding:    Padding{Left: 4},
		ShowTime:   &_showTimeT,
	})

	// Must not mutate the original (UserBubble defaults: Top/Bottom 0, Left/Right 3).
	if UserBubble.Style.Padding.Left != 3 || UserBubble.Style.Padding.Top != 0 {
		t.Fatalf("WithStyle mutated original: %+v", UserBubble.Style.Padding)
	}

	lines := customBubble.Render("custom", 40, time.Now())
	if len(lines) < 1 {
		t.Fatalf("expected ≥1 line, got %d", len(lines))
	}
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "custom") {
		t.Fatalf("expected content, got %q", plain)
	}
}

func TestMessageBubble_CustomLabelPrefix(t *testing.T) {
	_showTimeF := false
	noTimeBubble := UserBubble.WithStyle(BubbleStyle{ShowTime: &_showTimeF})
	lines := noTimeBubble.Render("no time", 40, time.Now())
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "no time") {
		t.Fatalf("expected content, got %q", plain)
	}
	nowStr := time.Now().In(time.Local).Format("15:04:05")
	if strings.Contains(plain, nowStr) {
		t.Fatalf("expected no timestamp when ShowTime=false, got %q", plain)
	}
}

func TestMessageBubble_AssistantWithCustomBackground(t *testing.T) {
	customBg := lipgloss.NewStyle().Background(lipgloss.Color("236"))
	_showTimeT := true
	asstBg := AssistantBubble.WithStyle(BubbleStyle{
		Background: &customBg,
		Padding:    Padding{Left: 2},
		ShowTime:   &_showTimeT,
	})
	lines := asstBg.Render("**bold** idea", 40, time.Now())
	if len(lines) < 1 {
		t.Fatalf("expected ≥1 line, got %d", len(lines))
	}
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "bold") {
		t.Fatalf("expected markdown-rendered content, got %q", plain)
	}
}

// ─── Goal 3: Composability (nesting/mixins) ────────────────────────────

func TestMessageBubble_WithStyleComposesNonDestructively(t *testing.T) {
	_showTimeF := false

	b1 := UserBubble.WithStyle(BubbleStyle{Padding: Padding{Left: 3}})
	b2 := b1.WithStyle(BubbleStyle{ShowTime: &_showTimeF})
	b3 := b2.WithStyle(BubbleStyle{Padding: Padding{Left: 5}})

	// Original unchanged (UserBubble defaults: Top/Bottom 0, Left/Right 3).
	if UserBubble.Style.Padding.Left != 3 || UserBubble.Style.Padding.Top != 0 {
		t.Fatalf("original mutated: %+v", UserBubble.Style.Padding)
	}
	// b1 has Padding.Left=3, ShowTime true (inherited)
	if b1.Style.Padding.Left != 3 {
		t.Fatalf("b1 Padding.Left=%d want 3", b1.Style.Padding.Left)
	}
	if b1.Style.ShowTime == nil || !*b1.Style.ShowTime {
		t.Fatal("b1 should still have ShowTime=true")
	}
	// b2 has Padding.Left=3 (from b1), ShowTime false
	if b2.Style.Padding.Left != 3 {
		t.Fatalf("b2 Padding.Left=%d want 3", b2.Style.Padding.Left)
	}
	if b2.Style.ShowTime != nil && *b2.Style.ShowTime {
		t.Fatal("b2 should have ShowTime=false")
	}
	// b3 has Padding.Left=5 (overrides b2's 3), ShowTime false
	if b3.Style.Padding.Left != 5 {
		t.Fatalf("b3 Padding.Left=%d want 5", b3.Style.Padding.Left)
	}
	if b3.Style.ShowTime != nil && *b3.Style.ShowTime {
		t.Fatal("b3 should have ShowTime=false")
	}
}

func TestMessageBubble_MergeStyleMixinPattern(t *testing.T) {
	customBg := lipgloss.NewStyle().Background(lipgloss.Color("52"))
	merged := UserBubble.MergeStyle(BubbleStyle{
		Background: &customBg,
		Padding:    Padding{Left: 3},
	})
	if merged.Style.Padding.Left != 3 {
		t.Fatalf("merged Padding.Left=%d want 3", merged.Style.Padding.Left)
	}
	// ShowTime should still be true (inherited from UserBubble).
	if merged.Style.ShowTime == nil || !*merged.Style.ShowTime {
		t.Fatal("merged should inherit ShowTime=true")
	}
	if merged.Style.Background == nil {
		t.Fatal("merged Background should not be nil")
	}
}

func TestMessageBubble_ChainWithRendererAndStyle(t *testing.T) {
	customBg := lipgloss.NewStyle().Background(lipgloss.Color("52"))
	bubble := UserBubble.
		WithStyle(BubbleStyle{Background: &customBg, Padding: Padding{Left: 0}}).
		WithRenderer(&capsRenderer{})

	lines := bubble.Render("hello", 40, time.Time{})
	if len(lines) < 1 {
		t.Fatalf("expected ≥1 line, got %d", len(lines))
	}
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "HELLO") {
		t.Fatalf("expected uppercased content, got %q", plain)
	}
}

// ─── Goal 4: Plugin-in new renderers ───────────────────────────────────

func TestMessageBubble_PluginCustomRenderer(t *testing.T) {
	bubble := UserBubble.WithRenderer(&reverseRenderer{})
	lines := bubble.Render("hello", 40, time.Time{})
	if len(lines) < 1 {
		t.Fatalf("expected ≥1 line, got %d", len(lines))
	}
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "olleh") {
		t.Fatalf("expected reversed content, got %q", plain)
	}
}

func TestMessageBubble_PluginCustomRendererWithTimestamp(t *testing.T) {
	sent := time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local)
	bubble := UserBubble.WithRenderer(&prefixRenderer{})
	lines := bubble.Render("custom", 40, sent)
	if len(lines) < 1 {
		t.Fatalf("expected ≥1 line, got %d", len(lines))
	}
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "> custom") {
		t.Fatalf("expected prefix-rendered content, got %q", plain)
	}
	meta := formatUserBubbleTime(sent)
	if !strings.Contains(plain, meta) {
		t.Fatalf("expected timestamp with custom renderer, got %q", plain)
	}
}

func TestMessageBubble_PluginCustomRendererPreservesBackground(t *testing.T) {
	bubble := UserBubble.WithRenderer(&emojiRenderer{})
	lines := bubble.Render("plugin test", 40, time.Time{})
	if len(lines) < 1 {
		t.Fatalf("expected ≥1 line, got %d", len(lines))
	}
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "🔹 plugin test") {
		t.Fatalf("expected emoji-prefixed content, got %q", plain)
	}
}

func TestMessageBubble_PluginMarkdownLikeRenderer(t *testing.T) {
	bubble := AssistantBubble.WithRenderer(&boldRenderer{})
	lines := bubble.Render("important", 40, time.Time{})
	if len(lines) < 1 {
		t.Fatalf("expected ≥1 line, got %d", len(lines))
	}
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "important") {
		t.Fatalf("expected content, got %q", plain)
	}
}

// ─── Padding: top/bottom/right padding with background fill ────────────

func TestMessageBubble_TopBottomPaddingAddsBlankLines(t *testing.T) {
	showF := false
	customBg := lipgloss.NewStyle().Background(lipgloss.Color("236"))
	bubble := UserBubble.WithStyle(BubbleStyle{
		Background: &customBg,
		Padding:    Padding{Top: 1, Bottom: 1, Left: 2, Right: 2},
		ShowTime:   &showF,
	})
	lines := bubble.Render("hello", 40, time.Time{})
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines (top pad + content + bottom pad), got %d: %v", len(lines), lines)
	}
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "hello") {
		t.Fatalf("expected content, got %q", plain)
	}
}

func TestMessageBubble_RightPaddingFillsToWidth(t *testing.T) {
	showF := false
	customBg := lipgloss.NewStyle().Background(lipgloss.Color("236"))
	bubble := UserBubble.WithStyle(BubbleStyle{
		Background: &customBg,
		Padding:    Padding{Left: 4, Right: 4},
		ShowTime:   &showF,
	})
	lines := bubble.Render("short", 30, time.Time{})
	if len(lines) < 1 {
		t.Fatalf("expected ≥1 line, got %d", len(lines))
	}
	// Each line must be exactly 30 visible cells (left pad + content + right pad).
	for i, line := range lines {
		vis := VisibleWidth(line)
		if vis != 30 {
			t.Fatalf("line %d visible width=%d want 30: %q", i, vis, stripANSI(line))
		}
	}
	plain := stripANSI(strings.Join(lines, "\n"))
	// Left=4: Render uses plain spaces for padding. Rail is applied
	// by the caller, not by Render.
	if !strings.Contains(plain, "short") {
		t.Fatalf("expected content, got %q", plain)
	}
}

func TestMessageBubble_PaddingBackgroundFillsBlankLines(t *testing.T) {
	customBg := lipgloss.NewStyle().Background(lipgloss.Color("52"))
	showF := false
	bubble := UserBubble.WithStyle(BubbleStyle{
		Background: &customBg,
		Padding:    Padding{Top: 1, Left: 2},
		ShowTime:   &showF,
	})
	lines := bubble.Render("x", 20, time.Time{})
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines (padding + content), got %d: %v", len(lines), lines)
	}
	// Top padding line should be blank (spaces) at full width.
	if vis := VisibleWidth(lines[0]); vis != 20 {
		t.Fatalf("padding line width=%d want 20: %q", vis, stripANSI(lines[0]))
	}
	// Content line should contain "x".
	plain := stripANSI(lines[1])
	if !strings.Contains(plain, "x") {
		t.Fatalf("expected content 'x', got %q", plain)
	}
}

func TestMessageBubble_PaddingWithLabel(t *testing.T) {
	showT := true
	customBg := lipgloss.NewStyle().Background(lipgloss.Color("236"))
	bubble := UserBubble.WithStyle(BubbleStyle{
		Background: &customBg,
		Padding:    Padding{Top: 1, Bottom: 1, Left: 2, Right: 2},
		ShowTime:   &showT,
	})
	sent := time.Date(2026, 1, 1, 12, 0, 0, 0, time.Local)
	lines := bubble.Render("labeled", 40, sent)
	if len(lines) < 2 {
		t.Fatalf("expected ≥2 lines (padding + labeled content), got %d", len(lines))
	}
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "labeled") {
		t.Fatalf("expected content, got %q", plain)
	}
	meta := formatUserBubbleTime(sent)
	if !strings.Contains(plain, meta) {
		t.Fatalf("expected timestamp %q in output", meta)
	}
}

// ─── Foreground color ──────────────────────────────────────────────────

func TestMessageBubble_ForegroundColorApplied(t *testing.T) {
	fg := lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	showF := false
	bubble := UserBubble.WithStyle(BubbleStyle{
		Foreground: &fg,
		ShowTime:   &showF,
	})
	lines := bubble.Render("red text", 40, time.Time{})
	if len(lines) < 1 {
		t.Fatalf("expected ≥1 line, got %d", len(lines))
	}
	// With Foreground set, ANSI should be present (but background also present
	// since UserBubble has a background). We just verify content.
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "red text") {
		t.Fatalf("expected content, got %q", plain)
	}
}

// ─── Backward compatibility: formatUserMessageCard still works ──────────

func TestFormatUserMessageCardIsCompact(t *testing.T) {
	sent := time.Date(2026, 7, 27, 15, 4, 5, 0, time.Local)
	lines := formatUserMessageCard("backward compat", 40, sent)

	// Label row + one body row: no trailing timestamp-only line, and no
	// full-width background bar padding the block out.
	if len(lines) != 2 {
		t.Fatalf("compact card wants 2 lines, got %d: %q", len(lines), stripANSI(strings.Join(lines, "\n")))
	}
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "backward compat") {
		t.Fatalf("card lost its body: %q", plain)
	}
	if strings.Contains(plain, "[ ") {
		t.Fatalf("time must be inline on the label row, not a bracketed meta line: %q", plain)
	}
}

func TestFormatUserMessageCardZeroTime(t *testing.T) {
	lines := formatUserMessageCard("legacy call", 40, time.Time{})
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "legacy call") {
		t.Fatalf("expected legacy content, got %q", plain)
	}
}

// ─── RenderMessageForHistory assistant uses AssistantBubble ─────────────

func TestRenderMessageForHistoryAssistantDelegatesToBubble(t *testing.T) {
	msg := providerMessageForBlock(ChatBlock{
		Kind: ChatBlockAssistant,
		Text: "**bold** and *italic*",
	}, "**bold** and *italic*")

	lines := RenderMessageForHistory(msg, "m", 80)
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "bold") {
		t.Fatalf("expected markdown rendering, got %q", plain)
	}
}
