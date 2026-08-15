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
	Stacking    *Stacking           `toml:"stacking" json:"stacking,omitempty"`
	// StepDefaults is decode-time sugar: ParseWorkflowTOML copies each
	// non-empty field into every agent/agent_panel step whose own field is
	// empty, then clears this to nil. It never reaches the compiler or the
	// digest - the json:"-" tag keeps a sugared file and its hand-expanded
	// twin byte-identical after json.Marshal, so they compile to the same
	// digest.
	StepDefaults *StepDefaults `toml:"step_defaults" json:"-"`
}

// StepDefaults holds shared step field values applied at decode time to
// every step whose resolved kind is "agent" or "agent_panel" and whose own
// field is empty (for agent_panel this fills only the step's top-level
// synthesis fields, never per-member PanelMember entries). Only Kind is
// considered for the other kinds (agent_gate, evidence_gate, human_gate).
// See applyStepDefaults.
type StepDefaults struct {
	Kind         string           `toml:"kind" json:"-"`
	Agent        string           `toml:"agent" json:"-"`
	Skill        string           `toml:"skill" json:"-"`
	Template     string           `toml:"template" json:"-"`
	OutputSchema string           `toml:"output_schema" json:"-"`
	OnFailure    string           `toml:"on_failure" json:"-"`
	MaxTurns     int              `toml:"max_turns" json:"-"`
	Context      []ContextBinding `toml:"context" json:"-"`
}

type InputDef struct {
	Type     string `toml:"type" json:"type,omitempty"`
	Required bool   `toml:"required" json:"required,omitempty"`
	MaxBytes int    `toml:"max_bytes" json:"max_bytes,omitempty"`
}

type Limits struct {
	MaxStepAttempts    int `toml:"max_step_attempts" json:"max_step_attempts,omitempty"`
	MaxDurationSeconds int `toml:"max_duration_seconds" json:"max_duration_seconds,omitempty"`
	// MaxOnFailureReentries bounds how many times ONE step may re-enter its
	// declared non-terminal on_failure (repair) target after genuine
	// failures: agent steps, agent_panel steps, and evidence_gate host
	// failures all spend this budget, counted per step. 0 means the
	// controller default (3); negative values are rejected by the compiler.
	// The budget is a safety net, not a tuning dial: the compiler accepts
	// on_failure cycles, so without it a workflow whose author declared a
	// repair cycle would spin to the run deadline.
	MaxOnFailureReentries int `toml:"max_on_failure_reentries" json:"max_on_failure_reentries,omitempty"`
	// MaxTransientStepRetries bounds step-level retries of transient
	// LLM-provider failures (overload, rate limit, upstream 5xx) within one
	// attempt, each retry re-running the whole step with a fresh task
	// identity. 0 means the controller default (3); negative values are
	// rejected by the compiler.
	MaxTransientStepRetries int `toml:"max_transient_step_retries" json:"max_transient_step_retries,omitempty"`
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
	// MaxTurns bounds the agent-loop turns each child agent of this step may
	// take. For an agent_panel step it bounds every panel member and the panel
	// synthesis child; for agent and agent_gate steps it bounds the step's own
	// agent loop. 0 means unlimited (the default), matching the agent loop's
	// MaxSteps=0 semantics and the [chat] max_steps config. Negative values
	// are rejected by the compiler.
	MaxTurns int `toml:"max_turns" json:"max_turns,omitempty"`
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
	// PartialTarget is the step a loop routes to when its budget exhausts and
	// the ledger still holds verified (succeeded) step outputs. Without it, an
	// exhausted loop fails the run (with a salvage hint). With it, the verified
	// work survives: the run advances to the target with run.salvage bound as
	// evidence. The target must be a declared step; it usually forwards to
	// success or delivery.
	PartialTarget string `toml:"partial_target" json:"partial_target,omitempty"`
}

type MatchCriteria struct {
	Status string            `toml:"status" json:"status,omitempty"`
	Output map[string]string `toml:"output" json:"output,omitempty"`
}

// ProviderGitHub is the only delivery provider the engine supports. It lives
// in the schema package, next to the field it constrains, so the compiler
// (admission) and the delivery package (run probe and delivery-time
// backstop) share one value instead of drifting literals.
const ProviderGitHub = "github"

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
	// OnDiffSizeFailure names the step that repairs an over-limit delivered
	// diff (a stacking hard_lines rejection). An empty value makes the run use
	// OnFailure for diff-size failures, which keeps pre-existing stacking
	// workflows on their declared generic repair step.
	OnDiffSizeFailure string `toml:"on_diff_size_failure" json:"on_diff_size_failure,omitempty"`
	// MaxRepairs bounds the delivery -> repair -> success -> delivery cycle:
	// how many times a delivery failure may route back into the workflow's
	// repair step before the run settles terminal (delivery_failed) with the
	// last rejection recorded. A rejection the named repair step cannot fix
	// must not cycle until the step cap or the run deadline is spent. Zero
	// selects the delivery package default (delivery.MaxDeliveryRepairs).
	// Negative values are rejected at admission.
	MaxRepairs int `toml:"max_repairs" json:"max_repairs,omitempty"`
	// DeliverPlanRun publishes a stacking plan-mode run's own diff as its own
	// PR after the stack drive. A plan run with a multi-chunk plan settles at
	// delivery_pending (its success terminal is delivery-policy active) and the
	// chunk stack is driven to completion; the chunk PRs carry the work. When
	// false (the default) the plan run is NOT published - the plan and its
	// artifacts stay recorded in the run ledger and the stack task ledger, and
	// the run settles succeeded. Set true to also publish the plan run's PR.
	DeliverPlanRun bool `toml:"deliver_plan_run" json:"deliver_plan_run,omitempty"`
}

// Stacking enables the generic stacked-small-PR capability for a workflow.
// A plan-mode run (no stack_mode input) executes the workflow's own planning
// steps plus an engine-synthesized decompose step and ends with a chunk plan;
// chunk-mode runs (stack_mode="chunk") start at implement_step and deliver one
// small PR each; a driver merges the stack incrementally.
//
// Stacking is opt-in: a workflow participates only when it declares a
// [stacking] table. A declared table is enabled unless it sets
// enabled = false, and must name plan_step and implement_step explicitly.
// Every knob is a per-workflow override of a global default.
type Stacking struct {
	// Enabled selects stacking for a workflow that declares this table. nil
	// means enabled (DefaultStackingEnabled); false is a deliberate opt-out.
	Enabled *bool `toml:"enabled" json:"enabled,omitempty"`
	// PlanStep is the id of the workflow's planning step (whose output feeds
	// the decompose step). Required when stacking is enabled.
	PlanStep string `toml:"plan_step" json:"plan_step,omitempty"`
	// ImplementStep is the id where chunk-mode runs start. Required when
	// stacking is enabled.
	ImplementStep string `toml:"implement_step" json:"implement_step,omitempty"`
	// MaxChunks bounds the number of chunks in one plan (0 = global default).
	MaxChunks int `toml:"max_chunks" json:"max_chunks,omitempty"`
	// SoftLines is the preferred per-chunk diff size (0 = global default).
	SoftLines int `toml:"soft_lines" json:"soft_lines,omitempty"`
	// HardLines is the maximum per-chunk diff size; delivery rejects larger
	// actual diffs (0 = global default).
	HardLines int `toml:"hard_lines" json:"hard_lines,omitempty"`
	// MaxFiles bounds the files per chunk (0 = global default).
	MaxFiles int `toml:"max_files" json:"max_files,omitempty"`
	// MergePolicy selects how merged PRs are approved: "approve" (human
	// approves each PR; the default) or "auto" (auto-merge on green with the
	// publish grant as the single human checkpoint).
	MergePolicy string `toml:"merge_policy" json:"merge_policy,omitempty"`
	// Agent names the agent used for the engine-synthesized decompose and
	// chunk-plan-validate steps. Empty selects the plan step's agent, which
	// always exists in the workflow, so agent references stay resolvable in
	// any workspace.
	Agent string `toml:"agent" json:"agent,omitempty"`
	// MaxTotalChunks bounds the number of chunks across all decompose waves
	// of one plan (0 = global default). It is the real ceiling on plan size;
	// MaxWaveChunks bounds only a single decompose call.
	MaxTotalChunks int `toml:"max_total_chunks" json:"max_total_chunks,omitempty"`
	// MaxWaveChunks bounds the number of chunks a single decompose call may
	// emit (0 = global default). Keeps one LLM call reliable; total plan size
	// is bounded by MaxTotalChunks instead.
	MaxWaveChunks int `toml:"max_wave_chunks" json:"max_wave_chunks,omitempty"`
	// MaxConcurrentChunks bounds how many chunk runs the stack driver admits
	// and drives concurrently within one ready wave (0 = global default).
	MaxConcurrentChunks int `toml:"max_concurrent_chunks" json:"max_concurrent_chunks,omitempty"`
	// SplitDeferred enables follow-up PR creation from a repair-produced
	// commit stack (spec-auto-split-oversized-prs.md §5.2-5.3): when a
	// chunk's delivered diff was still oversized despite a good estimate,
	// the diff-size repair step commits the review-sized slice plus one or
	// more additional commits for the deferred scope, and the driver admits
	// those trailing commits as follow-up chunk runs stacked on the first.
	// Opt-in (default false): shipped workflows must enable it explicitly.
	SplitDeferred *bool `toml:"split_deferred" json:"split_deferred,omitempty"`
	// SplitMaxChunks bounds how many follow-up PRs one oversized chunk's
	// repair may produce (0 = global default). Caps stack length; a repair
	// that produces more trailing commits than this allows folds the excess
	// into the last admitted follow-up chunk, logged, never silently dropped.
	SplitMaxChunks int `toml:"split_max_chunks" json:"split_max_chunks,omitempty"`
	// SplitMinLines: a trailing commit at or under this size folds into the
	// previous follow-up chunk instead of becoming its own PR (0 = global
	// default). Avoids a flood of trivial single-line follow-up PRs.
	SplitMinLines int `toml:"split_min_lines" json:"split_min_lines,omitempty"`
}

// Stacking defaults. These are the global defaults every workflow inherits;
// per-workflow [stacking] values override them.
const (
	DefaultStackingEnabled             = true
	DefaultStackingMaxChunks           = 12
	DefaultStackingSoftLines           = 200
	DefaultStackingHardLines           = 400
	DefaultStackingMaxFiles            = 5
	DefaultStackingMergePolicy         = "approve"
	DefaultStackingMaxTotalChunks      = 200
	DefaultStackingMaxWaveChunks       = 12
	DefaultStackingMaxConcurrentChunks = 4
	DefaultStackingSplitDeferred       = false
	DefaultStackingSplitMaxChunks      = 4
	DefaultStackingSplitMinLines       = 10
)

// ValidStackingMergePolicies enumerates the allowed merge_policy values.
var ValidStackingMergePolicies = map[string]bool{
	"approve": true,
	"auto":    true,
}

// StackingEnabled reports whether stacking applies to the workflow.
// Stacking is opt-in: a workflow without a [stacking] table does not
// participate. A declared table is enabled unless it sets enabled = false.
func (s *Stacking) StackingEnabled() bool {
	if s == nil {
		return false
	}
	if s.Enabled == nil {
		return DefaultStackingEnabled
	}
	return *s.Enabled
}

// SplitDeferredEnabled reports whether the repair-produced commit-stack
// correction path (§5.2-5.3) is enabled for the workflow. An explicit
// per-workflow value wins; otherwise the global default (off - opt-in).
func (s *Stacking) SplitDeferredEnabled() bool {
	if s == nil || s.SplitDeferred == nil {
		return DefaultStackingSplitDeferred
	}
	return *s.SplitDeferred
}

// StackingConfig is the resolved stacking configuration: per-workflow values
// with global defaults filled in for everything unset. PlanStep and
// ImplementStep are resolved by the compiler (inference needs the step
// graph) and passed in.
type StackingConfig struct {
	Enabled             bool
	PlanStep            string
	ImplementStep       string
	MaxChunks           int
	SoftLines           int
	HardLines           int
	MaxFiles            int
	MergePolicy         string
	Agent               string
	MaxTotalChunks      int
	MaxWaveChunks       int
	MaxConcurrentChunks int
	SplitDeferred       bool
	SplitMaxChunks      int
	SplitMinLines       int
}

// EffectiveStacking resolves the stacking configuration for a workflow.
// planStep and implementStep are the workflow's explicit [stacking] step
// ids, passed through by the compiler.
func (s *Stacking) EffectiveStacking(planStep, implementStep string) StackingConfig {
	cfg := StackingConfig{
		Enabled:             s.StackingEnabled(),
		PlanStep:            planStep,
		ImplementStep:       implementStep,
		MaxChunks:           DefaultStackingMaxChunks,
		SoftLines:           DefaultStackingSoftLines,
		HardLines:           DefaultStackingHardLines,
		MaxFiles:            DefaultStackingMaxFiles,
		MergePolicy:         DefaultStackingMergePolicy,
		MaxTotalChunks:      DefaultStackingMaxTotalChunks,
		MaxWaveChunks:       DefaultStackingMaxWaveChunks,
		MaxConcurrentChunks: DefaultStackingMaxConcurrentChunks,
		SplitDeferred:       DefaultStackingSplitDeferred,
		SplitMaxChunks:      DefaultStackingSplitMaxChunks,
		SplitMinLines:       DefaultStackingSplitMinLines,
	}
	if s == nil {
		return cfg
	}
	if s.MaxChunks > 0 {
		cfg.MaxChunks = s.MaxChunks
	}
	if s.SoftLines > 0 {
		cfg.SoftLines = s.SoftLines
	}
	if s.HardLines > 0 {
		cfg.HardLines = s.HardLines
	}
	if s.MaxFiles > 0 {
		cfg.MaxFiles = s.MaxFiles
	}
	if s.MergePolicy != "" {
		cfg.MergePolicy = s.MergePolicy
	}
	if s.MaxTotalChunks > 0 {
		cfg.MaxTotalChunks = s.MaxTotalChunks
	}
	if s.MaxWaveChunks > 0 {
		cfg.MaxWaveChunks = s.MaxWaveChunks
	}
	if s.MaxConcurrentChunks > 0 {
		cfg.MaxConcurrentChunks = s.MaxConcurrentChunks
	}
	cfg.SplitDeferred = s.SplitDeferredEnabled()
	if s.SplitMaxChunks > 0 {
		cfg.SplitMaxChunks = s.SplitMaxChunks
	}
	if s.SplitMinLines > 0 {
		cfg.SplitMinLines = s.SplitMinLines
	}
	cfg.Agent = s.Agent
	return cfg
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
