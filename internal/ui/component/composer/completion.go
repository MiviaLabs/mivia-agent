package composer

import (
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
)

// Command is one completion candidate.
type Command struct {
	Name string // without the leading "/"
	Desc string
}

// menu is the slash-completion state.
//
// It scores EVERY candidate and caps only the rendered rows
// (docs/design/ux-rules.md rule 5.7). Capping the candidate set instead
// is what made later matches unreachable in opencode: a candidate that
// scored well could be discarded before it was ever ranked.
type menu struct {
	all     []Command
	matches []Command
	cursor  int
	offset  int // first rendered row, for scrolling
	active  bool
}

// trigger reports whether text opens the completion menu.
//
// Start-anchored only (rule 5.1). Matching a bare "/" anywhere would
// make "src/foo" open the command menu mid-word, which is the reported
// defect this rule exists to prevent.
func trigger(text string) (string, bool) {
	if !strings.HasPrefix(text, "/") {
		return "", false
	}
	word := text[1:]
	if strings.ContainsAny(word, " \t") {
		// The command is complete and arguments have started.
		return "", false
	}
	return word, true
}

// refresh recomputes the match set from the current input.
func (m *menu) refresh(text string) {
	query, ok := trigger(text)
	if !ok {
		m.active = false
		m.matches = nil
		m.cursor, m.offset = 0, 0
		return
	}
	m.active = true
	m.matches = rank(m.all, query)
	if m.cursor >= len(m.matches) {
		m.cursor = 0
	}
	m.clampOffset()
}

// rank scores every candidate and returns the matches, best first.
//
// The score favours a prefix match, then an earlier substring match,
// then a subsequence match. Among prefix matches the shorter name wins:
// it is the closer match to what was typed. Ties keep alphabetical order
// so the list never reshuffles between keystrokes for no visible reason.
func rank(all []Command, query string) []Command {
	type scored struct {
		cmd   Command
		score int
	}
	q := strings.ToLower(query)
	out := make([]scored, 0, len(all))
	for _, c := range all {
		name := strings.ToLower(c.Name)
		switch {
		case q == "":
			out = append(out, scored{c, 1000})
		case strings.HasPrefix(name, q):
			out = append(out, scored{c, 1000 - len(name)})
		case strings.Contains(name, q):
			out = append(out, scored{c, 500 - strings.Index(name, q)})
		case isSubsequence(q, name):
			out = append(out, scored{c, 100})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].cmd.Name < out[j].cmd.Name
	})
	res := make([]Command, len(out))
	for i, s := range out {
		res[i] = s.cmd
	}
	return res
}

func isSubsequence(q, s string) bool {
	i := 0
	for _, r := range s {
		if i < len(q) && rune(q[i]) == r {
			i++
		}
	}
	return i == len(q)
}

func (m *menu) clampOffset() {
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

func (m *menu) next() {
	if len(m.matches) == 0 {
		return
	}
	m.cursor = (m.cursor + 1) % len(m.matches)
	m.clampOffset()
}

func (m *menu) prev() {
	if len(m.matches) == 0 {
		return
	}
	m.cursor = (m.cursor - 1 + len(m.matches)) % len(m.matches)
	m.clampOffset()
}

// commonPrefix is the longest prefix every match shares, for Tab.
func (m menu) commonPrefix() string {
	if len(m.matches) == 0 {
		return ""
	}
	prefix := m.matches[0].Name
	for _, c := range m.matches[1:] {
		for !strings.HasPrefix(c.Name, prefix) {
			prefix = prefix[:len(prefix)-1]
			if prefix == "" {
				return ""
			}
		}
	}
	return prefix
}

// view renders at most MaxCompletionRows rows, with a count when the
// match set is longer. The count is what tells the user that scrolling
// reaches more, rather than leaving the cap silent. Each row is clamped
// to width: the menu shares the composer's column budget, and a wider
// row would wrap the bottom block of the layout.
func (m menu) view(t theme.Theme, tier theme.Tier, width int) string {
	if !m.active || len(m.matches) == 0 {
		return ""
	}
	end := min(m.offset+uikitconfig.MaxCompletionRows, len(m.matches))
	rows := make([]string, 0, end-m.offset+1)
	for i := m.offset; i < end; i++ {
		c := m.matches[i]
		marker := "  "
		style := render.Role(t, tier, theme.RoleFG)
		if i == m.cursor {
			marker = "> "
			style = render.Role(t, tier, theme.RoleAccent)
		}
		row := marker + "/" + c.Name
		if c.Desc != "" {
			row += "  " + c.Desc
		}
		if width > 0 {
			row = ansi.Truncate(row, width, "")
		}
		rows = append(rows, style.Render(row))
	}
	if len(m.matches) > uikitconfig.MaxCompletionRows {
		rows = append(rows, render.Role(t, tier, theme.RoleFGSubtle).
			Render(countLabel(m.cursor+1, len(m.matches))))
	}
	return strings.Join(rows, "\n")
}

func countLabel(pos, total int) string {
	return "  " + itoa(pos) + " of " + itoa(total)
}

// itoa avoids pulling fmt into a hot render path for one small integer.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
