package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// workflowRunRow is one workflow run in the /workflows sidebar. description
// and nextStep are resolved from the workspace definition at refresh time;
// both are empty when the definition is missing or unreadable.
type workflowRunRow struct {
	run         workflowledger.RunSnapshot
	description string
	nextStep    string
}

// workflowsSidebar stores the workflow-run list state for the right sidebar.
type workflowsSidebar struct {
	cursor      int
	scroll      int
	dirty       bool
	lastRefresh time.Time
	rows        []workflowRunRow
}

const (
	workflowsRowsY      = 2 // after title (0) and divider (1)
	workflowsChromeRows = 3 // title, divider, footer
)

// workflowRunRowLines is the number of terminal lines one row occupies. The
// selected row expands with detail lines when it has any.
func workflowRunRowLines(row workflowRunRow, selected bool) int {
	if !selected {
		return 2
	}
	lines := 2
	if row.description != "" {
		lines++
	}
	if row.nextStep != "" {
		lines++
	}
	return lines
}

// workflowRunDot maps a run status onto the shared sidebar live-dot palette.
// Active statuses use the live colors; pending and terminal runs use the idle
// dot.
func workflowRunDot(status workflowledger.RunStatus) sidebarLiveStatus {
	switch status {
	case workflowledger.RunStatusRunning:
		return liveStatusThinking
	case workflowledger.RunStatusWaitingApproval:
		return liveStatusTools
	case workflowledger.RunStatusDeliveryPending:
		return liveStatusStreaming
	default:
		return liveStatusIdle
	}
}

func newWorkflowsSidebar() *workflowsSidebar {
	return &workflowsSidebar{}
}

// move clamps the cursor by delta within [0, len(rows)].
func (s *workflowsSidebar) move(rows []workflowRunRow, delta int) {
	if len(rows) == 0 {
		s.cursor = 0
		return
	}
	cursor := s.cursor
	if cursor < 0 {
		cursor = 0
	} else if cursor >= len(rows) {
		cursor = len(rows) - 1
	}
	if delta > 0 {
		if delta > len(rows)-1-cursor {
			cursor = len(rows) - 1
		} else {
			cursor += delta
		}
	} else if delta < 0 {
		if delta < -cursor {
			cursor = 0
		} else {
			cursor += delta
		}
	}
	s.cursor = cursor
}

// selected returns the row under the cursor.
func (s *workflowsSidebar) selected(rows []workflowRunRow) (workflowRunRow, bool) {
	s.move(rows, 0)
	if len(rows) == 0 {
		return workflowRunRow{}, false
	}
	return rows[s.cursor], true
}

// rowTops returns the top line of each row relative to the rows region,
// before the scroll offset is applied.
func (s *workflowsSidebar) rowTops(rows []workflowRunRow) []int {
	tops := make([]int, len(rows))
	line := 0
	for i := range rows {
		tops[i] = line
		line += workflowRunRowLines(rows[i], i == s.cursor)
	}
	return tops
}

// clampScroll keeps the selected row fully visible and the scroll within the
// rows region of height space.
func (s *workflowsSidebar) clampScroll(rows []workflowRunRow, space int) {
	if len(rows) == 0 {
		s.scroll = 0
		return
	}
	space = max(1, space)
	tops := s.rowTops(rows)
	selTop := tops[s.cursor]
	selBottom := selTop + workflowRunRowLines(rows[s.cursor], true)
	if s.scroll > selTop {
		s.scroll = selTop
	}
	if s.scroll+space < selBottom {
		s.scroll = selBottom - space
	}
	last := tops[len(tops)-1] + workflowRunRowLines(rows[len(rows)-1], len(rows)-1 == s.cursor)
	s.scroll = min(max(0, s.scroll), max(0, last-space))
}

// cursorAt returns the sidebar cursor for a terminal row y, or ok=false when
// the row is not part of the list.
func (s *workflowsSidebar) cursorAt(rows []workflowRunRow, width, height, y int) (int, bool) {
	_ = width
	if y < 0 || y >= max(1, height) {
		return 0, false
	}
	if y < workflowsRowsY {
		return 0, false
	}
	top := y - workflowsRowsY + s.scroll
	tops := s.rowTops(rows)
	for i := range rows {
		bottom := tops[i] + workflowRunRowLines(rows[i], i == s.cursor)
		if top >= tops[i] && top < bottom {
			return i, true
		}
	}
	return 0, false
}

// view renders the workflow-run sidebar.
func (s *workflowsSidebar) view(width, height int, focused bool) string {
	width = max(1, width)
	height = max(1, height)
	rows := s.rows
	s.move(rows, 0)
	space := max(1, height-workflowsChromeRows)
	if len(rows) > 0 {
		s.clampScroll(rows, space)
	}
	title := fmt.Sprintf(" Workflows · %d runs", len(rows))
	lines := []string{
		tuiHeaderStyle.Render(sidebarPad(title, width)),
		tuiDimStyle.Render(strings.Repeat("─", width)),
	}
	if len(rows) == 0 {
		lines = append(lines, tuiDimStyle.Render(sidebarPad(" no workflow runs", width)))
	} else {
		tops := s.rowTops(rows)
		for i := range rows {
			top := tops[i]
			rowLines := workflowRunRowLines(rows[i], i == s.cursor)
			if top < s.scroll || top+rowLines > s.scroll+space {
				continue
			}
			lines = append(lines, s.renderRunRow(rows[i], i == s.cursor, width, focused)...)
		}
	}
	lines = append(lines, tuiDimStyle.Render(sidebarPad(s.footer(), width)))
	return lipgloss.NewStyle().Width(width).Height(height).Render(strings.Join(lines, "\n"))
}

// renderRunRow renders one workflow-run row. The selected row expands with a
// description line and a next-step line.
func (s *workflowsSidebar) renderRunRow(row workflowRunRow, selected bool, width int, focused bool) []string {
	marker := "  "
	if selected {
		marker = "▸ "
	}
	budget := max(1, width-runeWidth(marker))
	// One cell is reserved for the status dot so the name truncation happens
	// before the dot enters the width math (mirrors the sessions sidebar).
	budget = max(1, budget-1)
	name := truncateToWidth(row.run.WorkflowName, budget)
	dot := sidebarLiveDot(workflowRunDot(row.run.Status), width >= sidebarStatusColorMinWidth)
	line := marker + dot + name
	line = line + strings.Repeat(" ", max(0, width-lipgloss.Width(line)))
	if selected {
		line = sidebarSelectedStyle(width, focused).Render(line)
	}
	step := row.run.ActiveStepID
	if step == "" {
		step = "-"
	}
	metadata := "   step " + truncateToWidth(step, max(1, width-8))
	lines := []string{line, tuiDimStyle.Render(sidebarPad(metadata, width))}
	if selected {
		if row.description != "" {
			lines = append(lines, tuiDimStyle.Render(sidebarPad("   "+truncateToWidth(row.description, max(1, width-4)), width)))
		}
		if row.nextStep != "" {
			lines = append(lines, tuiDimStyle.Render(sidebarPad("   next: "+truncateToWidth(row.nextStep, max(1, width-10)), width)))
		}
	}
	return lines
}

// footer returns the key hints line.
func (s *workflowsSidebar) footer() string {
	if len(s.rows) == 0 {
		return " /workflows · Esc close"
	}
	return " ↑↓ · Enter info · Esc close"
}

// workflowSidebarLoad reads the ledger and the workspace definitions once and
// returns the workflow-run rows for the sidebar, active statuses above
// terminal runs and newest first inside each group. A broken definition
// directory degrades to rows without description or next step; a ledger read
// failure is returned so the caller keeps the previous rows.
var workflowSidebarLoad = func(root, configPath string) ([]workflowRunRow, error) {
	repo, closeFn, err := openWorkflowReportContext(root, configPath)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	runs, err := workflowRunsList(context.Background(), repo)
	if err != nil {
		return nil, err
	}
	compiled := workflowCompiledByName(root)
	rows := make([]workflowRunRow, 0, len(runs))
	for _, r := range runs {
		row := workflowRunRow{run: r}
		if cw := compiled[r.WorkflowName]; cw != nil {
			row.description = cw.Description
			if !workflowledger.IsTerminalRunStatus(r.Status) {
				row.nextStep = nextStepAfterActive(cw, r.ActiveStepID)
			}
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		ti := workflowledger.IsTerminalRunStatus(rows[i].run.Status)
		tj := workflowledger.IsTerminalRunStatus(rows[j].run.Status)
		if ti != tj {
			return !ti
		}
		return rows[i].run.StartedAt.After(rows[j].run.StartedAt)
	})
	return rows, nil
}

// workflowCompiledByName discovers and compiles every workflow definition in
// the workspace once per refresh. A broken definition is skipped; the sidebar
// then renders its rows without description and next step.
func workflowCompiledByName(root string) map[string]*compiler.CompiledWorkflow {
	out := map[string]*compiler.CompiledWorkflow{}
	workflows, err := definition.DiscoverWorkflows(root)
	if err != nil {
		return out
	}
	for _, d := range workflows {
		wf, _, err := definition.ParseWorkflowTOML(d.Raw, d.Name+".toml")
		if err != nil {
			continue
		}
		compiled, err := compiler.Compile(&wf)
		if err != nil {
			continue
		}
		out[d.Name] = compiled
	}
	return out
}

// workflowsSidebarRefreshInterval throttles ledger reads: at most one refresh
// per interval while the sidebar is open.
const workflowsSidebarRefreshInterval = 2 * time.Second

// workflowsSidebarRefreshMsg delivers the result of an off-goroutine ledger
// read for the /workflows sidebar. The read runs as a tea.Cmd (bubbletea
// executes commands on their own goroutine), so a slow ledger or definition
// scan never blocks the TUI update goroutine.
type workflowsSidebarRefreshMsg struct {
	rows []workflowRunRow
	err  error
}

// refreshWorkflowsSidebar returns a tea.Cmd that re-reads the ledger when the
// sidebar is open and the throttle window has passed. The read itself runs
// off the update goroutine and its result is delivered as a
// workflowsSidebarRefreshMsg, which updateMessageImpl applies. A throttled
// call marks the sidebar dirty and returns nil; a failed read is carried on
// the message so the handler keeps the previous rows. A closed sidebar is a
// no-op. This mirrors the non-blocking goroutine+Cmd pattern used by
// uiAdapter.PollCmd(): the update goroutine only does the throttle check and
// command dispatch, never the I/O.
func (m *tuiModel) refreshWorkflowsSidebar() tea.Cmd {
	sidebar := m.workflowsSidebar
	if sidebar == nil {
		return nil
	}
	now := time.Now()
	if now.Sub(sidebar.lastRefresh) < workflowsSidebarRefreshInterval {
		sidebar.dirty = true
		return nil
	}
	sidebar.lastRefresh = now
	root := m.resolveRepoRoot()
	configPath := sessionEngineConfigPath(root, m.config)
	return func() tea.Msg {
		rows, err := workflowSidebarLoad(root, configPath)
		return workflowsSidebarRefreshMsg{rows: rows, err: err}
	}
}
