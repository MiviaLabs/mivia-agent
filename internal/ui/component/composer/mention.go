// Package composer - mention.go: @ workspace-mention menu.
//
// A mention is a workspace entity (file, symbol, session) the user inserts by
// typing "@" at any token boundary. It shares the same menu scaffold as slash
// completion but uses different trigger logic and a different candidate list.
package composer

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
)

// Mention is one workspace entity the user may reference via "@".
// Path is the workspace-relative path that gets inserted into the text.
// Display is the label shown in the picker (may equal Path).
type Mention struct {
	Path    string
	Display string
}

// mentionMenu is the "@" mention picker state. It mirrors the slash-command
// menu layout but triggers on "@" at any token start, not just the line head.
type mentionMenu struct {
	all     []Mention
	matches []Mention
	cursor  int
	offset  int
	active  bool
	query   string // the fragment after the trigger "@"
	// triggerPos is the rune index of the "@" that opened the menu in the
	// current Value(). -1 when the menu is closed.
	triggerPos int
}

// mentionTrigger reports whether the rune sequence ending at the cursor in text
// contains an active "@" token that should open the picker.
//
// Rules (ux-rules.md §5.9 adapted for "@"):
//   - "@" at any token start is a trigger (token boundary = start of string or
//     preceded by whitespace).
//   - The fragment from "@" to the cursor must contain no whitespace (a space
//     means the user finished the mention or abandoned it).
//   - Returns (query, triggerRune index, ok).
func mentionTrigger(text string, cursorPos int) (query string, pos int, ok bool) {
	if cursorPos > len(text) {
		cursorPos = len(text)
	}
	sub := text[:cursorPos]
	// Walk backwards to find the nearest "@".
	lastAt := strings.LastIndex(sub, "@")
	if lastAt < 0 {
		return "", -1, false
	}
	// Everything from "@" to the cursor is the fragment.
	fragment := sub[lastAt+1:]
	// Fragment must have no whitespace (abandoned if it does).
	if strings.ContainsAny(fragment, " \t\n") {
		return "", -1, false
	}
	// The "@" must be at a token boundary: start of string or preceded by whitespace.
	if lastAt > 0 && !isSpace(rune(sub[lastAt-1])) {
		return "", -1, false
	}
	return fragment, lastAt, true
}

func isSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n'
}

// refresh recomputes the mention match set from the current cursor and text.
func (m *mentionMenu) refresh(text string, cursorPos int) {
	query, pos, ok := mentionTrigger(text, cursorPos)
	if !ok {
		m.active = false
		m.matches = nil
		m.cursor, m.offset = 0, 0
		m.triggerPos = -1
		return
	}
	m.active = true
	m.query = query
	m.triggerPos = pos
	m.matches = rankMentions(m.all, query)
	if m.cursor >= len(m.matches) {
		m.cursor = 0
	}
	m.clampOffset()
}

// rankMentions scores and sorts mentions for the query.
// Shorter exact-prefix matches score higher; ties keep path order.
func rankMentions(all []Mention, query string) []Mention {
	type scored struct {
		m     Mention
		score int
	}
	q := strings.ToLower(query)
	out := make([]scored, 0, len(all))
	for _, m := range all {
		name := strings.ToLower(filepath.Base(m.Path))
		full := strings.ToLower(m.Path)
		switch {
		case q == "":
			out = append(out, scored{m, 1000})
		case strings.HasPrefix(name, q):
			out = append(out, scored{m, 1000 - len(name)})
		case strings.HasPrefix(full, q):
			out = append(out, scored{m, 800 - len(full)})
		case strings.Contains(name, q):
			out = append(out, scored{m, 500 - strings.Index(name, q)})
		case strings.Contains(full, q):
			out = append(out, scored{m, 200 - strings.Index(full, q)})
		case isSubsequence(q, name):
			out = append(out, scored{m, 100})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].m.Path < out[j].m.Path
	})
	res := make([]Mention, len(out))
	for i, s := range out {
		res[i] = s.m
	}
	return res
}

func (m *mentionMenu) clampOffset() {
	rows := uikitconfig.MaxCompletionRows
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+rows {
		m.offset = m.cursor - rows + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

func (m *mentionMenu) next() {
	if len(m.matches) == 0 {
		return
	}
	m.cursor = (m.cursor + 1) % len(m.matches)
	m.clampOffset()
}

func (m *mentionMenu) prev() {
	if len(m.matches) == 0 {
		return
	}
	m.cursor = (m.cursor - 1 + len(m.matches)) % len(m.matches)
	m.clampOffset()
}

// selected returns the currently highlighted Mention, or zero value.
func (m mentionMenu) selected() (Mention, bool) {
	if !m.active || len(m.matches) == 0 {
		return Mention{}, false
	}
	return m.matches[m.cursor], true
}

// replaceInText replaces the "@query" fragment in text with the accepted path,
// returning the new text and the new cursor position (placed after the inserted
// path so the user can keep typing).
func (m mentionMenu) replaceInText(text string, cursorPos int) (newText string, newCursor int) {
	if m.triggerPos < 0 || len(m.matches) == 0 {
		return text, cursorPos
	}
	accepted := m.matches[m.cursor].Path
	// Replace "@query" (from triggerPos to cursorPos) with "@path ".
	prefix := text[:m.triggerPos]
	suffix := ""
	if cursorPos <= len(text) {
		suffix = text[cursorPos:]
	}
	replacement := "@" + accepted
	newText = prefix + replacement + suffix
	newCursor = len(prefix) + len(replacement)
	return newText, newCursor
}

// view renders the mention picker rows, capped to MaxCompletionRows.
func (m mentionMenu) view(t theme.Theme, tier theme.Tier, width int) string {
	if !m.active || len(m.matches) == 0 {
		return ""
	}
	end := min(m.offset+uikitconfig.MaxCompletionRows, len(m.matches))
	rows := make([]string, 0, end-m.offset+1)
	subtle := render.Role(t, tier, theme.RoleFGSubtle)
	for i := m.offset; i < end; i++ {
		mn := m.matches[i]
		marker := "  "
		style := render.Role(t, tier, theme.RoleFG)
		if i == m.cursor {
			marker = "> "
			style = render.Role(t, tier, theme.RoleAccent)
		}
		base := filepath.Base(mn.Path)
		dir := filepath.Dir(mn.Path)
		label := marker + "@" + base
		row := style.Render(label)
		if dir != "." {
			row += "  " + subtle.Render(dir)
		}
		if width > 0 && ansi.StringWidth(row) > width {
			row = ansi.Truncate(row, width, "")
		}
		rows = append(rows, row)
	}
	if len(m.matches) > uikitconfig.MaxCompletionRows {
		rows = append(rows, subtle.Render(countLabel(m.cursor+1, len(m.matches))))
	}
	return strings.Join(rows, "\n")
}
