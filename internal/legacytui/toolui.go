package legacytui

import (
	"fmt"
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// ToolRow is relocated to internal/cli (needed there for tool-wave counting
// and by the classic-mode renderer). Aliased here so this package's own call
// sites are unchanged; methods that used to hang off ToolRow (elapsed, icon)
// are now free functions below since a type alias cannot carry new methods.
type ToolRow = cli.ToolRow

// toolRenderOptions is package-local: RailOpts (internal/cli) mirrors its
// shape for the rail-resolution path; this type stays here for the
// tool-panel-specific render functions below.
type toolRenderOptions struct{ ASCII, Color bool }

func terminalToolRenderOptions() toolRenderOptions {
	term := strings.ToLower(os.Getenv("TERM"))
	plain := os.Getenv("NO_COLOR") != "" || term == "dumb"
	return toolRenderOptions{ASCII: term == "dumb", Color: !plain}
}

// NewToolRenderItem, BoundedToolText, FormatDuration, ParseToolPath,
// IsLifecycleStatus, IsEditTool, ColorDiffLine, ClipPreviewLine,
// TruncatePreviewUTF8, ResultLooksLikeDiff, ToolIconForName are relocated to
// internal/cli (needed there by the classic-mode renderer); aliased here so
// this package's own call sites are unchanged.
var (
	NewToolRenderItem   = cli.NewToolRenderItem
	BoundedToolText     = cli.BoundedToolText
	FormatDuration      = cli.FormatDuration
	ParseToolPath       = cli.ParseToolPath
	IsLifecycleStatus   = cli.IsLifecycleStatus
	IsEditTool          = cli.IsEditTool
	ColorDiffLine       = cli.ColorDiffLine
	ClipPreviewLine     = cli.ClipPreviewLine
	TruncatePreviewUTF8 = cli.TruncatePreviewUTF8
	ResultLooksLikeDiff = cli.ResultLooksLikeDiff
	ToolIconForName     = cli.ToolIconForName
	// lifecycleStatusFailed is relocated to internal/cli as
	// LifecycleStatusFailed (needed unqualified in both packages).
	lifecycleStatusFailed = cli.LifecycleStatusFailed
)

func formatToolLine(t cli.ToolRenderItem, width int, opts toolRenderOptions) string {
	// Leave room for optional lifecycle badge (running/queued).
	status, summary := t.StatusIcon(opts.ASCII), t.Summary(cli.Max(12, width-32))
	kind := toolKindIcon(t.Name, opts.ASCII)
	if !opts.Color {
		return fmt.Sprintf("  %s %s %s %s", status, kind, t.Name, summary)
	}
	if t.Failed {
		status = ToolErrStyle.Render(status)
	} else if t.Done {
		status = ToolOkStyle.Render(status)
	}
	return fmt.Sprintf("  %s %s %s %s", status, kind, ToolNameStyle.Render(t.Name), ToolDimStyle.Render(summary))
}

// formatToolPanelLine is the colored panel row: icon kind name [status] summary elapsed.
func formatToolPanelLine(r ToolRow, iconStyled string, width int, now time.Time, selected bool) string {
	path := ParseToolPath(r.Detail, r.Result)
	pathPart := ""
	if path != "" {
		chip := path
		if len(chip) > cli.Max(12, width/3) {
			chip = "…" + chip[len(chip)-cli.Max(11, width/3-1):]
		}
		pathPart = " " + ToolPathStyle.Render(" "+chip+" ")
	}
	item := NewToolRenderItem(r.Name, r.Detail, r.Result, r.Done, r.Failed)
	budget := cli.Max(12, width-48-len(path))
	summary := item.Summary(budget)
	if path != "" && summary == path {
		summary = ""
	}
	statusPart := ""
	if st := strings.TrimSpace(r.Status); st != "" && !r.Done {
		statusPart = " " + ToolDimStyle.Render(st)
	}
	marker := "  "
	if selected {
		marker = cli.GlyphTriR + " "
	}
	// Nested tools carry a ◆ agent badge so parallel subagents stay
	// distinguishable from the session's own calls.
	agentPart := ""
	if r.Agent != "" {
		agentPart = AgentBadgeStyle.Render(cli.GlyphDiamond+" "+r.Agent) + " "
	}
	line := fmt.Sprintf("%s%s %s %s%s%s %s %s",
		marker, iconStyled, toolKindIcon(r.Name, false), agentPart+ToolNameStyle.Render(r.Name),
		statusPart, pathPart, ToolDimStyle.Render(summary), ToolTimeStyle.Render(FormatDuration(toolRowElapsed(r, now))),
	)
	if selected {
		line = toolSelStyle.Render(line)
	}
	return line
}

// toolKindIcon returns a glyph for the tool name. ASCII terminals get a
// single-byte stand-in so dumb TERM / NO_COLOR still show tool kind.
func toolKindIcon(name string, ascii bool) string {
	if !ascii {
		return ToolIconForName(name)
	}
	switch name {
	case "read_file":
		return "r"
	case "list_dir":
		return "d"
	case "grep", "glob":
		return "/"
	case "write_file", "search_replace", "multi_edit":
		return "e"
	case "run_command":
		return ">"
	case "search":
		return "w"
	case cli.HandlerDelegate, cli.ToolDispatchTasks:
		return "+"
	default:
		return "-"
	}
}

// toolRowElapsed replaces the former ToolRow.elapsed method: ToolRow is now a
// type alias of cli.ToolRow, and an alias cannot carry new methods.
func toolRowElapsed(r ToolRow, now time.Time) time.Duration {
	if r.Done {
		if r.End.IsZero() {
			return 0
		}
		return r.End.Sub(r.Start)
	}
	if r.Start.IsZero() {
		return 0
	}
	return now.Sub(r.Start)
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// toolRowIcon replaces the former ToolRow.icon method: see toolRowElapsed.
func toolRowIcon(r ToolRow, now time.Time) string {
	if r.Done {
		if r.Failed {
			return cli.GlyphCross
		}
		return cli.GlyphCheck
	}
	idx := int(now.UnixMilli()/80) % len(spinnerFrames)
	return spinnerFrames[idx]
}

// ToolOkStyle, ToolNameStyle, ToolTimeStyle, ToolPathStyle, AgentBadgeStyle
// are relocated to internal/cli (needed there by the classic-mode renderer);
// aliased here so this package's own call sites are unchanged.
var (
	toolRunStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorInfo))
	ToolOkStyle     = cli.ToolOkStyle
	ToolNameStyle   = cli.ToolNameStyle
	ToolTimeStyle   = cli.ToolTimeStyle
	toolSelStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorBright)).Background(lipgloss.Color(themeColorSelBg))
	toolSection     = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorInfo)).Faint(true)
	ToolPathStyle   = cli.ToolPathStyle
	AgentBadgeStyle = cli.AgentBadgeStyle
	// GitHub-style diff colors (full-width backgrounds).
	toolDiffAddBg  = lipgloss.NewStyle().Foreground(lipgloss.Color(ThemeColorDiffAdd)).Background(lipgloss.Color(themeColorDiffAddBg)) // green on dark green
	toolDiffDelBg  = lipgloss.NewStyle().Foreground(lipgloss.Color(ThemeColorDiffDel)).Background(lipgloss.Color(themeColorDiffDelBg)) // red on dark red
	toolDiffHeader = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorInfo))                                                    // cyan header
	toolDiffOld    = lipgloss.NewStyle().Foreground(lipgloss.Color(ThemeColorDiffDel)).Faint(true)                                     // red (kept for legacy)
	toolDiffNew    = lipgloss.NewStyle().Foreground(lipgloss.Color(ThemeColorDiffAdd))                                                 // green (kept for legacy)
)

// renderToolPanel is the legacy entry point used by tests/benches.
// Delegates to the windowed panel (max 6 rows, active+recent first).
// Expand previews only paint for the selected row; if selectedIdx < 0 and a row
// is Expanded, the first Expanded row is selected so legacy tests still see I/O.
func renderToolPanel(rows []ToolRow, width int, now time.Time, selectedIdx int, logoFrame int, phase brandPhase) (string, int) {
	st := toolPanelState{Selected: selectedIdx, Focused: selectedIdx >= 0}
	if selectedIdx < 0 {
		for i := range rows {
			if rows[i].Expanded {
				st.Selected = i
				break
			}
		}
	}
	out, n, _ := renderToolPanelWindow(rows, width, now, st, logoFrame, phase, toolMaxVisibleRows, 0)
	return out, n
}
