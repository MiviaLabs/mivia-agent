package uiadapter

import (
	"context"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// settingsAutomations
type settingsAutomations struct{ *SettingsStore }

// SetAutomationBackend installs the automation backend the Automations
// settings section delegates to. It is an INTERFACE on purpose:
// internal/automation imports cliworkflow/clichat/cliworktree, and
// INV-TUI-29 (AGENTS.md:135-139) requires this package stay isolated from
// CLI entrypoints. The composition root (internal/newtui) constructs the
// concrete *automation.Service and injects it here; this package never
// names that concrete type. nil restores the in-memory behaviour every
// existing test in this package relies on. Mirrors SetConversation/
// SetSyncOptsNotifier (settings.go:81,125) in mutex style.
func (s *SettingsStore) SetAutomationBackend(b ports.AutomationSettings) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.automationBackend = b
}

func (a settingsAutomations) Automations() []ports.Automation {
	a.mu.Lock()
	backend := a.automationBackend
	a.mu.Unlock()
	if backend != nil {
		return backend.Automations()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]ports.Automation, len(a.automations))
	copy(out, a.automations)
	return out
}

func (a settingsAutomations) Runs(automationID string, limit int) []ports.Run {
	a.mu.Lock()
	backend := a.automationBackend
	a.mu.Unlock()
	if backend != nil {
		return backend.Runs(automationID, limit)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	runs := a.runs[automationID]
	out := make([]ports.Run, len(runs))
	copy(out, runs)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (a settingsAutomations) Run(runID string) (ports.Run, bool) {
	a.mu.Lock()
	backend := a.automationBackend
	a.mu.Unlock()
	if backend != nil {
		return backend.Run(runID)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, runs := range a.runs {
		for _, r := range runs {
			if r.ID == runID {
				return r, true
			}
		}
	}
	return ports.Run{}, false
}

func (a settingsAutomations) Apply(ctx context.Context, scope ports.Scope, e ports.AutomationEdit) (ports.SaveHandle, error) {
	a.mu.Lock()
	backend := a.automationBackend
	a.mu.Unlock()
	if backend != nil {
		return backend.Apply(ctx, scope, e)
	}
	if trig, ok := e.(ports.TriggerAutomation); ok {
		return a.newSaveHandle(func() error { return a.startRun(trig.ID) }), nil
	}
	if cancel, ok := e.(ports.CancelAutomationRun); ok {
		return a.newSaveHandle(func() error { return a.cancelRun(cancel.RunID) }), nil
	}
	return a.newSaveHandle(func() error { return a.applyAutomation(e) }), nil
}

func (s *SettingsStore) findAutomation(id string) int {
	for i := range s.automations {
		if s.automations[i].ID == id {
			return i
		}
	}
	return -1
}

func (s *SettingsStore) applyAutomation(e ports.AutomationEdit) error {
	switch v := e.(type) {
	case ports.UpsertAutomation:
		if i := s.findAutomation(v.Automation.ID); i >= 0 {
			s.automations[i] = v.Automation
			return nil
		}
		s.automations = append(s.automations, v.Automation)
	case ports.RemoveAutomation:
		i := s.findAutomation(v.ID)
		if i < 0 {
			return fmt.Errorf("automation %q not found", v.ID)
		}
		s.automations = append(s.automations[:i], s.automations[i+1:]...)
	case ports.SetAutomationEnabled:
		i := s.findAutomation(v.ID)
		if i < 0 {
			return fmt.Errorf("automation %q not found", v.ID)
		}
		s.automations[i].Enabled = v.On
	default:
		return fmt.Errorf("unknown automation edit %T", e)
	}
	return nil
}

func (s *SettingsStore) startRun(automationID string) error {
	i := s.findAutomation(automationID)
	if i < 0 {
		return fmt.Errorf("automation %q not found", automationID)
	}
	s.saveSeq++
	run := ports.Run{
		ID:           fmt.Sprintf("run-%d", s.saveSeq),
		AutomationID: automationID,
		Trigger:      ports.TriggerManual,
		State:        ports.RunPending,
	}
	s.runs[automationID] = append(s.runs[automationID], run)
	summary := ports.RunSummary{ID: run.ID, State: run.State}
	s.automations[i].LastRun = &summary
	s.publishRunLocked(automationID, run)
	return nil
}

// cancelRun stops a run that is still RunPending or RunRunning,
// searching every automation's run list by run ID since a
// CancelAutomationRun edit only carries the run ID (the section that
// sends it does not track which automation the live run belongs to
// separately from the run itself). Caller holds s.mu (invoked from
// newSaveHandle's apply closure, same as applyAutomation/startRun).
func (s *SettingsStore) cancelRun(runID string) error {
	for automationID, runs := range s.runs {
		for i := range runs {
			if runs[i].ID != runID {
				continue
			}
			if runs[i].State != ports.RunPending && runs[i].State != ports.RunRunning {
				return fmt.Errorf("run %q is not cancellable (state %v)", runID, runs[i].State)
			}
			runs[i].State = ports.RunCancelled
			now := time.Now()
			runs[i].EndedAt = &now
			if j := s.findAutomation(automationID); j >= 0 {
				s.automations[j].LastRun = &ports.RunSummary{
					ID:        runs[i].ID,
					State:     runs[i].State,
					StartedAt: runs[i].StartedAt,
				}
			}
			s.publishRunLocked(automationID, runs[i])
			return nil
		}
	}
	return fmt.Errorf("run %q not found", runID)
}

// publishRunLocked delivers a run to every watcher of this automation.
//
// Watch registers a channel and returns a handle whose consumer blocks on it,
// but nothing here ever published, so the only wake was Cancel's close: each
// triggered run left a permanently blocked goroutine and never rendered. The
// send is non-blocking on a buffered channel, so one consumer that has stopped
// reading cannot wedge the trigger or its siblings. Caller holds s.mu.
func (s *SettingsStore) publishRunLocked(automationID string, run ports.Run) {
	for _, ch := range s.watchers[automationID] {
		select {
		case ch <- run:
		default:
			// Watcher is not keeping up; drop rather than block the trigger.
		}
	}
}

type runWatch struct {
	ch     chan ports.Run
	cancel func()
}

func (w *runWatch) Events() <-chan ports.Run { return w.ch }
func (w *runWatch) Cancel()                  { w.cancel() }

func (a settingsAutomations) Watch(ctx context.Context, automationID string) (ports.RunHandle, error) {
	a.mu.Lock()
	backend := a.automationBackend
	a.mu.Unlock()
	if backend != nil {
		return backend.Watch(ctx, automationID)
	}
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

func (s *SettingsStore) removeWatcherLocked(automationID string, ch chan ports.Run) {
	watchers := s.watchers[automationID]
	for i, w := range watchers {
		if w == ch {
			s.watchers[automationID] = append(watchers[:i], watchers[i+1:]...)
			close(ch)
			return
		}
	}
}
