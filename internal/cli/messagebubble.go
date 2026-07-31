// Package cli — shared reusable chat message bubble component.
//
// MessageBubble is a shared, reusable, customizable component for rendering
// chat messages from any role (user, assistant, etc.). It combines:
//
//   - Role-configurable visual style (background, label, padding, colors)
//   - A pluggable text renderer (plain text wrapping, markdown, etc.)
//   - Method chaining for composability (WithStyle, MergeStyle)
//   - Support for custom renderers via the BubbleRenderer interface
//
// Usage:
//
//	// Pre-built instances for common roles
//	lines := UserBubble.Render("hello", 80, time.Now())
//	lines := AssistantBubble.Render("**hello**", 80, time.Time{})
//
//	// Customize per-role (padding area gets background color)
//	bubble := UserBubble.WithStyle(BubbleStyle{
//	    Background: &myBgStyle,
//	    Padding:    Padding{Top: 1, Right: 4, Bottom: 1, Left: 4},
//	})
//
//	// Plugin a new renderer
//	type MyRenderer struct{}
//	func (r *MyRenderer) RenderText(text string, width int) []string { ... }
//	b := &MessageBubble{Style: UserBubble.Style, Renderer: &MyRenderer{}}
package cli

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Padding describes space around content inside the bubble background.
// The background color extends into the padding area.
type Padding struct {
	Top    int
	Right  int
	Bottom int
	Left   int
}

// BubbleStyle configures visual appearance for a message bubble.
// All fields are optional; zero/nil values mean "use default / no style".
type BubbleStyle struct {
	// Background is applied to every line of the bubble (solid bar).
	// The padding area gets this background color. Nil means no background.
	Background *lipgloss.Style

	// LabelStyle styles the optional timestamp/prefix label.
	// Nil when ShowTime is false or no label is needed.
	LabelStyle *lipgloss.Style

	// Foreground is applied to content text inside the bubble.
	// Nil means no foreground override (renderer ANSI passes through).
	Foreground *lipgloss.Style

	// Padding is the space around content that gets the Background color.
	// Only used when Background is non-nil.
	Padding Padding

	// LeftRail is optional 1-cell left chrome (glyph). Nil = pad spaces only.
	// Painted into the first left-pad cell on content lines (not a box border).
	LeftRail *LeftRail

	// ShowTime controls whether sentAt timestamp is rendered as a label.
	// nil means "use default" (true for UserBubble, false for AssistantBubble).
	ShowTime *bool
}

// HasBackground reports whether a non-nil background style is set.
func (s BubbleStyle) HasBackground() bool {
	return s.Background != nil
}

// HasLabelStyle reports whether a non-nil label style is set.
func (s BubbleStyle) HasLabelStyle() bool {
	return s.LabelStyle != nil
}

// HasForeground reports whether a non-nil foreground style is set.
func (s BubbleStyle) HasForeground() bool {
	return s.Foreground != nil
}

// ContentWidth returns the width available for content after subtracting
// left + right padding from totalWidth. Minimum 8.
func (s BubbleStyle) ContentWidth(totalWidth int) int {
	w := totalWidth - s.Padding.Left - s.Padding.Right
	if w < 8 {
		return 8
	}
	return w
}

// BubbleRenderer is a pluggable strategy for rendering message text content.
// Implementations can wrap plain text, render markdown, syntax highlight, etc.
type BubbleRenderer interface {
	// RenderText converts raw message text into display-ready lines.
	// Each returned string may contain ANSI codes. Width is the content width
	// in cells (not including padding). Returns nil for empty/skipped content.
	RenderText(text string, width int) []string
}

// plainTextRenderer wraps text without markdown rendering (for user messages).
type plainTextRenderer struct{}

func (r *plainTextRenderer) RenderText(text string, width int) []string {
	body := strings.TrimSpace(text)
	if body == "" {
		return []string{" "}
	}
	wrapped := wrapANSIv2(body, width)
	if wrapped == "" {
		return []string{" "}
	}
	return strings.Split(wrapped, "\n")
}

// markdownRenderer renders text as markdown (for assistant messages).
type markdownRenderer struct{}

func (r *markdownRenderer) RenderText(text string, width int) []string {
	body := strings.TrimSpace(text)
	if body == "" {
		return nil
	}
	md := RenderMarkdown(body, width)
	if md == "" {
		return nil
	}
	return strings.Split(wrapANSIv2(md, width), "\n")
}

// MessageBubble is a shared, reusable, customizable component for rendering
// chat messages. Both user and assistant messages use this with different
// style configurations and renderers.
type MessageBubble struct {
	// Style configures visual appearance.
	Style BubbleStyle

	// Renderer is the pluggable text rendering strategy.
	Renderer BubbleRenderer
}

// Pre-built bubble configurations for standard roles.
var (
	// Dark gray bar (256-color 236) — distinct from terminal default without a left rail.
	_userBgStyle = lipgloss.NewStyle().Background(lipgloss.Color(themeColorCardBg))
	// Time meta is dim (not bold blue) so body stays primary.
	_userLabelStyle = tuiDimStyle
	_showTimeTrue   = true
	_showTimeFalse  = false
	// Thin assistant accent (│ not half-block ▌). Painted only on text lines.
	_assistantRail = LeftRail{
		Width: 1, Glyph: "│", Char: "│", Color: chromeNeutral,
		Bold: false, Mode: RailModeFull, // Full = every non-blank line (see applyLeftRail)
	}

	// UserBubble: full-width dark-gray background, time then body.
	// No vertical pad — spacing is a free empty lane after the bubble in
	// appendRenderedBlockMem (tools/groups skip that lane).
	UserBubble = &MessageBubble{
		Style: BubbleStyle{
			Background: &_userBgStyle,
			LabelStyle: &_userLabelStyle,
			Padding: Padding{
				Top:    0,
				Right:  3,
				Bottom: 0,
				Left:   3,
			},
			LeftRail: nil,
			ShowTime: &_showTimeTrue,
		},
		Renderer: &plainTextRenderer{},
	}

	// AssistantBubble: horizontal pad only; thin rail on text lines via chrome.
	AssistantBubble = &MessageBubble{
		Style: BubbleStyle{
			Padding: Padding{Top: 0, Bottom: 0, Left: 2, Right: 1},
			// No rail: the assistant is the transcript's default voice
			// (see resolveRailRole).
			LeftRail: nil,
			ShowTime: &_showTimeFalse,
		},
		Renderer: &markdownRenderer{},
	}
)

// WithStyle returns a copy of the bubble with non-nil pointer fields from s
// applied. Non-zero scalar fields override existing values.
//
//	myBubble := UserBubble.WithStyle(BubbleStyle{
//	    Padding: Padding{Top: 1, Right: 4, Bottom: 1, Left: 4},
//	})
func (b *MessageBubble) WithStyle(s BubbleStyle) *MessageBubble {
	nb := *b
	if s.Background != nil {
		nb.Style.Background = s.Background
	}
	if s.LabelStyle != nil {
		nb.Style.LabelStyle = s.LabelStyle
	}
	if s.Foreground != nil {
		nb.Style.Foreground = s.Foreground
	}
	if s.Padding != (Padding{}) {
		nb.Style.Padding = s.Padding
	}
	if s.ShowTime != nil {
		nb.Style.ShowTime = s.ShowTime
	}
	if s.LeftRail != nil {
		nb.Style.LeftRail = s.LeftRail
	}
	return &nb
}

// MergeStyle returns a copy of the bubble with s merged into the existing
// style. This is the mixin/composition pattern: non-nil pointer fields from s
// override existing values.
//
//	bubble := UserBubble.MergeStyle(BubbleStyle{Padding: Padding{Left: 4}})
func (b *MessageBubble) MergeStyle(s BubbleStyle) *MessageBubble {
	return b.WithStyle(s)
}

// WithRenderer returns a copy of the bubble with a custom renderer plugged in.
// This is the plugin/extensibility entry point for new rendering strategies.
//
//	type MyRenderer struct{}
//	bubble := UserBubble.WithRenderer(&MyRenderer{})
func (b *MessageBubble) WithRenderer(r BubbleRenderer) *MessageBubble {
	nb := *b
	nb.Renderer = r
	return &nb
}

// ─── Blank-line (padding) helpers ──────────────────────────────────────

// blankLine returns a string of width cells filled with spaces, then
// wrapped in background style if one is set.
func (b *MessageBubble) blankLine(width int) string {
	row := strings.Repeat(" ", width)
	if b.Style.HasBackground() {
		return b.Style.Background.Render(row)
	}
	return row
}

// blankLines returns n blank lines of the given width, each background-filled.
func (b *MessageBubble) blankLines(n, width int) []string {
	if n <= 0 {
		return nil
	}
	out := make([]string, n)
	row := strings.Repeat(" ", width)
	if b.Style.HasBackground() {
		r := b.Style.Background.Render(row)
		for i := range out {
			out[i] = r
		}
		return out
	}
	for i := range out {
		out[i] = row
	}
	return out
}

// ─── Content line with left/right padding and background ───────────────

// paddedLine takes rendered content text (already wrapped to content width),
// wraps it in left/right padding spaces, fills to total width, and applies
// background styling.
func (b *MessageBubble) paddedLine(content, leftPadStr, rightPadFill string, totalWidth int) string {
	row := leftPadStr + content + rightPadFill
	vis := visibleWidth(row)
	if vis < totalWidth {
		row += strings.Repeat(" ", totalWidth-vis)
	}
	if b.Style.HasBackground() {
		row = b.Style.Background.Render(row)
	}
	return row
}

// applyForeground applies the Foreground lipgloss style if set.
func (b *MessageBubble) applyForeground(text string) string {
	if b.Style.HasForeground() {
		return b.Style.Foreground.Render(text)
	}
	return text
}

// ─── Render ────────────────────────────────────────────────────────────

// Render produces display-ready lines for the given message text.
// Width is the total terminal width. SentAt controls the optional
// timestamp label (zero time = no label).
//
// Layout with padding (bg fills pad cells when Background is set):
//
//	[bg]  message text…          ← body first
//	[bg]  continuation…
//	[bg]            [ 10:30PM ]  ← dim trailing meta (no seconds)
func (b *MessageBubble) Render(text string, width int, sentAt time.Time) []string {
	if width < 16 {
		width = 16
	}
	contentW := b.Style.ContentWidth(width)
	showTime := b.Style.ShowTime != nil && *b.Style.ShowTime
	leftPad := b.leftPadString()

	// Fast path: no background and no timestamp chrome.
	if !showTime && !b.Style.HasBackground() {
		return b.renderPlain(text, contentW, leftPad)
	}

	body := strings.TrimSpace(text)
	if body == "" {
		body = " "
	}
	timeMeta := ""
	if showTime && !sentAt.IsZero() {
		timeMeta = formatUserBubbleTime(sentAt)
	}

	var out []string
	out = append(out, b.blankLines(b.Style.Padding.Top, width)...)
	// Body owns the bubble; time is trailing dim meta (not a header line).
	out = append(out, b.renderBodyLines(text, contentW, leftPad, width)...)
	if timeMeta != "" {
		out = append(out, b.renderTimeMetaLine(timeMeta, width)...)
	}
	out = append(out, b.blankLines(b.Style.Padding.Bottom, width)...)
	return out
}

// formatUserBubbleTime returns dim meta like "[ 10:30PM ]" (local, no seconds).
func formatUserBubbleTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.In(time.Local).Format("[ 3:04PM ]")
}

// renderTimeMetaLine right-aligns the time label on a full-width bar.
// Dim gray text; optional background matches the user bubble.
func (b *MessageBubble) renderTimeMetaLine(label string, width int) []string {
	if label == "" {
		return nil
	}
	plainPad := width - visibleWidth(label)
	if plainPad < 0 {
		plainPad = 0
	}
	dimLabel := tuiDimStyle.Render(label)
	if b.Style.HasLabelStyle() {
		dimLabel = b.Style.LabelStyle.Render(label)
	}
	if b.Style.HasBackground() {
		// Continuous bg: fill pad cells, then dim label (label carries its own SGR).
		return []string{b.Style.Background.Render(strings.Repeat(" ", plainPad)) + dimLabel}
	}
	return []string{strings.Repeat(" ", plainPad) + dimLabel}
}

// leftPadString builds plain left padding spaces.
// Full-height LeftRail is applied by renderOneChatBlock → applyLeftRail after Render.
func (b *MessageBubble) leftPadString() string {
	return strings.Repeat(" ", b.Style.Padding.Left)
}

func (b *MessageBubble) renderPlain(text string, contentW int, leftPad string) []string {
	lines := b.Renderer.RenderText(text, contentW)
	if len(lines) == 0 {
		return nil
	}
	out := make([]string, len(lines))
	for i, line := range lines {
		row := leftPad + line
		if b.Style.Padding.Right > 0 {
			vis := visibleWidth(row)
			target := b.Style.Padding.Left + contentW + b.Style.Padding.Right
			if vis < target {
				row += strings.Repeat(" ", target-vis)
			}
		}
		out[i] = b.applyForeground(row)
	}
	// Vertical pad without background: blank space lines (no bg fill).
	if top := b.Style.Padding.Top; top > 0 {
		pad := make([]string, top)
		for i := range pad {
			pad[i] = ""
		}
		out = append(pad, out...)
	}
	if bot := b.Style.Padding.Bottom; bot > 0 {
		for i := 0; i < bot; i++ {
			out = append(out, "")
		}
	}
	return out
}

func (b *MessageBubble) renderBodyLines(text string, contentW int, leftPad string, width int) []string {
	contentLines := b.Renderer.RenderText(text, contentW)
	if len(contentLines) == 0 {
		contentLines = []string{" "}
	}
	out := make([]string, 0, len(contentLines))
	for _, line := range contentLines {
		out = append(out, b.applyForeground(b.paddedLine(line, leftPad, "", width)))
	}
	return out
}
