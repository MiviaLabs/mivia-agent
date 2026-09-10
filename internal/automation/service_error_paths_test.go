package automation

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// TestStepKindPortsMappingCoversEveryNamedCase covers
// stepKindToPorts/stepKindFromPorts' remaining named switch arms
// (Skill/Agent/Slash) that TestApplyAutomationsRoundTrip's Prompt/
// Workflow-only sample never reaches, in both directions.
func TestStepKindPortsMappingCoversEveryNamedCase(t *testing.T) {
	cases := []struct {
		spec  StepKind
		ports ports.ActionStepKind
	}{
		{StepPrompt, ports.ActionStepPrompt},
		{StepSkill, ports.ActionStepSkill},
		{StepAgent, ports.ActionStepAgent},
		{StepSlash, ports.ActionStepSlash},
		{StepWorkflow, ports.ActionStepWorkflow},
	}
	for _, tc := range cases {
		if got := stepKindToPorts(tc.spec); got != tc.ports {
			t.Errorf("stepKindToPorts(%v) = %v, want %v", tc.spec, got, tc.ports)
		}
		if got := stepKindFromPorts(tc.ports); got != tc.spec {
			t.Errorf("stepKindFromPorts(%v) = %v, want %v", tc.ports, got, tc.spec)
		}
	}
}

// TestScheduleKindPortsMappingCoversScheduleAt covers
// scheduleKindToPorts/scheduleKindFromPorts' ScheduleAt arm, which the
// round-trip test's ScheduleRecurring sample never reaches.
func TestScheduleKindPortsMappingCoversScheduleAt(t *testing.T) {
	if got := scheduleKindToPorts(ScheduleAt); got != ports.ScheduleAt {
		t.Fatalf("scheduleKindToPorts(ScheduleAt) = %v, want ports.ScheduleAt", got)
	}
	if got := scheduleKindFromPorts(ports.ScheduleAt); got != ScheduleAt {
		t.Fatalf("scheduleKindFromPorts(ports.ScheduleAt) = %v, want ScheduleAt", got)
	}
}

// TestTriggerSpecAtTimesRoundTrips covers specToPortsTrigger/
// portsTriggerToSpec's AtTimes conversion loops in both directions -
// the RFC3339 parse loop (spec -> ports) and the RFC3339 format loop
// (ports -> spec) - which the round-trip test's Cron-only sample never
// exercises. Also proves a malformed AtTimes entry is skipped rather
// than aborting the whole conversion (specToPortsTrigger's
// `if ts, err := time.Parse(...); err == nil` guard).
func TestTriggerSpecAtTimesRoundTrips(t *testing.T) {
	t1 := time.Date(2026, 9, 10, 2, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 9, 11, 3, 30, 0, 0, time.UTC)
	spec := TriggerSpec{
		Kind: TriggerScheduled,
		Schedule: &ScheduleSpec{
			Kind:    ScheduleAt,
			AtTimes: []string{t1.Format(time.RFC3339), "not-a-valid-timestamp", t2.Format(time.RFC3339)},
		},
	}
	viaPorts := specToPortsTrigger(spec)
	if viaPorts.Schedule == nil || len(viaPorts.Schedule.At) != 2 {
		t.Fatalf("specToPortsTrigger AtTimes = %+v, want exactly 2 parsed times (malformed entry skipped)", viaPorts.Schedule)
	}
	if !viaPorts.Schedule.At[0].Equal(t1) || !viaPorts.Schedule.At[1].Equal(t2) {
		t.Fatalf("specToPortsTrigger AtTimes = %v, want [%v %v]", viaPorts.Schedule.At, t1, t2)
	}

	back := portsTriggerToSpec(viaPorts)
	if back.Schedule == nil || len(back.Schedule.AtTimes) != 2 {
		t.Fatalf("portsTriggerToSpec AtTimes = %+v, want exactly 2 formatted times", back.Schedule)
	}
	if back.Schedule.AtTimes[0] != t1.Format(time.RFC3339) || back.Schedule.AtTimes[1] != t2.Format(time.RFC3339) {
		t.Fatalf("portsTriggerToSpec AtTimes = %v, want [%s %s]", back.Schedule.AtTimes, t1.Format(time.RFC3339), t2.Format(time.RFC3339))
	}
}

// TestUpsertReplacesExistingAutomation covers upsert's found-and-replace
// branch (service.go: "if specs[i].ID == spec.ID { specs[i] = spec;
// found = true; break }"), which every other test only reaches via the
// not-found append path: Upsert the same ID twice with different
// content and assert the second write replaces, not duplicates, the
// first.
func TestUpsertReplacesExistingAutomation(t *testing.T) {
	root := t.TempDir()
	svc, err := New(root, nil, nil, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	first := ports.Automation{
		ID: "dup", Name: "first",
		Action: ports.ActionRef{Steps: []ports.ActionStep{{Kind: ports.ActionStepPrompt, Prompt: "one"}}},
	}
	h, err := svc.Apply(context.Background(), ports.ScopeProject, ports.UpsertAutomation{Automation: first})
	if err != nil {
		t.Fatalf("Apply(Upsert) first: %v", err)
	}
	drainSave(t, h)

	second := ports.Automation{
		ID: "dup", Name: "second",
		Action: ports.ActionRef{Steps: []ports.ActionStep{{Kind: ports.ActionStepPrompt, Prompt: "two"}}},
	}
	h, err = svc.Apply(context.Background(), ports.ScopeProject, ports.UpsertAutomation{Automation: second})
	if err != nil {
		t.Fatalf("Apply(Upsert) second: %v", err)
	}
	if last := drainSave(t, h); last.State != ports.SaveSaved {
		t.Fatalf("Apply(Upsert) second final event = %+v, want SaveSaved", last)
	}

	got := svc.Automations()
	if len(got) != 1 {
		t.Fatalf("Automations() after replace = %d entries, want exactly 1 (replaced, not duplicated): %+v", len(got), got)
	}
	if got[0].Name != "second" {
		t.Fatalf("Automations()[0].Name = %q, want %q (the replacement content)", got[0].Name, "second")
	}
}

// TestUpsertPreservesUnattendedPolicyOnExistingAutomation covers the
// bug found during the Automations TUI editor's design review:
// ports.Automation carries no Unattended field (the UI layer has no
// need to see or edit that policy), so automationToSpec always
// defaults a freshly-mapped Spec to UnattendedDeny. Upserting an
// EXISTING automation must not silently downgrade a hand-authored
// `unattended = "auto"` policy to "deny" just because the caller
// (e.g. a future TUI edit form) round-tripped a struct that cannot
// represent it - upsert must read the on-disk value back and carry it
// through untouched when replacing an existing row.
func TestUpsertPreservesUnattendedPolicyOnExistingAutomation(t *testing.T) {
	root := t.TempDir()
	svc, err := New(root, nil, nil, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	autoSpec := Spec{
		ID:         "auto-policy",
		Name:       "first",
		Steps:      []Step{{Kind: StepPrompt, Prompt: "one"}},
		Unattended: UnattendedAuto,
	}
	if err := SaveSpecs(ports.ScopeProject, root, []Spec{autoSpec}); err != nil {
		t.Fatalf("SaveSpecs (seed): %v", err)
	}

	edited := ports.Automation{
		ID: "auto-policy", Name: "renamed",
		Action: ports.ActionRef{Steps: []ports.ActionStep{{Kind: ports.ActionStepPrompt, Prompt: "two"}}},
	}
	h, err := svc.Apply(context.Background(), ports.ScopeProject, ports.UpsertAutomation{Automation: edited})
	if err != nil {
		t.Fatalf("Apply(Upsert): %v", err)
	}
	if last := drainSave(t, h); last.State != ports.SaveSaved {
		t.Fatalf("Apply(Upsert) final event = %+v, want SaveSaved", last)
	}

	specs, err := LoadSpecs(ports.ScopeProject, root)
	if err != nil {
		t.Fatalf("LoadSpecs after upsert: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("LoadSpecs after upsert = %d entries, want 1", len(specs))
	}
	if specs[0].Unattended != UnattendedAuto {
		t.Fatalf("upsert of an existing automation changed Unattended = %q, want it preserved as %q", specs[0].Unattended, UnattendedAuto)
	}
	if specs[0].Name != "renamed" {
		t.Fatalf("upsert did not apply the edited Name: got %q", specs[0].Name)
	}
}

// corruptAutomationsTOML writes an unparseable automations.toml directly
// at the project-scope path under root, so any subsequent LoadSpecs call
// against that root fails - the deterministic trigger this test file
// uses to reach every LoadSpecs-error branch in service.go
// (Automations/upsert/remove/setEnabled all call LoadSpecs first).
func corruptAutomationsTOML(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(root, ".mivia", automationsFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("not valid toml {{{"), 0o600); err != nil {
		t.Fatalf("write corrupt toml: %v", err)
	}
}

// TestAutomationsReturnsNilOnLoadError covers Automations()' err != nil
// branch: a load failure returns nil rather than propagating the error
// (Service's ports.AutomationSettings.Automations signature has no
// error return, so this is the only contract-compatible response).
func TestAutomationsReturnsNilOnLoadError(t *testing.T) {
	root := t.TempDir()
	corruptAutomationsTOML(t, root)
	svc, err := New(root, nil, nil, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := svc.Automations(); got != nil {
		t.Fatalf("Automations() with a corrupt store = %v, want nil", got)
	}
}

// TestUpsertRemoveSetEnabledSurfaceLoadError covers the three Apply
// branches' shared "specs, err := LoadSpecs(...); if err != nil {
// return err }" guard (upsert, remove, setEnabled), proving a store
// load failure surfaces as a Failed save event rather than a silent
// success or a panic.
func TestUpsertRemoveSetEnabledSurfaceLoadError(t *testing.T) {
	edits := []struct {
		name string
		edit ports.AutomationEdit
	}{
		{"upsert", ports.UpsertAutomation{Automation: ports.Automation{
			ID: "x", Action: ports.ActionRef{Steps: []ports.ActionStep{{Kind: ports.ActionStepPrompt, Prompt: "p"}}},
		}}},
		{"remove", ports.RemoveAutomation{ID: "x"}},
		{"setEnabled", ports.SetAutomationEnabled{ID: "x", On: true}},
	}
	for _, tc := range edits {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			corruptAutomationsTOML(t, root)
			svc, err := New(root, nil, nil, Config{})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			h, err := svc.Apply(context.Background(), ports.ScopeProject, tc.edit)
			if err != nil {
				t.Fatalf("Apply(%s) call: %v", tc.name, err)
			}
			if last := drainSave(t, h); last.State != ports.SaveFailed {
				t.Fatalf("Apply(%s) with a corrupt store final event = %+v, want SaveFailed", tc.name, last)
			}
		})
	}
}
