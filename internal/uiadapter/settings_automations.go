package uiadapter

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// settingsAutomations
type settingsAutomations struct{ *SettingsStore }

func (a settingsAutomations) Automations() []ports.Automation {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]ports.Automation, len(a.automations))
	copy(out, a.automations)
	return out
}

func (a settingsAutomations) Runs(automationID string, limit int) []ports.Run {
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

func (a settingsAutomations) Apply(_ context.Context, _ ports.Scope, e ports.AutomationEdit) (ports.SaveHandle, error) {
	if trig, ok := e.(ports.TriggerAutomation); ok {
		return a.newSaveHandle(func() error { return a.startRun(trig.ID) }), nil
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
	return nil
}

type runWatch struct {
	ch     chan ports.Run
	cancel func()
}

func (w *runWatch) Events() <-chan ports.Run { return w.ch }
func (w *runWatch) Cancel()                  { w.cancel() }

func (a settingsAutomations) Watch(_ context.Context, automationID string) (ports.RunHandle, error) {
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
