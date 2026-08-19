package render

import (
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/keymap"
)

// Help renders the keymap as a styled block, grouped by context.
//
// It is GENERATED from the binding table, never hand-written. A help
// screen maintained beside the dispatch table drifts from it, and the
// drift is invisible until a user presses a key the help promised.
//
// Pure: input in, string out.
func Help(t theme.Theme, tier theme.Tier, rows []keymap.HelpRow) string {
	if len(rows) == 0 {
		return ""
	}
	keyWidth := 0
	for _, r := range rows {
		if n := len(r.Keys); n > keyWidth {
			keyWidth = n
		}
	}

	var out []string
	var ctx keymap.Context
	for _, r := range rows {
		if r.Context != ctx {
			ctx = r.Context
			if len(out) > 0 {
				out = append(out, "")
			}
			out = append(out, Role(t, tier, theme.RoleFGMuted).Render(string(ctx)))
		}
		keys := r.Keys + strings.Repeat(" ", keyWidth-len(r.Keys))
		out = append(out,
			"  "+Role(t, tier, theme.RoleAccent).Render(keys)+
				"  "+Role(t, tier, theme.RoleFG).Render(r.Help))
	}
	return strings.Join(out, "\n")
}
