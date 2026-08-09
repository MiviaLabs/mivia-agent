package definition

// WorkflowFile is the on-disk TOML shape for a workflow definition.
type WorkflowFile struct {
	Version     int                 `toml:"version" json:"version,omitempty"`
	Name        string              `toml:"name" json:"name,omitempty"`
	Description string              `toml:"description" json:"description,omitempty"`
	InitialStep string              `toml:"initial_step" json:"initial_step,omitempty"`
	Inputs      map[string]InputDef `toml:"inputs" json:"inputs,omitempty"`
	Limits      Limits              `toml:"limits" json:"limits,omitempty"`
	Steps       []Step              `toml:"steps" json:"steps,omitempty"`
	Transitions []Transition        `toml:"transitions" json:"transitions,omitempty"`
	Delivery    *Delivery           `toml:"delivery" json:"delivery,omitempty"`
}

type InputDef struct {
	Type     string `toml:"type" json:"type,omitempty"`
	Required bool   `toml:"required" json:"required,omitempty"`
	MaxBytes int    `toml:"max_bytes" json:"max_bytes,omitempty"`
}

type Limits struct {
	MaxStepAttempts    int `toml:"max_step_attempts" json:"max_step_attempts,omitempty"`
	MaxDurationSeconds int `toml:"max_duration_seconds" json:"max_duration_seconds,omitempty"`
}

// IsEmpty reports true when both MaxStepAttempts and MaxDurationSeconds are zero,
// which is the state when the [limits] TOML section is omitted.
func (l Limits) IsEmpty() bool {
	return l.MaxStepAttempts == 0 && l.MaxDurationSeconds == 0
}

type Step struct {
	ID    string `toml:"id" json:"id,omitempty"`
	Kind  string `toml:"kind" json:"kind,omitempty"`
	Agent string `toml:"agent" json:"agent,omitempty"`
	// Skill binds an agent step to one named, policy-checked skill.
	// An empty value preserves compatibility with workflows admitted before
	// explicit workflow skill binding.
	Skill        string           `toml:"skill" json:"skill,omitempty"`
	Verifier     string           `toml:"verifier" json:"verifier,omitempty"`
	Command      *StepCommand     `toml:"command" json:"command,omitempty"`
	Template     string           `toml:"template" json:"template,omitempty"`
	OutputSchema string           `toml:"output_schema" json:"output_schema,omitempty"`
	Context      []ContextBinding `toml:"context" json:"context,omitempty"`
	OnFailure    string           `toml:"on_failure" json:"on_failure,omitempty"`
	Panel        *AgentPanel      `toml:"panel" json:"panel,omitempty"`
}

// AgentPanel defines the static members of one agent_panel step.
type AgentPanel struct {
	FailurePolicy           string        `toml:"failure_policy" json:"failure_policy,omitempty"`
	RequireDistinctBindings bool          `toml:"require_distinct_bindings" json:"require_distinct_bindings,omitempty"`
	Members                 []PanelMember `toml:"members" json:"members,omitempty"`
}

// PanelMember defines one statically bound agent in an agent_panel step.
type PanelMember struct {
	ID           string `toml:"id" json:"id,omitempty"`
	Agent        string `toml:"agent" json:"agent,omitempty"`
	Provider     string `toml:"provider" json:"provider,omitempty"`
	Model        string `toml:"model" json:"model,omitempty"`
	Skill        string `toml:"skill" json:"skill,omitempty"`
	Template     string `toml:"template" json:"template,omitempty"`
	OutputSchema string `toml:"output_schema" json:"output_schema,omitempty"`
}

// StepCommand declares one sandboxed command for an evidence_gate step that
// has no named verifier profile. Program must be a bare executable name
// resolved from the trusted system directories; Args are argv passed verbatim
// to the program, never a shell string.
type StepCommand struct {
	Check   string   `toml:"check" json:"check,omitempty"`
	Program string   `toml:"program" json:"program,omitempty"`
	Args    []string `toml:"args" json:"args,omitempty"`
}

type ContextBinding struct {
	From     string `toml:"from" json:"from,omitempty"`
	As       string `toml:"as" json:"as,omitempty"`
	MaxBytes int    `toml:"max_bytes" json:"max_bytes,omitempty"`
	// Optional is true for a steps.<id>.output binding whose prior output may
	// not exist on the first attempt (for example a reviewer step that has not
	// run yet). When the prior output is absent, the controller resolves the
	// binding to an empty string instead of failing. It has no effect on
	// inputs.<name> bindings, which are always present after admission.
	Optional bool `toml:"optional" json:"optional,omitempty"`
	// EnvelopeOnly is true when the bound prior output must reach the step as a
	// ledger reference envelope (artifact pointer + short note) instead of the
	// full inline payload. The controller resolves envelope-only bindings; a
	// step that needs the full artifact must read it back with workflow_inspect.
	EnvelopeOnly bool `toml:"envelope_only" json:"envelope_only,omitempty"`
}

type Transition struct {
	From          string        `toml:"from" json:"from,omitempty"`
	To            string        `toml:"to" json:"to,omitempty"`
	Match         MatchCriteria `toml:"match" json:"match,omitempty"`
	Loop          string        `toml:"loop" json:"loop,omitempty"`
	MaxIterations int           `toml:"max_iterations" json:"max_iterations,omitempty"`
}

type MatchCriteria struct {
	Status string            `toml:"status" json:"status,omitempty"`
	Output map[string]string `toml:"output" json:"output,omitempty"`
}

type Delivery struct {
	Kind                  string `toml:"kind" json:"kind,omitempty"`
	Mode                  string `toml:"mode" json:"mode,omitempty"`
	Provider              string `toml:"provider" json:"provider,omitempty"`
	Base                  string `toml:"base" json:"base,omitempty"`
	TitleTemplate         string `toml:"title_template" json:"title_template,omitempty"`
	CommitMessageTemplate string `toml:"commit_message_template" json:"commit_message_template,omitempty"`
	MaxTitleBytes         int    `toml:"max_title_bytes" json:"max_title_bytes,omitempty"`
	MaxCommitMessageBytes int    `toml:"max_commit_message_bytes" json:"max_commit_message_bytes,omitempty"`
	// OnFailure names the step to re-enter when delivery fails for a reason an
	// agent can repair, for example a commit hook that rejects the change.
	//
	// Delivery runs after the success terminal, outside the step graph, so a
	// delivery failure had no route back into the workflow: the run stopped
	// and waited for a person. With this set, the run returns to the named
	// step, the agents fix the cause, the run reaches success again, and
	// delivery runs again.
	//
	// The field names a step, not a reason. Which step repairs which failure
	// is the workflow author's choice, so this stays generic.
	//
	// Empty keeps the old behavior: the run holds for a person.
	OnFailure string `toml:"on_failure" json:"on_failure,omitempty"`
	// PRTitlePolicy is the relative path to the project PR-title policy file.
	// The path is relative to the workflow directory. An empty value selects
	// the default policy at .mivia/policy/pr-title.toml.
	PRTitlePolicy string `toml:"pr_title_policy" json:"pr_title_policy,omitempty"`
	// OnPRMetadataFailure names the step that repairs PR-metadata delivery
	// failures. PR metadata is the pull-request title and summary. An empty
	// value makes the run use OnFailure for PR-metadata failures.
	OnPRMetadataFailure string `toml:"on_pr_metadata_failure" json:"on_pr_metadata_failure,omitempty"`
}

// DiscoveredWorkflow is the result of discovering a workflow file.
type DiscoveredWorkflow struct {
	Name string
	Path string
	Raw  []byte
}

// ValidStepKinds enumerates the allowed step kind values.
var ValidStepKinds = map[string]bool{
	"agent":         true,
	"agent_panel":   true,
	"agent_gate":    true,
	"evidence_gate": true,
	"human_gate":    true,
}

// ReservedStepIDs are terminal state names that cannot be used as step IDs.
var ReservedStepIDs = map[string]bool{
	"success": true,
	"failure": true,
}

// UnlimitedIterations is the sentinel value for MaxIterations indicating no loop bound.
// Users must explicitly set max_iterations = -1 to opt in; omitting the field (zero) is rejected.
const UnlimitedIterations = -1

// MaxWorkflowFileBytes is the maximum allowed size for a single workflow TOML file.
const MaxWorkflowFileBytes = 65536

// MaxInputBytes is the maximum allowed max_bytes value for a single input definition.
const MaxInputBytes = 1048576

// MaxEvidenceBindingBytes is the maximum bytes of a prior step output bound
// into a later step context.
const MaxEvidenceBindingBytes = 32 << 10
