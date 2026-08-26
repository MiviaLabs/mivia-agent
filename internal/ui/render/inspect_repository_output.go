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
	trimmed := strings.TrimSpace(output)
	var env inspectRepositoryEnvelope
	if err := json.Unmarshal([]byte(trimmed), &env); err != nil || len(env.Results) == 0 && env.ResultCount == 0 {
		return "", strings.Split(strings.TrimRight(output, "\n"), "\n")
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

	var out []string
	for _, path := range fileOrder {
		group := fileGroups[path]
		out = append(out, accent.Render("• "+path)+" "+subtle.Render(fmt.Sprintf("(%d matches)", len(group))))
		for _, m := range group {
			text := strings.TrimSpace(m.Text)
			if ansi.StringWidth(text) > width-12 && width > 16 {
				text = ansi.Truncate(text, width-12, "…")
			}
			out = append(out, "  "+muted.Render(fmt.Sprintf("L%-4d", m.Line))+" "+text)
			for _, ctx := range m.Context {
				out = append(out, "       "+subtle.Render(strings.TrimSpace(ctx)))
			}
		}
	}

	if env.Truncated && env.TruncationReason != "" {
		out = append(out, subtle.Render(fmt.Sprintf("… truncated (%s)", env.TruncationReason)))
	}
	return summary, out
}
