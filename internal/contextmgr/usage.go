package contextmgr

import "context"

// UsageWriter durably records one token/cache/compaction usage measurement.
// Implemented by internal/storage (RecordUsageEvent). Lives in contextmgr,
// not internal/agent, deliberately: internal/agent already imports
// contextmgr (Preparation, Summarizer), so ContextManager holding a
// UsageWriter field of an internal/agent type would create an
// agent -> contextmgr -> agent import cycle. contextmgr is neutral ground
// both agent and storage can depend on.
//
// A Record failure is logged and dropped by the caller, never returned to
// or allowed to fail the turn it describes - this is a best-effort,
// synchronous side write, not a durability guarantee the turn depends on.
type UsageWriter interface {
	Record(ctx context.Context, record UsageRecord) error
}

// UsageRecord is one row of the token_usage_events table. Kind selects which
// of the kind-specific fields below are meaningful; the others are left at
// their zero value for that record.
type UsageRecord struct {
	// Kind is "token_usage", "cache_usage", or "compaction".
	Kind      string
	SessionID string
	TurnID    string

	// Provider/model identify the completion this record describes. Not set
	// for a compaction record.
	Provider string
	Model    string

	// InputTokens is set by both token_usage (with OutputTokens/
	// EstimatedTokens/CalibrationRatio) and cache_usage (the turn's total
	// input tokens, of which CachedInputTokens/CacheWriteTokens are a
	// subset) records.
	InputTokens      int
	OutputTokens     int
	EstimatedTokens  int
	CalibrationRatio float64

	// cache_usage fields.
	CachedInputTokens int
	CacheWriteTokens  int

	// compaction fields.
	BeforeTokens   int
	AfterTokens    int
	ElidedMessages int
	ElidedBytes    int
	Summarized     *bool
	Reason         string

	// Attribution, when the turn belongs to a subagent. Empty for a
	// root-loop turn.
	AgentTask  string
	AgentName  string
	AgentDepth int
}
