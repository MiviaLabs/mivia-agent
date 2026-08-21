// Package cli - TUI run dashboard panel.
// Shows active orchestration runs with status, task counts, and display names.
// Uses SubscribeLifecycle on the Coordinator for near-real-time updates.
package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/charmbracelet/lipgloss"
)

// Run dashboard styles (subtle, info-style) - colors from theme.go.
var (
	dashHeaderStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorInfo)).Bold(true)      // cyan bold
	dashRunIDSyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorWaitGray)).Faint(true) // dim run id
	dashNameStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorBright))               // white name
	dashStatusRunning = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorOk))                   // green
	dashStatusFailed  = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorStatusFailed))         // red
	dashStatusDone    = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorStatusDone))           // gray
)

// Dashboard-only / non-ledger compound states. Lifecycle statuses use ledger.RunStatus*
// / ledger.TaskStatus*; these four have no typed ledger equivalent.
const (
	taskStatusRetryQueued              = "retry_queued"
	taskStatusInterruptedUnrecoverable = "interrupted_unrecoverable"
	dashStatusDegraded                 = "degraded"
	dashStatusUnknown                  = "unknown"
)

// runDashboard tracks active orchestration runs for the TUI dashboard panel.
// It receives lifecycle events via the Coordinator's SubscribeLifecycle.
type runDashboard struct {
	mu            sync.RWMutex
	runs          map[string]*dashRunInfo // runID → run info
	open          bool                    // panel visibility toggle
	subscribeOnce sync.Once               // ensures SubscribeLifecycle is called once
	selectedIdx   int                     // cursor index for row selection (-1 = no selection)
	cursorRuns    []string                // ordered run IDs matching the rendered order
}

// dashRunInfo is the display model for one orchestration run.
type dashRunInfo struct {
	RunID       string
	DisplayName string
	Status      string // run-level status
	TaskCount   int
	TaskStates  map[string]string // taskID → status
	CreatedAt   time.Time
	// HeldByAnotherExecutor is true when the run is claimed by a different
	// mivia process, so the dashboard shows it separately from "interrupted".
	HeldByAnotherExecutor bool
}

// newRunDashboard creates an empty dashboard.
func newRunDashboard() *runDashboard {
	return &runDashboard{
		runs:        make(map[string]*dashRunInfo),
		selectedIdx: -1,
		cursorRuns:  nil,
	}
}

// deriveRunStatus infers the run status from task states.
func (d *runDashboard) deriveRunStatus(tasks map[string]string) string {
	if len(tasks) == 0 {
		return string(ledger.RunStatusCreated)
	}
	hasRunning := false
	hasQueued := false
	hasFailed := false
	allDone := true
	for _, s := range tasks {
		switch s {
		case string(ledger.TaskStatusRunning), string(ledger.TaskStatusCancelRequested), string(ledger.TaskStatusRetryPending):
			hasRunning = true
			allDone = false
		case string(ledger.TaskStatusQueued), taskStatusRetryQueued:
			hasQueued = true
			allDone = false
		case string(ledger.TaskStatusFailed), string(ledger.TaskStatusTimedOut), taskStatusInterruptedUnrecoverable:
			hasFailed = true
		case string(ledger.TaskStatusCompleted):
			// done
		case string(ledger.TaskStatusCanceled):
			return string(ledger.RunStatusCanceled)
		default:
			allDone = false
		}
	}
	if hasRunning || hasQueued {
		if hasFailed {
			return dashStatusDegraded
		}
		return string(ledger.RunStatusRunning)
	}
	if allDone && !hasFailed {
		return string(ledger.RunStatusCompleted)
	}
	if hasFailed {
		return string(ledger.RunStatusFailed)
	}
	return dashStatusUnknown
}

// activeCount returns the number of non-terminal runs.
func (d *runDashboard) activeCount() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	count := 0
	for _, r := range d.runs {
		if r.Status != string(ledger.RunStatusCompleted) && r.Status != string(ledger.RunStatusFailed) && r.Status != string(ledger.RunStatusCanceled) {
			count++
		}
	}
	return count
}

// dismissRun removes a run from the dashboard by ID.
// Used to dismiss runs that are held by another executor and cannot be resumed.
func (d *runDashboard) dismissRun(runID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.runs, runID)
	// Rebuild cursorRuns on next renderPanel.
	d.cursorRuns = nil
}

// cursorUp moves the selection cursor up by one row.
func (d *runDashboard) cursorUp() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.selectedIdx > 0 {
		d.selectedIdx--
	}
}

// cursorDown moves the selection cursor down by one row.
func (d *runDashboard) cursorDown() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.selectedIdx >= 0 && d.selectedIdx < len(d.cursorRuns)-1 {
		d.selectedIdx++
	}
}

// totalCount returns the total number of tracked runs.
func (d *runDashboard) totalCount() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.runs)
}

// summary returns a one-line summary string for the status bar.
func (d *runDashboard) summary() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if len(d.runs) == 0 {
		return ""
	}
	active := 0
	for _, r := range d.runs {
		if r.Status != string(ledger.RunStatusCompleted) && r.Status != string(ledger.RunStatusFailed) && r.Status != string(ledger.RunStatusCanceled) {
			active++
		}
	}
	if active > 0 {
		return fmt.Sprintf("⚡ %d run(s)", active)
	}
	return ""
}

// renderPanel renders the full dashboard panel.
// Returns empty string if closed or no runs.
func (d *runDashboard) renderPanel(width int) string {
	if !d.open {
		return ""
	}
	d.mu.RLock()
	if len(d.runs) == 0 {
		d.mu.RUnlock()
		return ""
	}
	// Collect and sort runs by CreatedAt descending.
	runs := make([]*dashRunInfo, 0, len(d.runs))
	for _, r := range d.runs {
		// Deep copy to avoid data race with concurrent handleEvent.
		cp := &dashRunInfo{
			RunID:       r.RunID,
			DisplayName: r.DisplayName,
			Status:      r.Status,
			TaskCount:   r.TaskCount,
			CreatedAt:   r.CreatedAt,
			// renderRunLine reads this; omitting it from the copy made it always
			// false at render time, so a run another process holds looked
			// resumable and /resume then refused it with no visible reason.
			HeldByAnotherExecutor: r.HeldByAnotherExecutor,
			TaskStates:            make(map[string]string, len(r.TaskStates)),
		}
		for k, v := range r.TaskStates {
			cp.TaskStates[k] = v
		}
		runs = append(runs, cp)
	}
	d.mu.RUnlock()
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].CreatedAt.After(runs[j].CreatedAt)
	})

	// Build cursorRuns and clamp selectedIdx.
	d.mu.Lock()
	d.cursorRuns = make([]string, len(runs))
	for i, r := range runs {
		d.cursorRuns[i] = r.RunID
	}
	if d.selectedIdx >= len(runs) {
		d.selectedIdx = len(runs) - 1
	} else if d.selectedIdx < 0 && len(runs) > 0 {
		d.selectedIdx = 0
	}
	selIdx := d.selectedIdx
	d.mu.Unlock()

	var b strings.Builder
	// If width is too small, do minimal rendering.
	if width < 40 {
		b.WriteString(dashHeaderStyle.Render(" Runs"))
		b.WriteString(fmt.Sprintf(" %d active", d.activeCount()))
		return b.String()
	}
	b.WriteString(dashHeaderStyle.Render(" ⚡ Orchestration Runs"))
	b.WriteString("  ")
	b.WriteString(TUIDimStyle.Render(fmt.Sprintf("[%d tracked, %d active]", len(runs), d.activeCount())))
	if len(runs) > 0 {
		b.WriteString("  ")
		b.WriteString(TUIDimStyle.Render("[↑↓ select, /resume <id> to resume, ctrl+r close]"))
	}
	b.WriteString("\n")
	for i, r := range runs {
		line := d.renderRunLine(r, width)
		if i == selIdx {
			// Highlight the selected row with a reverse-video marker.
			line = "▸ " + line
		} else {
			line = "  " + line
		}
		b.WriteString(line)
	}
	return b.String()
}

// renderRunLine renders one run line in the dashboard panel.
func (d *runDashboard) renderRunLine(r *dashRunInfo, width int) string {
	var b strings.Builder
	statusColor := dashStatusRunning
	switch r.Status {
	case string(ledger.RunStatusCompleted):
		statusColor = dashStatusDone
	case string(ledger.RunStatusFailed), dashStatusDegraded:
		statusColor = dashStatusFailed
	case string(ledger.RunStatusCanceled):
		statusColor = dashStatusDone
	}
	b.WriteString(statusColor.Render(bulletForStatus(r.Status)))
	b.WriteString(" ")
	b.WriteString(dashNameStyle.Render(r.DisplayName))
	if r.DisplayName != "" {
		b.WriteString(" ")
	}
	b.WriteString(dashRunIDSyle.Render(shortRunID(r.RunID)))
	b.WriteString("  ")
	// Show held-by-another status right after the run ID.
	if r.HeldByAnotherExecutor {
		b.WriteString(TUIDimStyle.Render("[held by another process]"))
		b.WriteString(" ")
	}
	// Task status summary.
	taskSummary := d.taskSummary(r.TaskStates)
	b.WriteString(TUIDimStyle.Render(taskSummary))
	b.WriteString("\n")
	return b.String()
}

// taskSummary returns a compact summary of task states.
// E.g. "3/5 done, 1 running, 1 queued"
func (d *runDashboard) taskSummary(tasks map[string]string) string {
	counts := map[string]int{}
	for _, s := range tasks {
		counts[s]++
	}
	total := len(tasks)
	done := counts[string(ledger.TaskStatusCompleted)]
	var parts []string
	if done > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d done", done, total))
	}
	if n := counts[string(ledger.TaskStatusRunning)]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d running", n))
	}
	if n := counts[string(ledger.TaskStatusQueued)]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d queued", n))
	}
	if n := counts[string(ledger.TaskStatusFailed)]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", n))
	}
	if n := counts[string(ledger.TaskStatusRetryPending)]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d retrying", n))
	}
	if n := counts[string(ledger.TaskStatusBlocked)]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d blocked", n))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%d task(s)", total)
	}
	return strings.Join(parts, ", ")
}

// bulletForStatus returns a status bullet character.
func bulletForStatus(status string) string {
	switch status {
	case string(ledger.RunStatusRunning), dashStatusDegraded:
		return "●"
	case string(ledger.RunStatusCompleted):
		return glyphCheck
	case string(ledger.RunStatusFailed):
		return glyphCross
	case string(ledger.RunStatusCanceled):
		return "-"
	default:
		return "○"
	}
}

// shortRunID returns a shortened run ID for display.
func shortRunID(id string) string {
	if len(id) > 12 {
		return id[:12] + "…"
	}
	return id
}

// trySubscribe lazily finds the Coordinator and subscribes to lifecycle events.
// Safe to call repeatedly - the underlying sync.Once ensures one subscription.
func (d *runDashboard) trySubscribe() {
	d.subscribeOnce.Do(func() {
		c, ok := activeCoordinator()
		if !ok {
			return
		}
		// Subscribe to lifecycle events.
		c.SubscribeLifecycle(func(evt ledger.LifecycleEvent) {
			d.handleEvent(evt)
		})
		// Backfill existing runs from the coordinator.
		d.backfillFromCoordinator(c)
	})
}

// backfillFromCoordinator queries the coordinator for existing interrupted runs
// and populates the dashboard with them. This ensures runs created before the
// dashboard was opened are visible.
func (d *runDashboard) backfillFromCoordinator(c coordinator.Coordinator) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runs, err := c.ListInterruptedRuns(ctx)
	if err != nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range runs {
		if _, exists := d.runs[r.RunID]; exists {
			continue
		}
		d.runs[r.RunID] = &dashRunInfo{
			RunID:                 r.RunID,
			DisplayName:           r.DisplayName,
			Status:                r.Status,
			TaskStates:            make(map[string]string),
			CreatedAt:             time.Now(),
			HeldByAnotherExecutor: r.HeldByAnotherExecutor,
		}
	}
}

// handleEvent processes a lifecycle event from the coordinator.
func (d *runDashboard) handleEvent(evt ledger.LifecycleEvent) {
	if evt.RunID == "" {
		return
	}
	kind := string(evt.Kind)
	isRunEvent := kind == "run_created" || kind == "run_completed" || kind == "run_canceled" || kind == "run_failed"

	d.mu.Lock()
	info, ok := d.runs[evt.RunID]
	if !ok {
		info = &dashRunInfo{
			RunID:      evt.RunID,
			Status:     string(ledger.RunStatusCreated),
			TaskStates: make(map[string]string),
			CreatedAt:  time.Now(),
		}
		d.runs[evt.RunID] = info
	}
	// Update task state from event kind.
	if strings.HasPrefix(kind, "task_") {
		state := strings.TrimPrefix(kind, "task_")
		if evt.TaskID != "" {
			info.TaskStates[evt.TaskID] = state
		}
		info.Status = d.deriveRunStatus(info.TaskStates)
		info.TaskCount = len(info.TaskStates)
	}
	// Handle run-level events.
	if isRunEvent {
		info.RunID = evt.RunID
		switch kind {
		case "run_completed":
			info.Status = string(ledger.RunStatusCompleted)
		case "run_failed":
			info.Status = string(ledger.RunStatusFailed)
		case "run_canceled":
			info.Status = string(ledger.RunStatusCanceled)
		}
	}
	d.mu.Unlock()
}

// toggleOpen toggles the dashboard panel visibility.
// Returns the new state.
func (d *runDashboard) toggleOpen() bool {
	d.mu.Lock()
	d.open = !d.open
	open := d.open
	d.mu.Unlock()
	return open
}

// isVisible reports whether the panel is actually drawn, which is what decides
// whether it may consume keys. `open` alone is not enough: renderPanel draws
// nothing when there are no runs and summary() is empty too, so a dashboard
// toggled open on a workspace with no orchestration runs is invisible. Letting
// that state swallow up/down left the arrow keys dead for both the composer and
// the transcript with nothing on screen to explain why.
func (d *runDashboard) isVisible() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.open && len(d.runs) > 0
}

// isOpen returns whether the dashboard panel is visible.
func (d *runDashboard) isOpen() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.open
}

// handleLifecycleEvent is exported for the poll loop to feed events.
// It delegates to trySubscribe + upsert.
func (d *runDashboard) handleLifecycleEvent(evt ledger.LifecycleEvent) {
	d.trySubscribe()
	d.handleEvent(evt)
}
