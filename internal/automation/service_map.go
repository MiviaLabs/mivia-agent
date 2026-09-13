// Package automation owns user-defined automations: their TOML
// definitions, their schedules, and their durable run records.
//
// This file (service_map.go) holds the pure value mappers between this
// package's storage-shaped types and their ports views. No I/O lives
// here.
package automation

import (
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// stepKindToPorts maps automation.StepKind to ports.ActionStepKind.
func stepKindToPorts(k StepKind) ports.ActionStepKind {
	switch k {
	case StepPrompt:
		return ports.ActionStepPrompt
	case StepSkill:
		return ports.ActionStepSkill
	case StepAgent:
		return ports.ActionStepAgent
	case StepSlash:
		return ports.ActionStepSlash
	case StepWorkflow:
		return ports.ActionStepWorkflow
	default:
		return ports.ActionStepPrompt
	}
}

// stepKindFromPorts maps ports.ActionStepKind back to automation.StepKind.
func stepKindFromPorts(k ports.ActionStepKind) StepKind {
	switch k {
	case ports.ActionStepPrompt:
		return StepPrompt
	case ports.ActionStepSkill:
		return StepSkill
	case ports.ActionStepAgent:
		return StepAgent
	case ports.ActionStepSlash:
		return StepSlash
	case ports.ActionStepWorkflow:
		return StepWorkflow
	default:
		return StepPrompt
	}
}

// unattendedToPorts maps automation.UnattendedPolicy to
// ports.UnattendedPolicy.
func unattendedToPorts(u UnattendedPolicy) ports.UnattendedPolicy {
	if u == UnattendedAuto {
		return ports.UnattendedPolicyAuto
	}
	return ports.UnattendedPolicyDeny
}

// unattendedFromPorts maps ports.UnattendedPolicy back to
// automation.UnattendedPolicy. Deliberately asymmetric with
// unattendedToPorts: any out-of-range/unknown ports.UnattendedPolicy
// value (not just the zero value) collapses to UnattendedDeny, the
// fail-safe default direction - an unrecognized policy must never be
// interpreted as the more permissive UnattendedAuto.
func unattendedFromPorts(p ports.UnattendedPolicy) UnattendedPolicy {
	if p == ports.UnattendedPolicyAuto {
		return UnattendedAuto
	}
	return UnattendedDeny
}

// triggerKindToPorts maps automation.TriggerKind to ports.TriggerKind.
func triggerKindToPorts(k TriggerKind) ports.TriggerKind {
	if k == TriggerScheduled {
		return ports.TriggerScheduled
	}
	return ports.TriggerManual
}

// triggerKindFromPorts maps ports.TriggerKind back to automation.TriggerKind.
func triggerKindFromPorts(k ports.TriggerKind) TriggerKind {
	if k == ports.TriggerScheduled {
		return TriggerScheduled
	}
	return TriggerManual
}

// scheduleKindToPorts maps automation.ScheduleKind to ports.ScheduleKind.
func scheduleKindToPorts(k ScheduleKind) ports.ScheduleKind {
	switch k {
	case ScheduleAt:
		return ports.ScheduleAt
	case ScheduleRecurring:
		return ports.ScheduleRecurring
	default:
		return ports.ScheduleInterval
	}
}

// scheduleKindFromPorts maps ports.ScheduleKind back to automation.ScheduleKind.
func scheduleKindFromPorts(k ports.ScheduleKind) ScheduleKind {
	switch k {
	case ports.ScheduleAt:
		return ScheduleAt
	case ports.ScheduleRecurring:
		return ScheduleRecurring
	default:
		return ScheduleInterval
	}
}

// runStateToPorts maps automation.RunState to ports.RunState.
func runStateToPorts(st RunState) ports.RunState {
	switch st {
	case RunRunning:
		return ports.RunRunning
	case RunSucceeded:
		return ports.RunSucceeded
	case RunFailed:
		return ports.RunFailed
	case RunCancelled:
		return ports.RunCancelled
	case RunInterrupted:
		return ports.RunInterrupted
	case RunSkipped:
		return ports.RunSkipped
	default:
		return ports.RunPending
	}
}

// runFailKindToPorts maps automation.RunFailKind to ports.RunFailKind.
func runFailKindToPorts(k RunFailKind) ports.RunFailKind {
	switch k {
	case RunFailJobError:
		return ports.RunFailJobError
	case RunFailConditionNotMet:
		return ports.RunFailConditionNotMet
	case RunFailTimeout:
		return ports.RunFailTimeout
	default:
		return ports.RunFailNone
	}
}

// runOriginToTrigger maps a run's stored Origin string ("manual" or
// "scheduled") to ports.TriggerKind. Any other/unset value maps to
// TriggerManual, the zero value - matching triggerKindToPorts's own
// default-branch convention for an out-of-range/unknown input.
func runOriginToTrigger(origin string) ports.TriggerKind {
	if origin == "scheduled" {
		return ports.TriggerScheduled
	}
	return ports.TriggerManual
}

// runToPorts maps one runstore Run to its ports.Run view.
func runToPorts(r Run) ports.Run {
	return ports.Run{
		ID:           r.ID,
		AutomationID: r.AutomationID,
		Trigger:      runOriginToTrigger(r.Origin),
		State:        runStateToPorts(r.State),
		StartedAt:    r.StartedAt,
		EndedAt:      r.EndedAt,
		FailKind:     runFailKindToPorts(r.FailKind),
		Message:      r.Message,
	}
}

// specToPortsTrigger maps automation.TriggerSpec to ports.TriggerSpec.
func specToPortsTrigger(t TriggerSpec) ports.TriggerSpec {
	out := ports.TriggerSpec{Kind: triggerKindToPorts(t.Kind)}
	if t.Schedule != nil {
		out.Schedule = &ports.ScheduleSpec{
			Kind:  scheduleKindToPorts(t.Schedule.Kind),
			Every: saturatingSeconds(t.Schedule.EverySeconds),
			Cron:  t.Schedule.Cron,
			TZ:    t.Schedule.TZ,
		}
		for _, raw := range t.Schedule.AtTimes {
			if ts, err := time.Parse(time.RFC3339, raw); err == nil {
				out.Schedule.At = append(out.Schedule.At, ts)
			}
		}
	}
	return out
}

// portsTriggerToSpec maps ports.TriggerSpec back to automation.TriggerSpec.
func portsTriggerToSpec(t ports.TriggerSpec) TriggerSpec {
	out := TriggerSpec{Kind: triggerKindFromPorts(t.Kind)}
	if t.Schedule != nil {
		sched := &ScheduleSpec{
			Kind:         scheduleKindFromPorts(t.Schedule.Kind),
			EverySeconds: int64(t.Schedule.Every / time.Second),
			Cron:         t.Schedule.Cron,
			TZ:           t.Schedule.TZ,
		}
		for _, ts := range t.Schedule.At {
			sched.AtTimes = append(sched.AtTimes, ts.Format(time.RFC3339))
		}
		out.Schedule = sched
	}
	return out
}

// specToAutomation maps one automation.Spec to its ports.Automation view.
func specToAutomation(spec Spec) ports.Automation {
	steps := make([]ports.ActionStep, 0, len(spec.Steps))
	for _, st := range spec.Steps {
		steps = append(steps, ports.ActionStep{
			Kind:   stepKindToPorts(st.Kind),
			Ref:    st.Ref,
			Prompt: st.Prompt,
			Inputs: st.Inputs,
		})
	}
	worktreeMode := 0
	if spec.Worktree == WorktreeNew {
		worktreeMode = 1
	}
	return ports.Automation{
		ID:          spec.ID,
		Name:        spec.Name,
		Description: spec.Description,
		Enabled:     spec.Enabled,
		Trigger:     specToPortsTrigger(spec.Trigger),
		Action:      ports.ActionRef{Steps: steps},
		Worktree:    ports.WorktreeSpec{Mode: worktreeMode, BaseRef: spec.BaseRef},
		Unattended:  unattendedToPorts(spec.Unattended),
	}
}

// automationToSpec maps a ports.Automation edit value back to
// automation.Spec. Unattended maps through unattendedFromPorts, which
// collapses any unrecognized ports.UnattendedPolicy value to
// UnattendedDeny (see that function's doc comment) - callers that
// deliberately omit the field (leaving it at its ports zero value) get
// the safe default, but the UI always sends the real current value end
// to end (see automations_editor.go), so upsert of an existing
// automation through the normal editor path correctly carries the true
// policy rather than silently defaulting it.
func automationToSpec(a ports.Automation) Spec {
	steps := make([]Step, 0, len(a.Action.Steps))
	for _, st := range a.Action.Steps {
		steps = append(steps, Step{
			Kind:   stepKindFromPorts(st.Kind),
			Ref:    st.Ref,
			Prompt: st.Prompt,
			Inputs: st.Inputs,
		})
	}
	// Compat: a bare ActionRef.Workflow with no Steps is a single
	// StepWorkflow step (D's "ActionRef.Workflow stays a compat alias").
	if len(steps) == 0 && a.Action.Workflow != "" {
		steps = append(steps, Step{Kind: StepWorkflow, Ref: a.Action.Workflow})
	}
	worktreeMode := WorktreeNone
	if a.Worktree.Mode != 0 {
		worktreeMode = WorktreeNew
	}
	return Spec{
		ID:          a.ID,
		Name:        a.Name,
		Description: a.Description,
		Enabled:     a.Enabled,
		Trigger:     portsTriggerToSpec(a.Trigger),
		Steps:       steps,
		Worktree:    worktreeMode,
		BaseRef:     a.Worktree.BaseRef,
		Unattended:  unattendedFromPorts(a.Unattended),
	}
}
