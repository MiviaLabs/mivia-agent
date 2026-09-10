// Package automation owns user-defined automations: their TOML
// definitions, their schedules, and their durable run records. It
// depends on cli* packages; nothing in internal/ui* may import it.
//
// This file (service.go) is Chunk 2's wiring shape: Service satisfies
// ports.AutomationSettings so the settings UI reaches it only through
// that interface (see docs/design/automations.md D4). No scheduling,
// claim/lifecycle handling, or executor lives here yet - Runs/Run
// return empty/zero rather than fabricate anything, and Apply's
// TriggerAutomation/ResumeAutomationRun variants return named
// "not yet implemented" errors rather than silently no-op.
package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// SessionSpawner is the capability set the executor needs from the
// session pool. The parameter types are PLAIN funcs, never
// uiadapter.BindFunc: a composition-root adapter in internal/newtui
// converts (see docs/design/automations.md D4's compile-hazard note).
// This package must never import internal/uiadapter.
//
// SetApprovalOverride is chunk 6's addition: after CreateFreshInDir
// returns (never inside the bind closure - see D8 and
// uiadapter.SetApprovalOverride's own doc comment on the clobber
// ordering), the executor installs the automation's unattended
// approval posture (DenyGate/AutoApproveGate) directly on the spawned
// session, keyed by its conversation id.
type SessionSpawner interface {
	CreateFreshInDir(bind func(*chat.Session) (string, error), dir string) (ports.Conversation, error)
	SetApprovalOverride(sessionID string, gate func(ctx context.Context, name string, args json.RawMessage) sdkadapter.ApprovalResult, policy string) error
}

// Config holds the executor's tunables. TurnTimeout bounds every
// headless-driven turn (see D5); zero means headless.go's caller must
// supply an explicit timeout - runTurnHeadless does not default it.
type Config struct {
	TurnTimeout time.Duration
}

// Service is the automation backend. It satisfies
// ports.AutomationSettings, which is how the settings UI reaches it
// without any UI package importing this one (D4).
type Service struct {
	root  string
	db    *storage.SQLite
	spawn SessionSpawner
	cfg   Config
}

// New builds a Service rooted at workspace root, optionally backed by
// db (may be nil - run persistence lands in a later chunk) and spawn
// (may be nil until the executor, chunk 6, needs it). Rejects an empty
// root: Automations()/Apply() always resolve automations.toml at
// ports.ScopeProject (scopeForLoad below), and automationsFilePath
// itself already refuses an empty workspaceRoot for that scope
// (store.go) - a Service built with one could never load or save
// anything, so failing fast here is more honest than a Service that
// silently does nothing.
func New(root string, db *storage.SQLite, spawn SessionSpawner, cfg Config) (*Service, error) {
	if root == "" {
		return nil, fmt.Errorf("automation: New requires a non-empty workspace root")
	}
	return &Service{root: root, db: db, spawn: spawn, cfg: cfg}, nil
}

// Compile-time proof of the inverted dependency (D4): automation
// imports ports and implements AutomationSettings; ports never imports
// automation.
var _ ports.AutomationSettings = (*Service)(nil)

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
			Every: time.Duration(t.Schedule.EverySeconds) * time.Second,
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
	}
}

// automationToSpec maps a ports.Automation edit value back to
// automation.Spec, preserving Unattended default-deny (D8): a ports
// value carries no unattended field, so an upsert through this path
// always lands UnattendedDeny. Widening ports.Automation to carry it
// is out of this chunk's scope.
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
		Unattended:  UnattendedDeny,
	}
}

// scopeForLoad is the scope Automations()/Apply() read/write from. Chunk
// 2 always uses ports.ScopeProject: the service is constructed with one
// workspace root (see New's root parameter) and has no user-scope
// resolution wired yet. Widening to read/merge both scopes is a later
// chunk's job (D1 already documents user+project as the eventual model).
const scopeForLoad = ports.ScopeProject

// Automations returns every defined automation, mapped from the store's
// on-disk Spec shape.
func (s *Service) Automations() []ports.Automation {
	specs, err := LoadSpecs(scopeForLoad, s.root)
	if err != nil {
		return nil
	}
	out := make([]ports.Automation, 0, len(specs))
	for _, spec := range specs {
		out = append(out, specToAutomation(spec))
	}
	return out
}

// Runs returns automationID's run history via the runstore, most
// recently started first, mapped to ports.Run. A store error (including
// "no run store configured" on a nil db) is treated as empty rather than
// propagated: ports.AutomationSettings.Runs has no error return, matching
// this method's pre-chunk-5 unconditional-empty contract for an
// unconfigured/misbehaving store.
func (s *Service) Runs(automationID string, limit int) []ports.Run {
	runs, err := s.listRuns(context.Background(), automationID, limit)
	if err != nil || len(runs) == 0 {
		return nil
	}
	out := make([]ports.Run, 0, len(runs))
	for _, r := range runs {
		out = append(out, runToPorts(r))
	}
	return out
}

// Run looks up one run by id via the runstore, mapped to ports.Run.
func (s *Service) Run(runID string) (ports.Run, bool) {
	r, ok, err := s.getRun(context.Background(), runID)
	if err != nil || !ok {
		return ports.Run{}, false
	}
	return runToPorts(r), true
}

// Apply handles the automation-definition edits this chunk can honor
// (Upsert/Remove/SetEnabled) via a load-modify-save round trip through
// the store, and returns a clear "not yet implemented" error for the
// two edits that need an executor this chunk does not have
// (TriggerAutomation needs chunk 6; ResumeAutomationRun needs chunk 8).
func (s *Service) Apply(ctx context.Context, scope ports.Scope, e ports.AutomationEdit) (ports.SaveHandle, error) {
	switch v := e.(type) {
	case ports.UpsertAutomation:
		return s.runSaveHandle(func() error { return s.upsert(v.Automation) }), nil
	case ports.RemoveAutomation:
		return s.runSaveHandle(func() error { return s.remove(v.ID) }), nil
	case ports.SetAutomationEnabled:
		return s.runSaveHandle(func() error { return s.setEnabled(v.ID, v.On) }), nil
	case ports.TriggerAutomation:
		return s.runSaveHandle(func() error { _, err := s.RunOnce(ctx, v.ID, ports.TriggerManual); return err }), nil
	case ports.ResumeAutomationRun:
		return nil, fmt.Errorf("automation: resume not yet implemented until chunk 8")
	default:
		return nil, fmt.Errorf("automation: unknown edit %T", e)
	}
}

func (s *Service) upsert(a ports.Automation) error {
	specs, err := LoadSpecs(scopeForLoad, s.root)
	if err != nil {
		return err
	}
	spec := automationToSpec(a)
	if err := ValidateSpec(spec, nil); err != nil {
		return err
	}
	found := false
	for i := range specs {
		if specs[i].ID == spec.ID {
			specs[i] = spec
			found = true
			break
		}
	}
	if !found {
		specs = append(specs, spec)
	}
	return SaveSpecs(scopeForLoad, s.root, specs)
}

func (s *Service) remove(id string) error {
	specs, err := LoadSpecs(scopeForLoad, s.root)
	if err != nil {
		return err
	}
	out := specs[:0]
	found := false
	for _, spec := range specs {
		if spec.ID == id {
			found = true
			continue
		}
		out = append(out, spec)
	}
	if !found {
		return fmt.Errorf("automation %q not found", id)
	}
	return SaveSpecs(scopeForLoad, s.root, out)
}

func (s *Service) setEnabled(id string, on bool) error {
	specs, err := LoadSpecs(scopeForLoad, s.root)
	if err != nil {
		return err
	}
	found := false
	for i := range specs {
		if specs[i].ID == id {
			specs[i].Enabled = on
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("automation %q not found", id)
	}
	return SaveSpecs(scopeForLoad, s.root, specs)
}

// saveHandle is a synchronous ports.SaveHandle: apply runs immediately
// and the whole event sequence (Pending -> Validating -> Saved|Failed)
// is queued onto a small buffered channel before returning, mirroring
// internal/uiadapter's own saveHandle shape (settings.go's
// newSaveHandle) without importing that package.
type saveHandle struct {
	id     string
	events chan ports.SaveEvent
}

func (h *saveHandle) ID() string                     { return h.id }
func (h *saveHandle) Events() <-chan ports.SaveEvent { return h.events }
func (h *saveHandle) Cancel()                        {}

func (s *Service) runSaveHandle(apply func() error) ports.SaveHandle {
	ch := make(chan ports.SaveEvent, 4)
	ch <- ports.SaveEvent{State: ports.SavePending}
	ch <- ports.SaveEvent{State: ports.SaveValidating}
	if err := apply(); err != nil {
		ch <- ports.SaveEvent{State: ports.SaveFailed, Message: err.Error()}
	} else {
		ch <- ports.SaveEvent{State: ports.SaveSaved}
	}
	close(ch)
	return &saveHandle{id: "automation-save", events: ch}
}

// runHandle is a no-op ports.RunHandle: Watch on a run-less automation is
// not a failure (Watch-with-no-runs, plan Tests section), so Events()
// returns a channel that is immediately closed rather than one that
// blocks forever or is fabricated with synthetic runs.
type runHandle struct {
	ch chan ports.Run
}

func (h *runHandle) Events() <-chan ports.Run { return h.ch }
func (h *runHandle) Cancel()                  {}

// Watch returns a handle whose Events() channel is closed immediately:
// no run execution exists yet in this chunk, so nothing is ever
// fabricated and nothing is ever an error.
func (s *Service) Watch(ctx context.Context, automationID string) (ports.RunHandle, error) {
	ch := make(chan ports.Run)
	close(ch)
	return &runHandle{ch: ch}, nil
}
