package render

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// inspectRepositoryEnvelope mirrors inspect_repository's fixed-shape result
// (internal/tools/inspect_repository.go's inspectOutput). Kept narrow and
// independent of that package - the UI layer must not import the tools
// package (mivia-ui isolation, INV-TUI-29).
type inspectRepositoryEnvelope struct {
	Results          []inspectRepositoryMatch `json:"results"`
	ResultCount      int                      `json:"result_count"`
	Truncated        bool                     `json:"truncated"`
	TruncationReason string                   `json:"truncation_reason"`
}

type inspectRepositoryMatch struct {
	Path    string   `json:"path"`
	Line    int      `json:"line"`
	Text    string   `json:"text"`
	Context []string `json:"context"`
}

// FormatInspectRepositoryOutput formats an inspect_repository result -
// matches grouped by file with line numbers, mirroring the grouping
// FormatGrepOutputWithContext already uses for grep/glob/symbol tools -
// instead of a raw JSON dump of the provenance+results envelope.
func FormatInspectRepositoryOutput(t theme.Theme, tier theme.Tier, output string, width int) (string, []string) {
	trimmed := UnwrapJSONString(strings.TrimSpace(output))
	var env inspectRepositoryEnvelope
	if err := json.Unmarshal([]byte(trimmed), &env); err != nil || len(env.Results) == 0 && env.ResultCount == 0 {
		return "", rawToolFallback(t, tier, output)
	}

	accent := Role(t, tier, theme.RoleAccent)
	subtle := Role(t, tier, theme.RoleFGSubtle)
	muted := Role(t, tier, theme.RoleFGMuted)

	fileOrder := make([]string, 0)
	fileGroups := make(map[string][]inspectRepositoryMatch)
	for _, m := range env.Results {
		if _, ok := fileGroups[m.Path]; !ok {
			fileOrder = append(fileOrder, m.Path)
		}
		fileGroups[m.Path] = append(fileGroups[m.Path], m)
	}

	summary := fmt.Sprintf("%d matches in %d files", env.ResultCount, len(fileOrder))
	if env.Truncated && env.ResultCount > len(env.Results) {
		// The envelope says more matches existed than the list carries:
		// the summary must state the claim, not the survivors.
		summary += fmt.Sprintf(" · truncated, showing %d", len(env.Results))
	}

	var out []string
	for _, path := range fileOrder {
		group := fileGroups[path]
		out = append(out, accent.Render("• "+middleTruncatePath(path, width))+" "+subtle.Render(fmt.Sprintf("(%d matches)", len(group))))
		shown := 0
		for _, m := range group {
			// One hot file must not own the whole expanded body: cap
			// each file's rows and say what the cap hid
			// (tool-output-polish.md R5).
			if shown >= maxInspectMatchesPerFile {
				out = append(out, subtle.Render(fmt.Sprintf("  … +%d more in this file", len(group)-shown)))
				break
			}
			shown++
			text := strings.TrimSpace(m.Text)
			if ansi.StringWidth(text) > width-12 && width > 16 {
				text = ansi.Truncate(text, width-12, "…")
			}
			out = append(out, "  "+muted.Render(fmt.Sprintf("L%-4d", m.Line))+" "+text)
			for _, ctx := range m.Context {
				ctx = strings.TrimSpace(ctx)
				if ansi.StringWidth(ctx) > width-9 && width > 12 {
					ctx = ansi.Truncate(ctx, width-9, "…")
				}
				out = append(out, "       "+subtle.Render(ctx))
			}
		}
	}

	if env.Truncated && env.TruncationReason != "" {
		out = append(out, subtle.Render(fmt.Sprintf("… truncated (%s)", env.TruncationReason)))
	}
	return summary, out
}

// maxInspectMatchesPerFile caps one file's rendered rows before the
// "+N more in this file" line takes over.
const maxInspectMatchesPerFile = 5

// middleTruncatePath keeps both ends of a long path visible, the same
// convention Claude Code uses for tool-use rows: a path's tail (the file
// name) and head (the repo root) identify it better than either alone.
// Short paths pass through untouched.
func middleTruncatePath(path string, width int) string {
	const maxPathCols = 48
	limit := width - 20
	if limit > maxPathCols {
		limit = maxPathCols
	}
	if limit < 16 || ansi.StringWidth(path) <= limit {
		return path
	}
	return ansi.Truncate(path, limit, "…")
}
