package demoharness

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// runSimStep is how long the fake run simulation waits between state
// transitions (pending -> running -> done). Small and fixed: this is a
// fake's pacing knob, not a claim about real workflow duration.
const runSimStep = 5 * time.Millisecond

// harnessAutomations is the ports.AutomationSettings adapter.
type harnessAutomations struct{ *Harness }

func (a harnessAutomations) Automations() []ports.Automation {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]ports.Automation, len(a.settingsAutomations))
	copy(out, a.settingsAutomations)
	return out
}

// Runs returns automationID's runs, newest first, capped at limit (0
// or negative means no cap).
func (a harnessAutomations) Runs(automationID string, limit int) []ports.Run {
	a.mu.Lock()
	defer a.mu.Unlock()
	runs := a.settingsRuns[automationID]
	out := make([]ports.Run, len(runs))
	copy(out, runs)
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (a harnessAutomations) Run(runID string) (ports.Run, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, runs := range a.settingsRuns {
		for _, r := range runs {
			if r.ID == runID {
				return r, true
			}
		}
	}
	return ports.Run{}, false
}

func (a harnessAutomations) Apply(_ context.Context, _ ports.Scope, e ports.AutomationEdit) (ports.SaveHandle, error) {
	if trig, ok := e.(ports.TriggerAutomation); ok {
		// A manual fire is not a config write: it starts a run and
		// reports the SAME async shape (SaveHandle) so the UI has one
		// convention, but the underlying action is
		// mivia-ai-sdk/trigger.Registry.Fire's real-world analogue,
		// which also returns only an error and no run id - the fake
		// mints one, same as the eventual adapter must.
		return a.newSaveHandle(func() error { return a.startRun(trig.ID) }), nil
	}
	return a.newSaveHandle(func() error { return a.applyAutomation(e) }), nil
}

func (h *Harness) findAutomation(id string) int {
	for i := range h.settingsAutomations {
		if h.settingsAutomations[i].ID == id {
			return i
		}
	}
	return -1
}

func (h *Harness) applyAutomation(e ports.AutomationEdit) error {
	switch v := e.(type) {
	case ports.UpsertAutomation:
		if i := h.findAutomation(v.Automation.ID); i >= 0 {
			h.settingsAutomations[i] = v.Automation
			return nil
		}
		h.settingsAutomations = append(h.settingsAutomations, v.Automation)
	case ports.RemoveAutomation:
		i := h.findAutomation(v.ID)
		if i < 0 {
			return fmt.Errorf("automation %q not found", v.ID)
		}
		h.settingsAutomations = append(h.settingsAutomations[:i], h.settingsAutomations[i+1:]...)
	case ports.SetAutomationEnabled:
		i := h.findAutomation(v.ID)
		if i < 0 {
			return fmt.Errorf("automation %q not found", v.ID)
		}
		h.settingsAutomations[i].Enabled = v.On
	default:
		return fmt.Errorf("unknown automation edit %T", e)
	}
	return nil
}

// startRun mints a run id, records the run as pending, publishes it to
// watchers, and schedules its advance through Running -> Succeeded on
// a separate goroutine, publishing each transition - the live-run
// state a Watch caller streams into a run-detail pane.
//
// startRun itself does NOT lock h.mu: it runs as the apply callback
// newSaveHandle invokes WHILE ALREADY HOLDING h.mu (see newSaveHandle).
// Locking here too would deadlock against sync.Mutex's non-reentrance -
// exactly the bug -race and TestAutomationTriggerProducesAWatchableRun
// exist to catch. The scheduled advanceRun goroutine runs unlocked and
// takes the lock itself in updateRun, since by then newSaveHandle has
// already returned and released it.
func (h *Harness) startRun(automationID string) error {
	i := h.findAutomation(automationID)
	if i < 0 {
		return fmt.Errorf("automation %q not found", automationID)
	}
	h.saveSeq++
	run := ports.Run{
		ID: fmt.Sprintf("run-%d", h.saveSeq), AutomationID: automationID,
		Trigger: ports.TriggerManual, State: ports.RunPending, StartedAt: timeNow(),
	}
	h.settingsRuns[automationID] = append(h.settingsRuns[automationID], run)
	summary := ports.RunSummary{ID: run.ID, State: run.State, StartedAt: run.StartedAt}
	h.settingsAutomations[i].LastRun = &summary
	h.publishRunLocked(run)

	go h.advanceRun(automationID, run.ID)
	return nil
}

func (h *Harness) advanceRun(automationID, runID string) {
	time.Sleep(runSimStep)
	h.updateRun(automationID, runID, ports.RunRunning, false)
	time.Sleep(runSimStep)
	h.updateRun(automationID, runID, ports.RunSucceeded, true)
}

func (h *Harness) updateRun(automationID, runID string, state ports.RunState, ended bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	runs := h.settingsRuns[automationID]
	for j := range runs {
		if runs[j].ID != runID {
			continue
		}
		runs[j].State = state
		if ended {
			now := timeNow()
			runs[j].EndedAt = &now
		}
		if i := h.findAutomation(automationID); i >= 0 {
			h.settingsAutomations[i].LastRun = &ports.RunSummary{
				ID: runs[j].ID, State: runs[j].State, StartedAt: runs[j].StartedAt,
			}
		}
		h.publishRunLocked(runs[j])
		return
	}
}

// runWatch is the fake ports.RunHandle.
type runWatch struct {
	ch     chan ports.Run
	cancel func()
}

func (w *runWatch) Events() <-chan ports.Run { return w.ch }
func (w *runWatch) Cancel()                  { w.cancel() }

func (a harnessAutomations) Watch(_ context.Context, automationID string) (ports.RunHandle, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.findAutomation(automationID) < 0 {
		return nil, fmt.Errorf("automation %q not found", automationID)
	}
	ch := make(chan ports.Run, 8)
	a.watchers[automationID] = append(a.watchers[automationID], ch)
	return &runWatch{
		ch: ch,
		cancel: func() {
			a.mu.Lock()
			defer a.mu.Unlock()
			a.removeWatcherLocked(automationID, ch)
		},
	}, nil
}

// publishRunLocked fans run out to every open Watch on its automation.
// Callers hold h.mu; the send is non-blocking (buffered, drop-oldest
// is not implemented since the buffer is generous relative to the two
// transitions per run a fake run ever produces) so a slow or absent
// watcher cannot stall the run.
func (h *Harness) publishRunLocked(run ports.Run) {
	for _, ch := range h.watchers[run.AutomationID] {
		select {
		case ch <- run:
		default:
		}
	}
}

func (h *Harness) removeWatcherLocked(automationID string, ch chan ports.Run) {
	watchers := h.watchers[automationID]
	for i, w := range watchers {
		if w == ch {
			h.watchers[automationID] = append(watchers[:i], watchers[i+1:]...)
			close(ch)
			return
		}
	}
}
