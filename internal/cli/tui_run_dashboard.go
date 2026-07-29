// Package cli — TUI run dashboard panel.
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

// Run dashboard styles (subtle, info-style).
var (
	dashHeaderStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)   // cyan bold
	dashRunIDSyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Faint(true) // dim run id
	dashNameStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))              // white name
	dashStatusRunning = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))               // green
	dashStatusFailed  = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))               // red
	dashStatusDone    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))               // gray
)

// runDashboard tracks active orchestration runs for the TUI dashboard panel.
// It receives lifecycle events via the Coordinator's SubscribeLifecycle.
type runDashboard struct {
	mu            sync.RWMutex
	runs          map[string]*dashRunInfo // runID → run info
	open          bool                    // panel visibility toggle
	subscribeOnce sync.Once               // ensures SubscribeLifecycle is called once
}

// dashRunInfo is the display model for one orchestration run.
type dashRunInfo struct {
	RunID       string
	DisplayName string
	Status      string // run-level status
	TaskCount   int
	TaskStates  map[string]string // taskID → status
	CreatedAt   time.Time
}

// newRunDashboard creates an empty dashboard.
func newRunDashboard() *runDashboard {
	return &runDashboard{
		runs: make(map[string]*dashRunInfo),
	}
}

// deriveRunStatus infers the run status from task states.
func (d *runDashboard) deriveRunStatus(tasks map[string]string) string {
	if len(tasks) == 0 {
		return "created"
	}
	hasRunning := false
	hasQueued := false
	hasFailed := false
	allDone := true
	for _, s := range tasks {
		switch s {
		case "running", "cancel_requested", "retry_pending":
			hasRunning = true
			allDone = false
		case "queued", "retry_queued":
			hasQueued = true
			allDone = false
		case "failed", "timed_out", "interrupted_unrecoverable":
			hasFailed = true
		case "completed":
			// done
		case "canceled":
			return "canceled"
		default:
			allDone = false
		}
	}
	if hasRunning || hasQueued {
		if hasFailed {
			return "degraded"
		}
		return "running"
	}
	if allDone && !hasFailed {
		return "completed"
	}
	if hasFailed {
		return "failed"
	}
	return "unknown"
}

// activeCount returns the number of non-terminal runs.
func (d *runDashboard) activeCount() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	count := 0
	for _, r := range d.runs {
		if r.Status != "completed" && r.Status != "failed" && r.Status != "canceled" {
			count++
		}
	}
	return count
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
		if r.Status != "completed" && r.Status != "failed" && r.Status != "canceled" {
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
			TaskStates:  make(map[string]string, len(r.TaskStates)),
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

	var b strings.Builder
	// If width is too small, do minimal rendering.
	if width < 40 {
		b.WriteString(dashHeaderStyle.Render(" Runs"))
		b.WriteString(fmt.Sprintf(" %d active", d.activeCount()))
		return b.String()
	}
	b.WriteString(dashHeaderStyle.Render(" ⚡ Orchestration Runs"))
	b.WriteString("  ")
	b.WriteString(tuiDimStyle.Render(fmt.Sprintf("[%d tracked, %d active]", len(runs), d.activeCount())))
	b.WriteString("\n")
	for _, r := range runs {
		b.WriteString(d.renderRunLine(r, width))
	}
	return b.String()
}

// renderRunLine renders one run line in the dashboard panel.
func (d *runDashboard) renderRunLine(r *dashRunInfo, width int) string {
	var b strings.Builder
	statusColor := dashStatusRunning
	switch r.Status {
	case "completed":
		statusColor = dashStatusDone
	case "failed", "degraded":
		statusColor = dashStatusFailed
	case "canceled":
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
	// Task status summary.
	taskSummary := d.taskSummary(r.TaskStates)
	b.WriteString(tuiDimStyle.Render(taskSummary))
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
	done := counts["completed"]
	var parts []string
	if done > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d done", done, total))
	}
	if n := counts["running"]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d running", n))
	}
	if n := counts["queued"]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d queued", n))
	}
	if n := counts["failed"]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", n))
	}
	if n := counts["retry_pending"]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d retrying", n))
	}
	if n := counts["blocked"]; n > 0 {
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
	case "running", "degraded":
		return "●"
	case "completed":
		return "✓"
	case "failed":
		return "✗"
	case "canceled":
		return "—"
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
// Safe to call repeatedly — the underlying sync.Once ensures one subscription.
func (d *runDashboard) trySubscribe() {
	d.subscribeOnce.Do(func() {
		// Iterate the package-level coordinators map to find an active Coordinator.
		coordinators.Range(func(_, value any) bool {
			c, ok := value.(coordinator.Coordinator)
			if !ok {
				return true // continue
			}
			// Subscribe to lifecycle events.
			c.SubscribeLifecycle(func(evt ledger.LifecycleEvent) {
				d.handleEvent(evt)
			})
			// Backfill existing runs from the coordinator.
			d.backfillFromCoordinator(c)
			return false // stop after first
		})
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
			RunID:       r.RunID,
			DisplayName: r.DisplayName,
			Status:      r.Status,
			TaskStates:  make(map[string]string),
			CreatedAt:   time.Now(),
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
			Status:     "created",
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
			info.Status = "completed"
		case "run_failed":
			info.Status = "failed"
		case "run_canceled":
			info.Status = "canceled"
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
