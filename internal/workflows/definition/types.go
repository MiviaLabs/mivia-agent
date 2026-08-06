package definition

// WorkflowFile is the on-disk TOML shape for a workflow definition.
type WorkflowFile struct {
	Version     int                 `toml:"version"`
	Name        string              `toml:"name"`
	Description string              `toml:"description"`
	InitialStep string              `toml:"initial_step"`
	Inputs      map[string]InputDef `toml:"inputs"`
	Limits      Limits              `toml:"limits"`
	Steps       []Step              `toml:"steps"`
	Transitions []Transition        `toml:"transitions"`
	Delivery    *Delivery           `toml:"delivery"`
}

type InputDef struct {
	Type     string `toml:"type"`
	Required bool   `toml:"required"`
	MaxBytes int    `toml:"max_bytes"`
}

type Limits struct {
	MaxStepAttempts    int `toml:"max_step_attempts"`
	MaxDurationSeconds int `toml:"max_duration_seconds"`
}

type Step struct {
	ID    string `toml:"id"`
	Kind  string `toml:"kind"`
	Agent string `toml:"agent"`
	// Skill binds an agent step to one named, policy-checked skill.
	// An empty value preserves compatibility with workflows admitted before
	// explicit workflow skill binding.
	Skill        string           `toml:"skill"`
	Verifier     string           `toml:"verifier"`
	Template     string           `toml:"template"`
	OutputSchema string           `toml:"output_schema"`
	Context      []ContextBinding `toml:"context"`
	OnFailure    string           `toml:"on_failure"`
}

type ContextBinding struct {
	From     string `toml:"from"`
	As       string `toml:"as"`
	MaxBytes int    `toml:"max_bytes"`
	// Optional is true for a steps.<id>.output binding whose prior output may
	// not exist on the first attempt (for example a reviewer step that has not
	// run yet). When the prior output is absent, the controller resolves the
	// binding to an empty string instead of failing. It has no effect on
	// inputs.<name> bindings, which are always present after admission.
	Optional bool `toml:"optional"`
}

type Transition struct {
	From          string        `toml:"from"`
	To            string        `toml:"to"`
	Match         MatchCriteria `toml:"match"`
	Loop          string        `toml:"loop"`
	MaxIterations int           `toml:"max_iterations"`
}

type MatchCriteria struct {
	Status string            `toml:"status"`
	Output map[string]string `toml:"output"`
}

type Delivery struct {
	Kind                  string `toml:"kind"`
	Mode                  string `toml:"mode"`
	Provider              string `toml:"provider"`
	Base                  string `toml:"base"`
	TitleTemplate         string `toml:"title_template"`
	CommitMessageTemplate string `toml:"commit_message_template"`
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
