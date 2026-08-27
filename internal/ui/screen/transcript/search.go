package transcript

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// match is one search occurrence: the row, and the byte range of the
// query inside that row's plain text.
type match struct {
	row, col, end int
}

// searchState is the pager's find state. While active, typing goes to
// the query; after Enter, matches stay live so n and N keep working
// with the bar closed (rule 6.2). Esc cancels AND restores the offset
// the search started from, so a cancelled search leaves the reader
// where they were.
type searchState struct {
	active  bool
	query   string
	restore int
	matches []match
	current int
}

// begin opens the bar and records the offset to restore on cancel.
func (st *searchState) begin(offset int) {
	st.active = true
	st.query = ""
	st.restore = offset
	st.matches = nil
	st.current = -1
}

// find recomputes every occurrence of the query, case-insensitive
// substring, in row order. Multiple hits inside one row each count.
func (st *searchState) find(rows []string) {
	st.matches = st.matches[:0]
	q := strings.ToLower(st.query)
	if q == "" {
		st.current = -1
		return
	}
	savedCurrent := st.current
	for i, row := range rows {
		lower := strings.ToLower(row)
		start := 0
		for {
			at := strings.Index(lower[start:], q)
			if at < 0 {
				break
			}
			col := start + at
			st.matches = append(st.matches, match{row: i, col: col, end: col + len(q)})
			start = col + len(q)
		}
	}
	if len(st.matches) == 0 {
		st.current = -1
	} else if savedCurrent >= 0 && savedCurrent < len(st.matches) {
		st.current = savedCurrent
	} else {
		st.current = 0
	}
}

func (st searchState) count() int { return len(st.matches) }

// currentMatch reports the selected occurrence.
func (st searchState) currentMatch() (match, bool) {
	if st.current < 0 || st.current >= len(st.matches) {
		return match{}, false
	}
	return st.matches[st.current], true
}

// step moves the selection by dir (+1 next, -1 previous) and wraps, the
// way less does.
func (st *searchState) step(dir int) {
	if len(st.matches) == 0 {
		return
	}
	st.current = (st.current + dir + len(st.matches)) % len(st.matches)
}

// accept closes the bar and keeps the matches for n/N.
func (st *searchState) accept() {
	st.active = false
}

// cancel closes the bar, drops the matches, and reports the offset to
// restore.
func (st *searchState) cancel() int {
	st.active = false
	st.query = ""
	st.matches = nil
	st.current = -1
	return st.restore
}

// statusText is the match report on the bar line.
func (st searchState) statusText() string {
	if st.query == "" {
		return "type to search"
	}
	if len(st.matches) == 0 {
		return "no matches"
	}
	return itoa(st.current+1) + " of " + itoa(len(st.matches))
}

// searchKey handles a key press while the bar is open.
func (s Screen) searchKey(msg tea.KeyPressMsg) (app.Screen, tea.Cmd) {
	switch msg.String() {
	case "enter":
		s.search.accept()
		if cur, ok := s.search.currentMatch(); ok {
			s.jumpToRow(cur.row)
		}
		return s, nil
	case "esc":
		s.offset = s.search.cancel()
		s.clamp()
		return s, nil
	case "backspace":
		if n := len(s.search.query); n > 0 {
			s.search.query = s.search.query[:n-1]
		}
		s.search.find(s.rows)
		return s, nil
	}
	if msg.Text == "" || msg.Mod != 0 {
		return s, nil // control keys and combos are not search text
	}
	s.search.query += msg.Text
	s.search.find(s.rows)
	// Live jump: the reader sees matches while typing, which is also
	// what makes the Esc restore meaningful - the view really moved.
	if cur, ok := s.search.currentMatch(); ok {
		s.jumpToRow(cur.row)
	}
	return s, nil
}

// renderRow draws one row with every visible match highlighted. The
// current match is reversed in the accent role so the selection is
// readable without color alone (ux-rules.md rule 6.3), and every other
// match is reversed in the plain foreground.
func (s Screen) renderRow(row int) string {
	text := s.rows[row]
	var out strings.Builder
	at := 0
	for i, m := range s.search.matches {
		if m.row != row || m.col < at {
			continue
		}
		out.WriteString(text[at:m.col])
		style := render.Role(s.Theme, s.Tier, theme.RoleFG).Reverse(true)
		if i == s.search.current {
			style = render.Role(s.Theme, s.Tier, theme.RoleAccent).Reverse(true)
		}
		out.WriteString(style.Render(text[m.col:m.end]))
		at = m.end
	}
	out.WriteString(text[at:])
	return out.String()
}
