// Package render turns theme roles into concrete lipgloss styles and
// renders structured event bodies (diffs, markdown, dialogs, headers)
// into styled text. Every function here is pure: input in, string out,
// no I/O and no package state.
//
// Markdown renders CommonMark + GFM (headings, paragraphs, lists,
// ordered + task lists, blockquotes, fenced + indented + inline code,
// tables, links, horizontal rules, strikethrough, emphasis/strong)
// through charm.land/glamour/v2, mapped to the theme palette so a
// markdown block matches the rest of the screen at every colour tier.
package render

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"
	"github.com/alecthomas/chroma/v2"
	chromastyles "github.com/alecthomas/chroma/v2/styles"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// minMarkdownWidth is the floor Markdown applies when the caller asks
// for a width of 0 or negative. Glamour wraps at the given column count
// and refuses (or, historically, panics on) widths below about 12; the
// transcript clamps at max(20, width-2) at the call site, but the guard
// here is the last line of defence.
const minMarkdownWidth = 20

// Markdown renders assistant markdown as ANSI-styled text using the
// theme palette. It is tier-aware: every role's hex (truecolor/256),
// ANSI16 index (16-colour), or absence (ASCII/NoTTY) flows through
// Glamour's style config and chroma formatter.
//
// It returns in unchanged when Glamour returns an error or panics.
// Glamour surfaces markdown errors as strings rather than panics, and
// the renderer wraps its body in defer recover() because goldmark
// plugins have historically been panic-prone in adjacent code paths.
// The cost is one deferred closure per call; the alternative - a
// crashed transcript - is not acceptable.
//
// A width of 0 or negative is clamped to minMarkdownWidth so Glamour
// never sees a 0-column wrap. The transcript already guards this with
// max(20, width-2); this floor is the last line of defence.
func Markdown(t theme.Theme, tier theme.Tier, width int, in string) (out string) {
	if in == "" {
		return ""
	}
	if width <= 0 {
		width = minMarkdownWidth
	}
	defer func() {
		if r := recover(); r != nil {
			out = in
		}
	}()
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(styleConfigFor(t, tier)),
		glamour.WithWordWrap(width),
		glamour.WithChromaFormatter(chromaFormatter(tier)),
	)
	if err != nil {
		return in
	}
	rendered, err := r.Render(in)
	if err != nil {
		return in
	}
	return rendered
}

// chromaFormatter returns the chroma formatter name that matches the
// tier. terminal16m for truecolor, terminal256 for 256 colours,
// terminal16 for the 16-colour tier, and "" for the no-chroma tiers
// where Glamour's ASCIIStyleConfig does not need a chroma formatter.
func chromaFormatter(tier theme.Tier) string {
	switch tier {
	case theme.TierTrueColor:
		return "terminal16m"
	case theme.Tier256:
		return "terminal256"
	case theme.Tier16:
		return "terminal16"
	default:
		return ""
	}
}

// styleConfigFor maps the theme's roles onto Glamour's StyleConfig.
// At ASCII/NoTTY tiers it returns Glamour's ASCIIStyleConfig so the
// renderer is byte-stable across NO_COLOR. At the other tiers it builds a
// theme-driven config where every element draws in the colour the theme
// assigned to the role.
//
// The mapping is:
//
//	text/body             -> RoleFG
//	h1..h3                -> RoleAccent (bold via Emphasis)
//	h4..h6                -> RoleFG (bold + faint)
//	emph                  -> RoleFGMuted, italic
//	strong                -> RoleFG, bold
//	strikethrough         -> RoleFGSubtle, crossed-out
//	inline code           -> RoleKeyword fg, RoleBGInset bg
//	code block            -> RoleString fg, RoleBGSubtle bg, Chroma themed
//	link / link_text      -> RoleLink (link underlined, link_text bold)
//	blockquote            -> RoleFGSubtle, indent token "│ "
//	horizontal rule       -> RoleBorder, glyph "--------"
//	table separators      -> RoleBorder (cascades into table cells)
//	task ticked / unticked -> RoleSuccess / RoleFGMuted
//
// A role the theme does not define (e.g. a downstream theme adds
// RoleFGHeading) falls back to Glamour's own defaults: Style drift is
// cheap and defensive, no upgrade required.
func styleConfigFor(t theme.Theme, tier theme.Tier) ansi.StyleConfig {
	cfg := ansi.StyleConfig{
		Document:  ansi.StyleBlock{},
		Paragraph: ansi.StyleBlock{StylePrimitive: resolveRole(t, tier, theme.RoleFG)},
		BlockQuote: ansi.StyleBlock{
			StylePrimitive: resolveRole(t, tier, theme.RoleFGSubtle),
			Indent:         uintPtr(1),
			IndentToken:    stringPtr("│ "),
		},
		List:           ansi.StyleList{LevelIndent: 4},
		Text:           ansi.StylePrimitive{},
		HorizontalRule: mergePrimitive(resolveRole(t, tier, theme.RoleBorder), ansi.StylePrimitive{Format: "\n--------\n"}),
		Item:           ansi.StylePrimitive{BlockPrefix: "• "},
		Enumeration:    ansi.StylePrimitive{BlockPrefix: ". "},
		Task:           ansi.StyleTask{Ticked: "[x] ", Unticked: "[ ] "},
		Table:          ansi.StyleTable{StyleBlock: ansi.StyleBlock{StylePrimitive: resolveRole(t, tier, theme.RoleBorder)}},
	}
	applyHeadingStyles(&cfg, t, tier)
	applyInlineStyles(&cfg, t, tier)
	applyCodeStyles(&cfg, t, tier)
	return cfg
}

func applyHeadingStyles(cfg *ansi.StyleConfig, t theme.Theme, tier theme.Tier) {
	cfg.Heading = ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{BlockSuffix: "\n", Bold: boolPtr(true)}}
	accentHead := mergePrimitive(resolveRole(t, tier, theme.RoleAccent), ansi.StylePrimitive{Bold: boolPtr(true)})
	faintHead := mergePrimitive(resolveRole(t, tier, theme.RoleFG), ansi.StylePrimitive{Bold: boolPtr(true), Faint: boolPtr(true)})
	cfg.H1 = ansi.StyleBlock{StylePrimitive: accentHead}
	cfg.H2 = ansi.StyleBlock{StylePrimitive: accentHead}
	cfg.H3 = ansi.StyleBlock{StylePrimitive: accentHead}
	cfg.H4 = ansi.StyleBlock{StylePrimitive: faintHead}
	cfg.H5 = ansi.StyleBlock{StylePrimitive: faintHead}
	cfg.H6 = ansi.StyleBlock{StylePrimitive: faintHead}
}

func applyInlineStyles(cfg *ansi.StyleConfig, t theme.Theme, tier theme.Tier) {
	cfg.Strikethrough = mergePrimitive(resolveRole(t, tier, theme.RoleFGSubtle), ansi.StylePrimitive{CrossedOut: boolPtr(true)})
	cfg.Emph = mergePrimitive(resolveRole(t, tier, theme.RoleFGMuted), ansi.StylePrimitive{Italic: boolPtr(true)})
	cfg.Strong = mergePrimitive(resolveRole(t, tier, theme.RoleFG), ansi.StylePrimitive{Bold: boolPtr(true)})
	cfg.Link = mergePrimitive(resolveRole(t, tier, theme.RoleLink), ansi.StylePrimitive{Underline: boolPtr(true)})
	cfg.LinkText = mergePrimitive(resolveRole(t, tier, theme.RoleLink), ansi.StylePrimitive{Bold: boolPtr(true)})
}

func applyCodeStyles(cfg *ansi.StyleConfig, t theme.Theme, tier theme.Tier) {
	cfg.Code = ansi.StyleBlock{
		StylePrimitive: mergePrimitive(
			resolveRole(t, tier, theme.RoleKeyword),
			ansi.StylePrimitive{BackgroundColor: backgroundFor(t, tier, theme.RoleBGInset)},
		),
	}
	cfg.CodeBlock = ansi.StyleCodeBlock{
		StyleBlock: ansi.StyleBlock{
			StylePrimitive: mergePrimitive(
				resolveRole(t, tier, theme.RoleString),
				ansi.StylePrimitive{BackgroundColor: backgroundFor(t, tier, theme.RoleBGSubtle)},
			),
			Margin: uintPtr(2),
		},
		Theme: chromaThemeFor(t, tier),
	}
}

// resolveRole converts a role's resolved style into a StylePrimitive
// with its colour set. Bold and Dim come from the theme's Emphasis map
// when the primitive does not set them explicitly downstream; mergeRole
// or the per-element mergePrimitive takes care of overriding.
func resolveRole(t theme.Theme, tier theme.Tier, r theme.Role) ansi.StylePrimitive {
	st := t.Resolve(r, tier)
	p := ansi.StylePrimitive{}
	if st.Hex != "" {
		p.Color = stringPtr(st.Hex)
	} else if st.ANSI16 >= 0 {
		p.Color = stringPtr(fmt.Sprintf("%d", st.ANSI16))
	}
	if st.Bold {
		p.Bold = boolPtr(true)
	}
	if st.Dim {
		p.Faint = boolPtr(true)
	}
	return p
}

// backgroundFor returns a *string suitable for BackgroundColor on the
// same tier. nil when the role has no colour at this tier, so the
// primitive keeps its parent's background.
func backgroundFor(t theme.Theme, tier theme.Tier, r theme.Role) *string {
	st := t.Resolve(r, tier)
	if st.Hex != "" {
		return stringPtr(st.Hex)
	}
	if st.ANSI16 >= 0 {
		return stringPtr(fmt.Sprintf("%d", st.ANSI16))
	}
	return nil
}

// mergePrimitive unions two StylePrimitives, with the second overriding
// the first on a per-field basis. nil pointers on the override mean
// "keep the base value", which is what lets a caller set Bold on a
// primitive without nuking its colour.
func mergePrimitive(base, override ansi.StylePrimitive) ansi.StylePrimitive {
	out := base
	if override.Color != nil {
		out.Color = override.Color
	}
	if override.BackgroundColor != nil {
		out.BackgroundColor = override.BackgroundColor
	}
	if override.Bold != nil {
		out.Bold = override.Bold
	}
	if override.Italic != nil {
		out.Italic = override.Italic
	}
	if override.Underline != nil {
		out.Underline = override.Underline
	}
	if override.Faint != nil {
		out.Faint = override.Faint
	}
	if override.CrossedOut != nil {
		out.CrossedOut = override.CrossedOut
	}
	if override.BlockPrefix != "" {
		out.BlockPrefix = override.BlockPrefix
	}
	if override.BlockSuffix != "" {
		out.BlockSuffix = override.BlockSuffix
	}
	if override.Prefix != "" {
		out.Prefix = override.Prefix
	}
	if override.Suffix != "" {
		out.Suffix = override.Suffix
	}
	if override.Format != "" {
		out.Format = override.Format
	}
	return out
}

func chromaStyleString(p ansi.StylePrimitive) string {
	var s string
	if p.Color != nil {
		s = *p.Color
	}
	if p.BackgroundColor != nil {
		if s != "" {
			s += " "
		}
		s += "bg:" + *p.BackgroundColor
	}
	if p.Italic != nil && *p.Italic {
		if s != "" {
			s += " "
		}
		s += "italic"
	}
	if p.Bold != nil && *p.Bold {
		if s != "" {
			s += " "
		}
		s += "bold"
	}
	if p.Underline != nil && *p.Underline {
		if s != "" {
			s += " "
		}
		s += "underline"
	}
	return s
}

// chromaThemeFor dynamically builds and registers a Chroma style in the
// Chroma style registry under a deterministic theme name keyed by the
// theme's colors and tier. This bypasses Glamour's static "charm"
// singleton caching bug and ensures every theme switch immediately renders
// syntax highlighting in the new theme's palette.
func chromaThemeFor(t theme.Theme, tier theme.Tier) string {
	var b strings.Builder
	b.WriteString(t.Name)
	b.WriteString(tier.String())
	for k, v := range t.Colors {
		b.WriteString(string(k))
		b.WriteString(v)
	}
	for k, v := range t.ANSI16 {
		b.WriteString(string(k))
		b.WriteString(fmt.Sprintf("%d", v))
	}
	h := sha256.Sum256([]byte(b.String()))
	name := "mivia-" + hex.EncodeToString(h[:8])

	chromastyles.Register(chroma.MustNewStyle(name, chroma.StyleEntries{
		chroma.Text:                chromaStyleString(resolveRole(t, tier, theme.RoleFG)),
		chroma.Error:               chromaStyleString(resolveRole(t, tier, theme.RoleDanger)),
		chroma.Comment:             chromaStyleString(mergePrimitive(resolveRole(t, tier, theme.RoleComment), ansi.StylePrimitive{Italic: boolPtr(true)})),
		chroma.CommentPreproc:      chromaStyleString(resolveRole(t, tier, theme.RoleComment)),
		chroma.Keyword:             chromaStyleString(resolveRole(t, tier, theme.RoleKeyword)),
		chroma.KeywordReserved:     chromaStyleString(mergePrimitive(resolveRole(t, tier, theme.RoleKeyword), ansi.StylePrimitive{Bold: boolPtr(true)})),
		chroma.KeywordNamespace:    chromaStyleString(resolveRole(t, tier, theme.RoleKeyword)),
		chroma.KeywordType:         chromaStyleString(resolveRole(t, tier, theme.RoleType)),
		chroma.Operator:            chromaStyleString(resolveRole(t, tier, theme.RoleFGMuted)),
		chroma.Punctuation:         chromaStyleString(resolveRole(t, tier, theme.RoleFGMuted)),
		chroma.Name:                chromaStyleString(resolveRole(t, tier, theme.RoleFG)),
		chroma.NameBuiltin:         chromaStyleString(resolveRole(t, tier, theme.RoleType)),
		chroma.NameTag:             chromaStyleString(resolveRole(t, tier, theme.RoleType)),
		chroma.NameAttribute:       chromaStyleString(resolveRole(t, tier, theme.RoleFG)),
		chroma.NameClass:           chromaStyleString(mergePrimitive(resolveRole(t, tier, theme.RoleType), ansi.StylePrimitive{Bold: boolPtr(true)})),
		chroma.NameConstant:        chromaStyleString(resolveRole(t, tier, theme.RoleNumber)),
		chroma.NameDecorator:       chromaStyleString(resolveRole(t, tier, theme.RoleFG)),
		chroma.NameFunction:        chromaStyleString(mergePrimitive(resolveRole(t, tier, theme.RoleFunction), ansi.StylePrimitive{Bold: boolPtr(true)})),
		chroma.NameOther:           chromaStyleString(resolveRole(t, tier, theme.RoleFG)),
		chroma.LiteralNumber:       chromaStyleString(resolveRole(t, tier, theme.RoleNumber)),
		chroma.LiteralString:       chromaStyleString(resolveRole(t, tier, theme.RoleString)),
		chroma.LiteralStringEscape: chromaStyleString(mergePrimitive(resolveRole(t, tier, theme.RoleString), ansi.StylePrimitive{Bold: boolPtr(true)})),
		chroma.GenericDeleted:      chromaStyleString(resolveRole(t, tier, theme.RoleDanger)),
		chroma.GenericInserted:     chromaStyleString(mergePrimitive(resolveRole(t, tier, theme.RoleSuccess), ansi.StylePrimitive{Bold: boolPtr(true)})),
		chroma.GenericStrong:       chromaStyleString(ansi.StylePrimitive{Bold: boolPtr(true)}),
		chroma.GenericEmph:         chromaStyleString(ansi.StylePrimitive{Italic: boolPtr(true)}),
		chroma.GenericSubheading:   chromaStyleString(mergePrimitive(resolveRole(t, tier, theme.RoleFGMuted), ansi.StylePrimitive{Bold: boolPtr(true)})),
		chroma.Background:          chromaStyleString(ansi.StylePrimitive{BackgroundColor: backgroundFor(t, tier, theme.RoleBGSubtle)}),
	}))
	return name
}

func boolPtr(b bool) *bool       { return &b }
func stringPtr(s string) *string { return &s }
func uintPtr(u uint) *uint       { return &u }
