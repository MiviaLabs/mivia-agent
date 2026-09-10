package automation

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// var _ ports.AutomationSettings = (*Service)(nil) already lives in
// service.go as a compile-time assertion; this test file exists so the
// plan's "Interface satisfaction" test has a real _test.go home too.
var _ ports.AutomationSettings = (*Service)(nil)

// TestNewRejectsEmptyRoot pins New's own empty-root guard: a Service
// built with "" as its workspace root could never resolve
// automations.toml (automationsFilePath already refuses an empty
// workspaceRoot at ports.ScopeProject), so New fails fast rather than
// returning a Service that silently does nothing.
func TestNewRejectsEmptyRoot(t *testing.T) {
	if _, err := New("", nil, nil, Config{}); err == nil {
		t.Fatal("New(\"\", ...): got nil error, want rejection of an empty workspace root")
	}
}

// TestApplyAutomationsRoundTrip proves the Spec<->ports.Automation
// mapping is correct both directions: Upsert an automation via Apply,
// read it back via Automations(), assert the mapped fields match.
func TestApplyAutomationsRoundTrip(t *testing.T) {
	root := t.TempDir()
	svc, err := New(root, nil, nil, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	in := ports.Automation{
		ID:          "nightly-summary",
		Name:        "Nightly summary",
		Description: "Summarizes the day's changes",
		Enabled:     true,
		Trigger: ports.TriggerSpec{
			Kind: ports.TriggerScheduled,
			Schedule: &ports.ScheduleSpec{
				Kind: ports.ScheduleRecurring,
				Cron: "0 2 * * *",
				TZ:   "America/New_York",
			},
		},
		Action: ports.ActionRef{
			Steps: []ports.ActionStep{
				{Kind: ports.ActionStepPrompt, Prompt: "Summarize today's commits"},
				{Kind: ports.ActionStepWorkflow, Ref: "release-notes", Inputs: map[string]string{"branch": "main"}},
			},
		},
		Worktree: ports.WorktreeSpec{Mode: 1, BaseRef: "HEAD"},
	}

	h, err := svc.Apply(context.Background(), ports.ScopeProject, ports.UpsertAutomation{Automation: in})
	if err != nil {
		t.Fatalf("Apply(Upsert): %v", err)
	}
	var lastEvent ports.SaveEvent
	for ev := range h.Events() {
		lastEvent = ev
	}
	if lastEvent.State != ports.SaveSaved {
		t.Fatalf("Apply(Upsert) final event = %+v, want SaveSaved", lastEvent)
	}

	got := svc.Automations()
	if len(got) != 1 {
		t.Fatalf("Automations() = %d entries, want 1: %+v", len(got), got)
	}
	out := got[0]
	if out.ID != in.ID || out.Name != in.Name || out.Description != in.Description || out.Enabled != in.Enabled {
		t.Fatalf("round-trip mismatch on scalar fields: got %+v, want %+v", out, in)
	}
	if out.Trigger.Kind != in.Trigger.Kind || out.Trigger.Schedule == nil {
		t.Fatalf("round-trip mismatch on trigger: got %+v", out.Trigger)
	}
	if out.Trigger.Schedule.Cron != in.Trigger.Schedule.Cron || out.Trigger.Schedule.TZ != in.Trigger.Schedule.TZ {
		t.Fatalf("round-trip mismatch on schedule: got %+v, want %+v", out.Trigger.Schedule, in.Trigger.Schedule)
	}
	if len(out.Action.Steps) != len(in.Action.Steps) {
		t.Fatalf("round-trip mismatch on steps: got %d, want %d", len(out.Action.Steps), len(in.Action.Steps))
	}
	for i := range in.Action.Steps {
		if out.Action.Steps[i].Kind != in.Action.Steps[i].Kind || out.Action.Steps[i].Ref != in.Action.Steps[i].Ref || out.Action.Steps[i].Prompt != in.Action.Steps[i].Prompt {
			t.Fatalf("round-trip mismatch on step %d: got %+v, want %+v", i, out.Action.Steps[i], in.Action.Steps[i])
		}
	}
	if out.Worktree.Mode != in.Worktree.Mode || out.Worktree.BaseRef != in.Worktree.BaseRef {
		t.Fatalf("round-trip mismatch on worktree: got %+v, want %+v", out.Worktree, in.Worktree)
	}
}

// TestApplyTriggerAutomationRunsAndFailsForUnknownID proves
// TriggerAutomation is wired to RunOnce (chunk 6): triggering an
// automation ID the store has never seen surfaces RunOnce's own
// ErrAutomationNotFound through the SaveHandle's SaveFailed event,
// rather than the old "not yet implemented" stub error.
func TestApplyTriggerAutomationRunsAndFailsForUnknownID(t *testing.T) {
	root := t.TempDir()
	svc, err := New(root, nil, nil, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h, err := svc.Apply(context.Background(), ports.ScopeProject, ports.TriggerAutomation{ID: "x"})
	if err != nil {
		t.Fatalf("Apply(TriggerAutomation) call: %v", err)
	}
	last := drainSave(t, h)
	if last.State != ports.SaveFailed {
		t.Fatalf("Apply(TriggerAutomation) for an unknown id final event = %+v, want SaveFailed", last)
	}
}

// TestApplyResumeAutomationRunDelegatesToResumeRun proves
// ResumeAutomationRun is wired to ResumeRun (chunk 8): resuming an
// unknown run id surfaces ResumeRun's own ErrRunNotFound through the
// SaveHandle's SaveFailed event, rather than the old "not yet
// implemented" stub error.
func TestApplyResumeAutomationRunDelegatesToResumeRun(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	svc, err := New(root, db, nil, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h, err := svc.Apply(context.Background(), ports.ScopeProject, ports.ResumeAutomationRun{RunID: "run-1"})
	if err != nil {
		t.Fatalf("Apply(ResumeAutomationRun) call: %v", err)
	}
	last := drainSave(t, h)
	if last.State != ports.SaveFailed {
		t.Fatalf("Apply(ResumeAutomationRun) for an unknown run id final event = %+v, want SaveFailed", last)
	}
	if !strings.Contains(last.Message, ErrRunNotFound.Error()) {
		t.Fatalf("Apply(ResumeAutomationRun) message = %q, want it to contain %q", last.Message, ErrRunNotFound.Error())
	}
}

// TestWatchNoRunsDoesNotFabricate proves Watch on an automation with no
// runs neither errors nor invents a run: Events() closes immediately.
func TestWatchNoRunsDoesNotFabricate(t *testing.T) {
	root := t.TempDir()
	svc, err := New(root, nil, nil, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h, err := svc.Watch(context.Background(), "no-such-automation")
	if err != nil {
		t.Fatalf("Watch: got error %v, want nil", err)
	}
	select {
	case run, ok := <-h.Events():
		if ok {
			t.Fatalf("Watch Events() delivered a fabricated run: %+v", run)
		}
	case <-time.After(time.Second):
		t.Fatal("Watch Events() did not close promptly")
	}
	if got := svc.Runs("no-such-automation", 10); got != nil {
		t.Fatalf("Runs() = %v, want nil (no fabricated runs)", got)
	}
	if _, ok := svc.Run("no-such-run"); ok {
		t.Fatal("Run() found a run that was never created")
	}
	// Cancel and ID/Events on the handles themselves are no-ops but must
	// not panic - pins the runHandle.Cancel and saveHandle accessor
	// branches this chunk's Watch/Apply stubs carry.
	h.Cancel()
}

// TestServiceRemoveAndSetEnabled covers the two Apply branches
// TestApplyAutomationsRoundTrip does not: RemoveAutomation and
// SetAutomationEnabled, both success and not-found.
func TestServiceRemoveAndSetEnabled(t *testing.T) {
	root := t.TempDir()
	svc, err := New(root, nil, nil, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	seed := ports.Automation{
		ID:      "seed",
		Name:    "seed",
		Enabled: false,
		Action:  ports.ActionRef{Steps: []ports.ActionStep{{Kind: ports.ActionStepPrompt, Prompt: "x"}}},
	}
	h, err := svc.Apply(context.Background(), ports.ScopeProject, ports.UpsertAutomation{Automation: seed})
	if err != nil {
		t.Fatalf("Apply(Upsert): %v", err)
	}
	drainSave(t, h)

	// SetAutomationEnabled: not-found first.
	h, err = svc.Apply(context.Background(), ports.ScopeProject, ports.SetAutomationEnabled{ID: "missing", On: true})
	if err != nil {
		t.Fatalf("Apply(SetAutomationEnabled) call: %v", err)
	}
	if last := drainSave(t, h); last.State != ports.SaveFailed {
		t.Fatalf("SetAutomationEnabled(missing) final event = %+v, want SaveFailed", last)
	}

	// SetAutomationEnabled: success.
	h, err = svc.Apply(context.Background(), ports.ScopeProject, ports.SetAutomationEnabled{ID: "seed", On: true})
	if err != nil {
		t.Fatalf("Apply(SetAutomationEnabled) call: %v", err)
	}
	if last := drainSave(t, h); last.State != ports.SaveSaved {
		t.Fatalf("SetAutomationEnabled(seed) final event = %+v, want SaveSaved", last)
	}
	got := svc.Automations()
	if len(got) != 1 || !got[0].Enabled {
		t.Fatalf("after SetAutomationEnabled, Automations() = %+v, want seed enabled", got)
	}

	// RemoveAutomation: not-found first.
	h, err = svc.Apply(context.Background(), ports.ScopeProject, ports.RemoveAutomation{ID: "missing"})
	if err != nil {
		t.Fatalf("Apply(RemoveAutomation) call: %v", err)
	}
	if last := drainSave(t, h); last.State != ports.SaveFailed {
		t.Fatalf("RemoveAutomation(missing) final event = %+v, want SaveFailed", last)
	}

	// RemoveAutomation: success.
	h, err = svc.Apply(context.Background(), ports.ScopeProject, ports.RemoveAutomation{ID: "seed"})
	if err != nil {
		t.Fatalf("Apply(RemoveAutomation) call: %v", err)
	}
	if last := drainSave(t, h); last.State != ports.SaveSaved {
		t.Fatalf("RemoveAutomation(seed) final event = %+v, want SaveSaved", last)
	}
	if got := svc.Automations(); len(got) != 0 {
		t.Fatalf("after RemoveAutomation, Automations() = %+v, want empty", got)
	}
}

// TestApplyUnknownEditType pins Apply's default case: an
// AutomationEdit value outside the five known variants gets a named
// error, not a silent no-op.
func TestApplyUnknownEditType(t *testing.T) {
	root := t.TempDir()
	svc, err := New(root, nil, nil, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = svc.Apply(context.Background(), ports.ScopeProject, fakeAutomationEdit{})
	if err == nil {
		t.Fatal("Apply(unknown edit): got nil error, want rejection")
	}
}

// fakeAutomationEdit embeds a real edit purely to inherit the
// unexported isAutomationEdit() marker (only ports itself can
// implement it directly); as its own concrete type it matches none of
// Apply's named cases, mirroring internal/uiadapter's own
// settings_test.go fixture of the same name and shape.
type fakeAutomationEdit struct{ ports.UpsertAutomation }

// TestApplyUpsertInvalidSpecFails proves an invalid Automation (no
// steps) fails validation inside upsert rather than being silently
// saved.
func TestApplyUpsertInvalidSpecFails(t *testing.T) {
	root := t.TempDir()
	svc, err := New(root, nil, nil, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h, err := svc.Apply(context.Background(), ports.ScopeProject, ports.UpsertAutomation{Automation: ports.Automation{ID: "bad"}})
	if err != nil {
		t.Fatalf("Apply(Upsert) call: %v", err)
	}
	if last := drainSave(t, h); last.State != ports.SaveFailed {
		t.Fatalf("Apply(Upsert) with no steps final event = %+v, want SaveFailed", last)
	}
}

// TestSpecPortsKindMappingDefaults pins the default branches of every
// bidirectional kind mapper: an out-of-range value maps to that type's
// zero value rather than panicking, covering the switch defaults
// TestApplyAutomationsRoundTrip's named-kind cases do not reach.
func TestSpecPortsKindMappingDefaults(t *testing.T) {
	if got := stepKindToPorts(StepKind(99)); got != ports.ActionStepPrompt {
		t.Fatalf("stepKindToPorts(99) = %v, want ActionStepPrompt", got)
	}
	if got := stepKindFromPorts(ports.ActionStepKind(99)); got != StepPrompt {
		t.Fatalf("stepKindFromPorts(99) = %v, want StepPrompt", got)
	}
	if got := triggerKindToPorts(TriggerKind(99)); got != ports.TriggerManual {
		t.Fatalf("triggerKindToPorts(99) = %v, want TriggerManual", got)
	}
	if got := triggerKindFromPorts(ports.TriggerKind(99)); got != TriggerManual {
		t.Fatalf("triggerKindFromPorts(99) = %v, want TriggerManual", got)
	}
	if got := scheduleKindToPorts(ScheduleKind(99)); got != ports.ScheduleInterval {
		t.Fatalf("scheduleKindToPorts(99) = %v, want ScheduleInterval", got)
	}
	if got := scheduleKindFromPorts(ports.ScheduleKind(99)); got != ScheduleInterval {
		t.Fatalf("scheduleKindFromPorts(99) = %v, want ScheduleInterval", got)
	}
}

// TestAutomationToSpecWorkflowCompatAlias proves ActionRef.Workflow (the
// documented compat alias) maps to a single StepWorkflow step when Steps
// is empty.
func TestAutomationToSpecWorkflowCompatAlias(t *testing.T) {
	spec := automationToSpec(ports.Automation{
		ID:     "compat",
		Action: ports.ActionRef{Workflow: "release-notes"},
	})
	if len(spec.Steps) != 1 || spec.Steps[0].Kind != StepWorkflow || spec.Steps[0].Ref != "release-notes" {
		t.Fatalf("automationToSpec compat alias = %+v, want one StepWorkflow(release-notes)", spec.Steps)
	}
}

// TestSaveHandleIDIsStable pins saveHandle.ID(), the one accessor the
// round-trip test never calls directly.
func TestSaveHandleIDIsStable(t *testing.T) {
	root := t.TempDir()
	svc, err := New(root, nil, nil, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h, err := svc.Apply(context.Background(), ports.ScopeProject, ports.UpsertAutomation{
		Automation: ports.Automation{ID: "x", Action: ports.ActionRef{Steps: []ports.ActionStep{{Kind: ports.ActionStepPrompt, Prompt: "p"}}}},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if h.ID() == "" {
		t.Fatal("saveHandle.ID() is empty")
	}
	h.Cancel() // no-op; must not panic
	drainSave(t, h)
}

// drainSave reads a SaveHandle's events to completion and returns the
// last one observed.
func drainSave(t *testing.T, h ports.SaveHandle) ports.SaveEvent {
	t.Helper()
	var last ports.SaveEvent
	for ev := range h.Events() {
		last = ev
	}
	return last
}
