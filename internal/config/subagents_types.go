package config

// SubagentConfig holds subagent execution policy and storage configuration.
type SubagentConfig struct {
	MaxWorkers int `toml:"max_workers"`
	// MaxDepth caps the dependency depth of one orchestrated task graph
	// (spawn_agent depends_on chains). 0 means unlimited (default); a
	// positive value caps the depth.
	MaxDepth int `toml:"max_depth"`
	// MaxFanout caps the number of tasks admitted in one orchestration.
	// 0 means unlimited (default); a positive value caps the count.
	MaxFanout      int `toml:"max_fanout"`
	DefaultTimeout int `toml:"default_timeout_seconds"`
	// DefaultRequestTimeoutSec is the per-LLM-request timeout for subagents
	// (seconds). When 0, ResolvedSubagentRequestTimeout uses
	// DefaultSubagentRequestTimeoutSec (1800s, 30 minutes) as the per-request
	// context deadline. The derived http.Client wall stays above the resolved
	// value plus a margin (resolveProviderHTTPTimeout); the 12-hour
	// orchestration default no longer feeds individual subagent requests.
	DefaultRequestTimeoutSec int `toml:"default_request_timeout_seconds"`
	// DefaultTotalTimeoutSec is the whole-subagent wall-clock budget
	// (seconds). 0 = unset = DefaultSubagentTotalTimeoutSec (3600s, 60
	// minutes). Negative = off: a direct spawn with no per-task timeout then
	// has no handler-level bound at all, and workflow or panel steps whose
	// own timeout is unset stay bounded only by workflow policy - this is an
	// explicit operator opt-out of the last-resort termination guarantee. A
	// positive value is the budget itself; a tighter per-task timeout from
	// the caller still wins.
	DefaultTotalTimeoutSec int `toml:"default_total_timeout_seconds"`
	// WireStream opts nested subagent calls into the wire-stream transport:
	// the request goes to the provider with stream:true while the call keeps
	// its non-stream contract - the full answer is assembled before it comes
	// back. Nil means unset, which resolves to true - the pointer exists so
	// an explicit `wire_stream = false` is distinguishable from an absent
	// key, which a plain bool cannot express. False opts out and keeps every
	// nested call on the plain non-stream endpoint. See WireStreamResolved
	// for why this default was briefly flipped off and then restored.
	WireStream    *bool  `toml:"wire_stream"`
	DefaultBudget int    `toml:"default_budget"`
	SystemPrompt  string `toml:"system_prompt"`
	NestedSteps   int    `toml:"nested_steps"`

	// StoreBackend selects the ledger storage backend: "memory" (default) or "sqlite".
	StoreBackend string `toml:"store_backend"`

	// StorePath is the configured SQLite file path. Chat uses this path for
	// sessions, context, worktree routes, and orchestration.
	StorePath string `toml:"store_path"`

	// HandleRetentionSeconds controls how long completed orchestration run
	// handles remain accessible via inspect_agents/join_run/cancel_run
	// before automatic eviction. Default: 600 (10 minutes). 0 = no retention.
	HandleRetentionSeconds int `toml:"handle_retention_seconds"`

	// MaxAuditRounds controls the maximum number of ADLC Step 5 bug audit
	// rounds. When 0 (default), rounds are unlimited. Set to a positive
	// value to cap.
	MaxAuditRounds int `toml:"max_audit_rounds"`

	// InlineOutputBytes is the per-task output size threshold (bytes). Task
	// results whose output body is at or below this threshold are inlined in
	// the model-visible result envelope (the "output" field). Results above
	// this threshold emit only "output_ref", "output_bytes", and a bounded
	// "synopsis"; the parent fetches the full body via ledger_read.
	// Default: 4096. An explicit 0 means "always use refs" (never inline) and
	// is preserved through resolution via inlineOutputBytesSet; an absent key
	// falls back to the 4096 default. Errors follow the same rule with
	// "error"/"error_ref".
	InlineOutputBytes int `toml:"inline_output_bytes"`

	// inlineOutputBytesSet records whether [subagents] inline_output_bytes was
	// present in the config file, so an explicit 0 ("always use refs") survives
	// the defaulting in resolveSubagentConfig. No toml tag: go-toml/v2 ignores
	// unexported fields, so the flag is set only by loadFile's raw-byte probe.
	inlineOutputBytesSet bool

	// SchemaRetryMax is how many corrective re-entries a multi-step child may
	// take after an invalid schema-validated reply (plan tools/02). Default 2.
	// The initial attempt is separate: retry_max=2 allows two corrective turns.
	// Clamped at load: <= 0 means the default 2; a positive value above
	// MaxSchemaRetryMax is clamped to it.
	SchemaRetryMax int `toml:"schema_retry_max"`

	// SpawnStaggerMs staggers the start of each task after the first within
	// one dispatch batch by this many milliseconds, so concurrent workers do
	// not fire their first provider call on the same instant (the step-1
	// thundering-herd hang behind overloaded local proxies). Default: 150.
	// An explicit 0 disables staggering and is preserved through resolution
	// via spawnStaggerMsSet, mirroring inline_output_bytes; an absent key
	// falls back to the default. Values above 1000 are clamped to 1000.
	SpawnStaggerMs int `toml:"spawn_stagger_ms"`

	// spawnStaggerMsSet records whether [subagents] spawn_stagger_ms was
	// present in the config file, so an explicit 0 (disabled) survives the
	// defaulting in resolveSubagentConfig. No toml tag: go-toml/v2 ignores
	// unexported fields, so the flag is set only by loadFile's raw-byte probe.
	spawnStaggerMsSet bool

	// Messaging configures typed agent-to-agent messaging (plan 53). Nested
	// under [subagents.messaging]. Always enabled (product decision 2026-08-03).
	Messaging MessagingConfig `toml:"messaging"`

	// TaskRetry configures automatic retry/backoff for a dispatched task whose
	// failure is classified transient (provider.IsTransient - network blip,
	// 429, 5xx, timeout) or that timed out. Nested under [subagents.retry].
	// Distinct from SchemaRetryMax above, which governs corrective re-entries
	// after an invalid schema-validated reply within one task, not whole-task
	// retry. All-zero (the default) disables retry entirely, identical to
	// today's behavior: a deployment must opt in. See task_retry_config.go
	// for the TaskRetryConfig type.
	TaskRetry TaskRetryConfig `toml:"retry"`
}

// WireStreamResolved reports the resolved wire-stream switch: absent means
// on. Read this rather than the pointer so the opt-out default lives in one
// place.
//
// This default was briefly flipped off as a mitigation after two sessions
// reported dispatch_tasks batches sticking at "running" with no output.
// Root-caused (see DefaultMaxBudget in internal/subagents/subagents.go):
// the actual cause was an unrelated admission-control bug - a 1000
// default MaxBudget silently rejecting realistic multi-thousand-per-task
// budgets before any provider call was ever made (the reported "0ms"
// failures and "budget limit exceeded"/"run budget exceeded" errors both
// point directly at it). Confirmed by live reproduction: the exact failing
// batch (4 tasks, budget 6000 each) succeeds on the first call with
// wire_stream left on once the budget default is fixed, and a
// concurrency+stall stress test against wire_stream found no hang
// (openai_compat_turnstream_concurrency_test.go). Restored to on.
func (c SubagentConfig) WireStreamResolved() bool {
	return c.WireStream == nil || *c.WireStream
}
