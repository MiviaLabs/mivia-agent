package ports

import (
	"context"
	"time"
)

// TriggerKind names how an automation starts. Only two exist today,
// deliberately: mivia-ai-sdk/trigger has no kind enum at all (it ships
// name-keyed condition/action pairs), and this screen is the first
// caller to scope one. See docs/design/settings-screen.md §12.
type TriggerKind int

const (
	TriggerManual TriggerKind = iota
	TriggerScheduled
)

// ScheduleKind selects which of mivia-ai-sdk/scheduler's two schedule
// primitives - or the cross-platform recurring schedule this repo adds
// in internal/cronschedule (settings-screen.md §14) - an automation
// uses.
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

// ActionRef names what an automation runs. A workflow is the only
// action kind mivia-agent has today (internal/workflows/definition);
// this is a struct rather than a bare string so a second action kind
// can be added as a field, not a breaking type change.
type ActionRef struct {
	Workflow string
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
)

// RunFailKind classifies a failed run without echoing the SDK's raw
// error text: scheduler.JobFailedEvent's Data is an unparsed
// fmt.Sprintf string with no structure, which is tainted under the
// same rule MCPFailKind follows. See settings-screen.md §12.5.
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
// screen defines; see settings-screen.md §12.
type Automation struct {
	ID          string
	Name        string
	Description string
	Enabled     bool
	Trigger     TriggerSpec
	Action      ActionRef
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

func (UpsertAutomation) isAutomationEdit()     {}
func (RemoveAutomation) isAutomationEdit()     {}
func (SetAutomationEnabled) isAutomationEdit() {}
func (TriggerAutomation) isAutomationEdit()    {}

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
