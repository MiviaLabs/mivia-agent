package transcript

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

func (b Block) renderUsage(t theme.Theme, tier theme.Tier, width int) string {
	if b.Usage == nil {
		return strings.Join(b.Body, "\n")
	}
	text := fmt.Sprintf("+ %s  %s  %s in  %s out  $%.2f",
		b.UsageModel,
		render.FormatElapsed(b.UsageElapsedMS),
		render.CompactTokens(b.Usage.InputTokens),
		render.CompactTokens(b.Usage.OutputTokens),
		b.Usage.CostUSD)
	if width <= 0 {
		width = ansi.StringWidth(text)
	}
	textWidth := ansi.StringWidth(text)
	if width > textWidth {
		text += strings.Repeat("-", width-textWidth)
	} else if width > 0 {
		text = ansi.Truncate(text, width, "")
	}
	return render.Role(t, tier, theme.RoleFGSubtle).Render(text)
}
