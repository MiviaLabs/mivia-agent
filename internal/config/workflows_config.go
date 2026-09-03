package config

// WorkflowsConfig holds workflow-engine defaults.
type WorkflowsConfig struct {
	Panels WorkflowPanelLimits `toml:"panels"`
}

// WorkflowPanelLimits overrides the compiled defaults every agent_panel
// step's member and synthesis children run under
// (internal/workflows/controller.DefaultPanelLimits, applied in
// buildPanelAttempt/buildPanelSynthesisWork). A nil field keeps the
// compiled default; this mirrors the [chat] max_steps *int
// nil-means-default pattern (ChatConfig.MaxSteps above), not a new
// convention.
type WorkflowPanelLimits struct {
	MemberMaxOutputPerCall    *int `toml:"member_max_output_per_call"`
	MemberMaxToolCalls        *int `toml:"member_max_tool_calls"`
	SynthesisMaxOutputPerCall *int `toml:"synthesis_max_output_per_call"`
	SynthesisMaxToolCalls     *int `toml:"synthesis_max_tool_calls"`
	// MemberDeadlineDefaultSeconds overrides the wall-clock default a
	// panel member attempt gets when the workflow declares no run
	// deadline (max_duration_seconds = 0). Seconds, matching
	// definition.Limits.MaxDurationSeconds' unit.
	MemberDeadlineDefaultSeconds *int `toml:"member_deadline_default_seconds"`
}
