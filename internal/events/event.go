package events

import (
	"fmt"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// Kind is the type for event kinds.
type Kind string

const (
	// Agent loop events (mirror agent.EventKind values).
	KindAssistant         Kind = "assistant"
	KindToolStart         Kind = "tool_start"
	KindToolEnd           Kind = "tool_end"
	KindStep              Kind = "step"
	KindPrune             Kind = "prune"
	KindToolParallel      Kind = "tool_parallel"
	KindSubagentStart     Kind = "subagent_start"
	KindSubagentEnd       Kind = "subagent_end"
	KindSubagentHeartbeat Kind = "subagent_heartbeat"
	KindThinking          Kind = "thinking"
	KindCompaction        Kind = "compaction"
	// KindCacheUsage reports provider-supplied prompt-cache accounting for
	// one completion turn. See CacheUsageEvent.
	KindCacheUsage Kind = "cache_usage"
	// KindTokenUsage reports provider-supplied input/output token counts
	// for one completion turn. See TokenUsageEvent.
	KindTokenUsage Kind = "token_usage"

	// Session/turn lifecycle events.
	KindSessionStart Kind = "session_start"
	KindSessionEnd   Kind = "session_end"
	KindTurnStart    Kind = "turn_start"
	KindTurnEnd      Kind = "turn_end"

	// UI/system events.
	KindUIResize     Kind = "ui_resize"
	KindUserInput    Kind = "user_input"
	KindUIReady      Kind = "ui_ready"
	KindConfigChange Kind = "config_change"

	// Error events.
	KindError Kind = "error"
)

// Event is the universal event type for the event bus.
type Event struct {
	Kind       Kind
	Timestamp  time.Time
	SessionID  string
	TurnID     string
	ToolCallID string
	Name       string
	Detail     string
	Content    string
	Input      string
	Output     string
	Metadata   map[string]string
	Err        error

	// Agent attribution: which subagent produced this event (empty for the
	// session's root loop). Flat fields keep this package free of an
	// agent-package dependency.
	AgentTask  string // runtime request/task id - the attribution key
	AgentName  string // dispatched subagent/skill name
	AgentDepth int    // nesting depth (root loop = 0)
	// Identity is the typed, allowlisted runtime identity. It never carries
	// prompts, paths, digests, tools, content, errors, or arbitrary metadata.
	Identity *Identity
}

// CompactionEvent is the sealed, content-free progress payload for context
// compaction. It intentionally has no generic content/input/output fields.
type CompactionEvent struct {
	Trigger        string                   `json:"trigger"`
	BeforeTokens   int                      `json:"before_tokens"`
	AfterTokens    int                      `json:"after_tokens"`
	SourceRange    contextstate.SourceRange `json:"source_range"`
	SummaryVersion uint32                   `json:"summary_version"`
	sealed         bool                     `json:"-"`
}

// NewCompactionEvent constructs the only valid compaction event. Callers get
// a value, not a pointer, so the event bus cannot mutate the constructor's
// private state through a shared object.
func NewCompactionEvent(trigger string, beforeTokens, afterTokens int, sourceRange contextstate.SourceRange, summaryVersion uint32) (CompactionEvent, error) {
	event := CompactionEvent{
		Trigger: trigger, BeforeTokens: beforeTokens, AfterTokens: afterTokens,
		SourceRange: sourceRange, SummaryVersion: summaryVersion, sealed: true,
	}
	return event, event.Validate()
}

func (e CompactionEvent) Validate() error {
	if !e.sealed {
		return fmt.Errorf("invalid compaction event: constructor seal missing")
	}
	if strings.TrimSpace(e.Trigger) == "" || len(e.Trigger) > 256 {
		return fmt.Errorf("invalid compaction event: trigger")
	}
	for _, r := range e.Trigger {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("invalid compaction event: trigger contains control character")
		}
	}
	if e.BeforeTokens < 0 || e.AfterTokens < 0 || e.AfterTokens > e.BeforeTokens {
		return fmt.Errorf("invalid compaction event: token estimates")
	}
	if err := e.SourceRange.Validate(); err != nil {
		return fmt.Errorf("invalid compaction event: %w", err)
	}
	if e.SummaryVersion == 0 {
		return fmt.Errorf("invalid compaction event: summary version")
	}
	return nil
}

// CacheUsageEvent is the sealed, content-free progress payload for
// provider-reported prompt-cache accounting on one completion turn. It
// intentionally carries no message content - only provider/model
// attribution and token counts.
type CacheUsageEvent struct {
	Provider          string `json:"provider"`
	Model             string `json:"model"`
	Style             string `json:"style"`
	InputTokens       int    `json:"input_tokens"`
	CachedInputTokens int    `json:"cached_input_tokens"`
	CacheWriteTokens  int    `json:"cache_write_tokens"`
	sealed            bool   `json:"-"`
}

// NewCacheUsageEvent constructs the only valid cache usage event. Style is a
// bounded free-form string (not a shared enum with provider.CacheStyle) so
// this package stays independent of internal/provider; a future style value
// there needs no matching update here.
func NewCacheUsageEvent(provider, model, style string, inputTokens, cachedInputTokens, cacheWriteTokens int) (CacheUsageEvent, error) {
	event := CacheUsageEvent{
		Provider: provider, Model: model, Style: style,
		InputTokens: inputTokens, CachedInputTokens: cachedInputTokens, CacheWriteTokens: cacheWriteTokens,
		sealed: true,
	}
	return event, event.Validate()
}

func (e CacheUsageEvent) Validate() error {
	if !e.sealed {
		return fmt.Errorf("invalid cache usage event: constructor seal missing")
	}
	if strings.TrimSpace(e.Provider) == "" || len(e.Provider) > 64 {
		return fmt.Errorf("invalid cache usage event: provider")
	}
	if strings.TrimSpace(e.Model) == "" || len(e.Model) > 256 {
		return fmt.Errorf("invalid cache usage event: model")
	}
	if strings.TrimSpace(e.Style) == "" || len(e.Style) > 32 {
		return fmt.Errorf("invalid cache usage event: style")
	}
	for _, value := range []string{e.Provider, e.Model, e.Style} {
		for _, r := range value {
			if r < 0x20 || r == 0x7f {
				return fmt.Errorf("invalid cache usage event: contains control character")
			}
		}
	}
	if e.InputTokens < 0 || e.CachedInputTokens < 0 || e.CacheWriteTokens < 0 {
		return fmt.Errorf("invalid cache usage event: token counts")
	}
	return nil
}

// TokenUsageEvent is the sealed progress payload for provider-reported
// input/output token counts, carrying estimate-vs-actual drift metrics
// so operators can see when the len(s)/4 heuristic diverges.
type TokenUsageEvent struct {
	Provider         string  `json:"provider"`
	Model            string  `json:"model"`
	InputTokens      int     `json:"input_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	EstimatedTokens  int     `json:"estimated_tokens"`
	CalibrationRatio float64 `json:"calibration_ratio"`
	sealed           bool    `json:"-"`
}

// NewTokenUsageEvent constructs the only valid token usage event. Callers get
// a value, not a pointer, so the event bus cannot mutate the constructor's
// private state through a shared object.
func NewTokenUsageEvent(provider, model string, inputTokens, outputTokens, estimatedTokens int, calibrationRatio float64) (TokenUsageEvent, error) {
	event := TokenUsageEvent{
		Provider: provider, Model: model, InputTokens: inputTokens, OutputTokens: outputTokens,
		EstimatedTokens: estimatedTokens, CalibrationRatio: calibrationRatio, sealed: true,
	}
	return event, event.Validate()
}

func (e TokenUsageEvent) Validate() error {
	if !e.sealed {
		return fmt.Errorf("invalid token usage event: constructor seal missing")
	}
	if strings.TrimSpace(e.Provider) == "" || len(e.Provider) > 64 {
		return fmt.Errorf("invalid token usage event: provider")
	}
	if strings.TrimSpace(e.Model) == "" || len(e.Model) > 256 {
		return fmt.Errorf("invalid token usage event: model")
	}
	if e.InputTokens < 0 || e.OutputTokens < 0 || e.EstimatedTokens < 0 {
		return fmt.Errorf("invalid token usage event: token counts")
	}
	if e.CalibrationRatio < 0 {
		return fmt.Errorf("invalid token usage event: calibration ratio")
	}
	return nil
}

// Identity separates definition, disposable execution instance, and model
// binding generation for operator-facing lifecycle events.
type Identity struct {
	DefinitionName   string
	DefinitionSource string
	InstanceID       string
	ModelGeneration  uint64
}

// NewIdentity validates and copies the public identity contract.
func NewIdentity(name, source, instanceID string, generation uint64) (Identity, error) {
	name = strings.TrimSpace(name)
	source = strings.TrimSpace(source)
	instanceID = strings.TrimSpace(instanceID)
	if name == "" || len(name) > 80 || instanceID == "" || len(instanceID) > 128 || generation == 0 {
		return Identity{}, fmt.Errorf("invalid event identity")
	}
	switch source {
	case "user", "workspace", "compiled":
	default:
		return Identity{}, fmt.Errorf("invalid event identity")
	}
	for _, value := range []string{name, source, instanceID} {
		for _, r := range value {
			if r < 0x20 || r == 0x7f {
				return Identity{}, fmt.Errorf("invalid event identity")
			}
		}
	}
	return Identity{DefinitionName: name, DefinitionSource: source, InstanceID: instanceID, ModelGeneration: generation}, nil
}

// WithAgentAttribution returns a copy of e attributed to a producing agent.
func (e Event) WithAgentAttribution(taskID, name string, depth int) Event {
	e.AgentTask, e.AgentName, e.AgentDepth = taskID, name, depth
	return e
}

// NewEvent creates an Event with the given Kind and the current timestamp.
func NewEvent(kind Kind) Event {
	return Event{Kind: kind, Timestamp: time.Now()}
}
