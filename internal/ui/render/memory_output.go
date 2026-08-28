package render

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

type memoryItem struct {
	ID      string   `json:"id"`
	Scope   string   `json:"scope"`
	Created string   `json:"created"`
	Summary string   `json:"summary"`
	Tags    []string `json:"tags"`
}

// FormatMemoryOutput renders memory search/list results into clean memory cards.
func FormatMemoryOutput(t theme.Theme, tier theme.Tier, output string, width int) (string, []string) {
	trimmed := UnwrapJSONString(strings.TrimSpace(output))
	var items []memoryItem

	if strings.HasPrefix(trimmed, "[") {
		_ = json.Unmarshal([]byte(trimmed), &items)
	} else if strings.HasPrefix(trimmed, "{") {
		var wrapped struct {
			Results []memoryItem `json:"results"`
		}
		if err := json.Unmarshal([]byte(trimmed), &wrapped); err == nil && len(wrapped.Results) > 0 {
			items = wrapped.Results
		} else {
			var single memoryItem
			if err := json.Unmarshal([]byte(trimmed), &single); err == nil && single.Summary != "" {
				items = []memoryItem{single}
			}
		}
	}

	if len(items) == 0 {
		// Plain sentences (memory_save's "saved memory ...") pass
		// through as-is; a payload that LOOKED like JSON but did not
		// parse gets the unparsed label, not a naked blob (R1).
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			return "", rawToolFallback(t, tier, output)
		}
		return "", strings.Split(strings.TrimRight(output, "\n"), "\n")
	}

	accent := Role(t, tier, theme.RoleAccent)
	subtle := Role(t, tier, theme.RoleFGSubtle)
	fg := Role(t, tier, theme.RoleFG)

	var out []string
	for _, item := range items {
		id := item.ID
		if len(id) > 8 {
			id = id[:8]
		}
		header := "•"
		if item.Scope != "" {
			header += " [" + item.Scope + "]"
		}
		if id != "" {
			header += " " + id
		}
		line1 := accent.Render(header)
		if item.Created != "" {
			line1 += " " + subtle.Render("("+item.Created+")")
		}
		out = append(out, line1)

		summary := strings.TrimSpace(item.Summary)
		if width > 8 && ansi.StringWidth(summary) > width-4 {
			summary = ansi.Truncate(summary, width-4, "…")
		}
		if summary != "" {
			out = append(out, "  "+fg.Render(summary))
		}

		if len(item.Tags) > 0 {
			out = append(out, "  "+subtle.Render("tags: "+strings.Join(item.Tags, ", ")))
		}
	}

	summary := fmt.Sprintf("%d memory item", len(items))
	if len(items) != 1 {
		summary += "s"
	}
	return summary, out
}
