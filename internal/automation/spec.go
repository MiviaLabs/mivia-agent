// Package automation owns user-defined automations: their TOML
// definitions, their schedules, and their durable run records. It
// depends on cli* packages; nothing in internal/ui* may import it.
//
// This file (spec.go) defines Chunk 1's data shapes only: the on-disk
// Spec, its Step/Trigger/Schedule sub-shapes, and the validation
// primitives (D11 ID charset). No scheduling, execution, or session
// spawning lives here - see docs/design/automations.md's Chunks list.
package automation

import "fmt"

// StepKind names what one automation step runs. Every kind except
// StepWorkflow executes as one turn in the automation's background
// session. Mirrors docs/design/automations.md's API section exactly.
type StepKind int

const (
	StepPrompt   StepKind = iota // Prompt sent verbatim
	StepSkill                    // Ref = skill name, rendered like /<skill>
	StepAgent                    // Ref = agent name; selects it, then sends Prompt
	StepSlash                    // Ref = slash command (headless-safe allowlist only)
	StepWorkflow                 // Ref = workflow name; dispatched to the workflow engine
)

// String renders a StepKind for error messages and test failure output.
func (k StepKind) String() string {
	switch k {
	case StepPrompt:
		return "prompt"
	case StepSkill:
		return "skill"
	case StepAgent:
		return "agent"
	case StepSlash:
		return "slash"
	case StepWorkflow:
		return "workflow"
	default:
		return "unknown"
	}
}

// validStepKind reports whether k is one of the five declared StepKind
// values. Used by store.go's load-time validation to reject an unknown
// kind (an out-of-range int decoded from TOML) rather than silently
// treating it as StepPrompt (the zero value).
func validStepKind(k StepKind) bool {
	return k >= StepPrompt && k <= StepWorkflow
}

// MarshalText renders StepKind as its lowercase name so automations.toml
// stays human-editable ("prompt", not "0"). Satisfies
// encoding.TextMarshaler, which github.com/pelletier/go-toml/v2 honors on
// encode without any opt-in flag (unlike its own Unmarshaler interface,
// gated by EnableUnmarshalerInterface - see internal/config/load.go's
// decodeConfigInto for that distinction).
func (k StepKind) MarshalText() ([]byte, error) {
	if !validStepKind(k) {
		return nil, fmt.Errorf("automation: unknown step kind %d", int(k))
	}
	return []byte(k.String()), nil
}

// UnmarshalText parses a lowercase step-kind name back into StepKind.
// Satisfies encoding.TextUnmarshaler, which go-toml/v2 honors on decode
// automatically.
func (k *StepKind) UnmarshalText(text []byte) error {
	switch string(text) {
	case "prompt":
		*k = StepPrompt
	case "skill":
		*k = StepSkill
	case "agent":
		*k = StepAgent
	case "slash":
		*k = StepSlash
	case "workflow":
		*k = StepWorkflow
	default:
		return fmt.Errorf("automation: unknown step kind %q", string(text))
	}
	return nil
}

// Step is one unit of an automation action. A MIXED action is a Steps
// slice with several kinds: steps run in order in ONE session, and each
// completed index is recorded so a resumed run restarts at index+1.
type Step struct {
	Kind   StepKind          `toml:"kind"`
	Ref    string            `toml:"ref"`
	Prompt string            `toml:"prompt"`
	Inputs map[string]string `toml:"inputs,omitempty"` // StepWorkflow only
}

// WorktreeMode selects where a run executes. BaseRef is first-class
// (R4): "HEAD" or any ref cliworktree can resolve.
type WorktreeMode int

const (
	WorktreeNone WorktreeMode = iota // run in the workspace root
	WorktreeNew                      // create a managed worktree off BaseRef
)

// TriggerKind names how an automation starts. Mirrors
// ports.TriggerKind's two values; automation does not import ports here
// to keep this package's own TOML shape independent of the ports wire
// shape (chunk 2 wires the two together at the Service boundary).
type TriggerKind int

const (
	TriggerManual TriggerKind = iota
	TriggerScheduled
)

// ScheduleKind selects which schedule primitive a scheduled trigger
// uses. Mirrors ports.ScheduleKind.
type ScheduleKind int

const (
	ScheduleInterval ScheduleKind = iota
	ScheduleAt
	ScheduleRecurring
)

// ScheduleSpec is a serialisable schedule, mirroring
// ports.ScheduleSpec's shape. Cron stays a plain string (+TZ) rather
// than a parsed cronschedule.Spec: internal/cronschedule does not exist
// yet (it lands in chunk 4), so this package stores the raw expression
// and timezone without parsing or validating them. Cron validation is
// explicitly deferred to chunk 4.
type ScheduleSpec struct {
	Kind ScheduleKind `toml:"kind"`
	// EverySeconds is the interval in seconds when Kind == ScheduleInterval.
	// A plain int (not time.Duration) keeps the TOML shape simple and
	// matches ports.ScheduleSpec's Every duration only in intent, not wire
	// representation - the adapter at chunk 2's Service boundary converts.
	EverySeconds int64 `toml:"every_seconds"`
	// AtTimes holds RFC3339 timestamps when Kind == ScheduleAt.
	AtTimes []string `toml:"at_times,omitempty"`
	// Cron is the raw cron expression when Kind == ScheduleRecurring.
	// Stored, not parsed: cron validation is chunk 4's job.
	Cron string `toml:"cron"`
	// TZ is the IANA timezone name for Cron. Stored, not parsed.
	TZ string `toml:"tz"`
}

// TriggerSpec is how one automation starts. Schedule is set only when
// Kind is TriggerScheduled.
type TriggerSpec struct {
	Kind     TriggerKind   `toml:"kind"`
	Schedule *ScheduleSpec `toml:"schedule,omitempty"`
}

// UnattendedPolicy is the automation's default-deny approval posture for
// unattended (scheduled or manual-with-no-attached-approver) runs. See
// D8: a scheduled run does not inherit a live session posture.
type UnattendedPolicy string

const (
	// UnattendedDeny is the default: a tool call needing approval fails
	// the step fast rather than hanging until a human attends.
	UnattendedDeny UnattendedPolicy = "deny"
	// UnattendedAuto opts in to auto-approving unattended tool calls,
	// rendered as a warning in the detail view (chunk 3+).
	UnattendedAuto UnattendedPolicy = "auto"
)

// Spec is one user-defined automation's on-disk TOML shape, exactly as
// specified in docs/design/automations.md's API section.
type Spec struct {
	ID          string `toml:"id"`
	Name        string `toml:"name"`
	Description string `toml:"description"`
	Enabled     bool   `toml:"enabled"`

	Trigger TriggerSpec `toml:"trigger"`
	Steps   []Step      `toml:"steps"`

	Worktree WorktreeMode `toml:"worktree"`
	BaseRef  string       `toml:"base_ref"`

	Unattended UnattendedPolicy `toml:"unattended"`
}
