package ports

import (
	"context"
	"time"
)

// TriggerKind names how an automation starts. Only two exist today,
// deliberately: mivia-ai-sdk/trigger has no kind enum at all (it ships
// name-keyed condition/action pairs), and this screen is the first
// caller to scope one.
type TriggerKind int

const (
	TriggerManual TriggerKind = iota
	TriggerScheduled
)

// ScheduleKind selects which of mivia-ai-sdk/scheduler's two schedule
// primitives - or the cross-platform recurring schedule this repo adds
// in internal/cronschedule - an automation uses.
type ScheduleKind int

const (
	ScheduleInterval ScheduleKind = iota
	ScheduleAt
	ScheduleRecurring
)

// ScheduleSpec is a serialisable schedule, unlike
// mivia-ai-sdk/scheduler.Schedule, which is an opaque interface with
// unexported implementations and no JSON shape. The adapter converts
// this into scheduler.Every, scheduler.At, or
// internal/cronschedule.Parse(Cron, TZ) - all three satisfy the same
// scheduler.Schedule interface, so nothing about Scheduler.Add changes
// when a new ScheduleKind is added.
//
// Cron stays plain text here rather than internal/cronschedule.Spec so
// this package never depends on that one; the adapter is the only code
// that parses it.
type ScheduleSpec struct {
	Kind  ScheduleKind
	Every time.Duration
	At    []time.Time
	Cron  string
	TZ    string
}

// TriggerSpec is how one automation starts. Schedule is set only when
// Kind is TriggerScheduled.
type TriggerSpec struct {
	Kind     TriggerKind
	Schedule *ScheduleSpec
}

// ActionRef names what an automation runs. Workflow is the original,
// single-workflow-only shape (kept as a compat alias: setting it alone
// is still a legal ActionRef, equivalent to a single StepWorkflow step).
// Steps is the ordered multi-kind action list internal/automation's Spec
// carries; ActionStepKind mirrors automation.StepKind's five values
// without this leaf package importing that package (internal/automation
// imports ports, never the reverse).
type ActionRef struct {
	Workflow string
	Steps    []ActionStep
}

// ActionStepKind names what one ActionStep runs. Mirrors
// automation.StepKind's five values exactly; kept as its own type here
// so ports stays a leaf (no import of internal/automation).
type ActionStepKind int

const (
	ActionStepPrompt ActionStepKind = iota
	ActionStepSkill
	ActionStepAgent
	ActionStepSlash
	ActionStepWorkflow
)

// ActionStep is one unit of a multi-kind automation action, mirroring
// automation.Step's shape.
type ActionStep struct {
	Kind   ActionStepKind
	Ref    string
	Prompt string
	Inputs map[string]string // ActionStepWorkflow only
}

// UnattendedPolicy names an automation's approval posture for
// unattended (scheduled or manual-with-no-attached-approver) runs.
// Mirrors automation.UnattendedPolicy's two values exactly; kept as its
// own type here so ports stays a leaf (no import of
// internal/automation), matching ActionStepKind's precedent above.
type UnattendedPolicy int

const (
	UnattendedPolicyDeny UnattendedPolicy = iota // zero value = safe default
	UnattendedPolicyAuto
)

// WorktreeSpec selects where an automation's run executes. Mode mirrors
// automation.WorktreeMode's two values (0 = run in place, 1 = create a
// managed worktree off BaseRef) without importing that package.
type WorktreeSpec struct {
	Mode    int
	BaseRef string
}

// RunState is where one automation run has reached. RunCancelled is
// ours: mivia-ai-sdk's scheduler has no cancellation and its ledger's
// status set has no cancelled/timed-out state either. Including it now
// is cheap; retrofitting a state into an enum callers already switch
// on is not.
type RunState int

const (
	RunPending RunState = iota
	RunRunning
	RunSucceeded
	RunFailed
	RunCancelled
	// RunInterrupted marks a run left in RunRunning when its fenced
	// claim (D7) is found expired at service start or sweep time (D13):
	// the process holding it died or was killed without ending the run.
	// Distinct from RunFailed - an interrupted run is resumable via
	// ResumeAutomationRun (chunk 8), not a terminal failure requiring a
	// fresh run.
	RunInterrupted
	// RunSkipped marks a fire that lost the fenced single-fire claim
	// (D7): another fire already owns the automation's in-flight run,
	// so this fire is a documented no-op, recorded as its own run row
	// rather than silently vanishing.
	RunSkipped
)

// RunFailKind classifies a failed run without echoing the SDK's raw
// error text: scheduler.JobFailedEvent's Data is an unparsed
// fmt.Sprintf string with no structure, which is tainted under the
// same rule MCPFailKind follows.
type RunFailKind int

const (
	RunFailNone RunFailKind = iota
	RunFailJobError
	RunFailConditionNotMet
	RunFailTimeout
)

// RunSummary is the compact form Automation.LastRun carries, so listing
// automations does not require a Runs() call per row.
type RunSummary struct {
	ID        string
	State     RunState
	StartedAt time.Time
}

// Run is one automation execution.
type Run struct {
	ID           string
	AutomationID string
	Trigger      TriggerKind
	State        RunState
	StartedAt    time.Time
	EndedAt      *time.Time
	FailKind     RunFailKind
	Message      string
}

// Automation is one user-defined automation. mivia-ai-sdk's scheduler
// has no entity like this at all (scheduler.Job is a bare closure with
// only a string id) - this type and its store are the domain model this
// screen defines.
type Automation struct {
	ID          string
	Name        string
	Description string
	Enabled     bool
	Trigger     TriggerSpec
	Action      ActionRef
	Worktree    WorktreeSpec
	Unattended  UnattendedPolicy
	LastRun     *RunSummary
	NextFire    *time.Time
	Scope       Scope
}

// AutomationEdit is a closed union of automation mutations.
type AutomationEdit interface{ isAutomationEdit() }

type UpsertAutomation struct{ Automation Automation }
type RemoveAutomation struct{ ID string }
type SetAutomationEnabled struct {
	ID string
	On bool
}
type TriggerAutomation struct{ ID string }

// ResumeAutomationRun resumes a run left interrupted (D13): it restarts
// at step_index+1 of a saved session. Actual resume execution is a
// later chunk (chunk 8); the Apply implementation in this chunk returns
// a named "not yet implemented" error rather than silently dropping the
// edit, so a caller wiring this variant learns the gap immediately
// instead of watching a no-op succeed.
type ResumeAutomationRun struct{ RunID string }

// CancelAutomationRun stops a run that is still RunPending or
// RunRunning. Unlike ResumeAutomationRun, the in-memory store
// (internal/uiadapter) handles this directly: cancelling a run needs
// no saved session state to resume from, only a state transition.
type CancelAutomationRun struct{ RunID string }

func (UpsertAutomation) isAutomationEdit()     {}
func (RemoveAutomation) isAutomationEdit()     {}
func (SetAutomationEnabled) isAutomationEdit() {}
func (TriggerAutomation) isAutomationEdit()    {}
func (ResumeAutomationRun) isAutomationEdit()  {}
func (CancelAutomationRun) isAutomationEdit()  {}

// RunHandle streams one automation's runs as they happen - live-run
// state, the same channel convention as TurnHandle and SaveHandle, so
// the UI has one async shape rather than a third.
type RunHandle interface {
	Events() <-chan Run
	Cancel()
}

// AutomationSettings is the Automations section's read/write surface.
// The store, not mivia-ai-sdk's Scheduler, is the source of truth for
// the automation list and run history: Scheduler.entries is
// unexported and unenumerable, and a fired one-shot schedule is deleted
// from it with no trace it ever existed (scheduler/run.go:131).
type AutomationSettings interface {
	Automations() []Automation
	Runs(automationID string, limit int) []Run
	Run(runID string) (Run, bool)
	Apply(ctx context.Context, scope Scope, e AutomationEdit) (SaveHandle, error)
	Watch(ctx context.Context, automationID string) (RunHandle, error)
}
