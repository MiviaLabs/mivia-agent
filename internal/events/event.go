package events

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// Kind is the type for event kinds.
type Kind string

const (
	// Agent loop events (mirror agent.EventKind values).
	KindAssistant Kind = "assistant"
	KindToolStart Kind = "tool_start"
	KindToolEnd   Kind = "tool_end"
	KindStep      Kind = "step"
	// KindHeartbeat mirrors agent.EventHeartbeat: a wall-clock progress
	// tick during model thinking, tool batches, and batch shaping. The
	// root loop publishes the bare string "heartbeat". Use this constant
	// to subscribe to it.
	KindHeartbeat         Kind = "heartbeat"
	KindPrune             Kind = "prune"
	KindToolParallel      Kind = "tool_parallel"
	KindSubagentStart     Kind = "subagent_start"
	KindSubagentEnd       Kind = "subagent_end"
	KindSubagentHeartbeat Kind = "subagent_heartbeat"
	// KindSubagentDone mirrors agent.EventSubagentDone: the run-level
	// terminal signal for one subagent, not the end of a nested tool call.
	KindSubagentDone Kind = "subagent_done"
	KindThinking     Kind = "thinking"
	KindCompaction   Kind = "compaction"
	// KindCacheUsage reports provider-supplied prompt-cache accounting for
	// one completion turn. See CacheUsageEvent.
	KindCacheUsage Kind = "cache_usage"
	// KindTokenUsage reports provider-supplied input/output token counts
	// for one completion turn. See TokenUsageEvent.
	KindTokenUsage Kind = "token_usage"
	// KindPrefixReset reports that the session's byte-prefix stability
	// identity changed at a binding switch or agent-surface publication, so a
	// provider-implicit prompt-cache prefix is no longer reusable for the next
	// request. See PrefixResetEvent.
	KindPrefixReset Kind = "prefix_reset"

	// Session/turn lifecycle events.
	KindSessionStart Kind = "session_start"
	KindSessionEnd   Kind = "session_end"
	KindTurnStart    Kind = "turn_start"
	KindTurnEnd      Kind = "turn_end"

	// Workflow and invocation observability events. Run, step, and task
	// identifiers ride in Event.Metadata; no Event fields are added.
	// KindWorkflowRunStarted reports the start of one workflow run.
	KindWorkflowRunStarted Kind = "workflow_run_started"
	// KindWorkflowStepStarted reports the start of one workflow step.
	KindWorkflowStepStarted Kind = "workflow_step_started"
	// KindWorkflowStepHeartbeat is the progress tick of a running step.
	KindWorkflowStepHeartbeat Kind = "workflow_step_heartbeat"
	// KindWorkflowStepCompleted reports the completion of one workflow step.
	KindWorkflowStepCompleted Kind = "workflow_step_completed"
	// KindWorkflowGateResult reports the start of one workflow gate: the gate
	// begin is published at gate_started time; the gate's outcome is published
	// as step_completed when the attempt reaches its terminal status.
	KindWorkflowGateResult Kind = "workflow_gate_result"
	// KindWorkflowApprovalRequested reports a workflow approval request.
	KindWorkflowApprovalRequested Kind = "workflow_approval_requested"
	// KindWorkflowRunFinished reports the end of one workflow run.
	KindWorkflowRunFinished Kind = "workflow_run_finished"
	// KindWorkflowDeliveryStage reports one delivery stage of a workflow.
	KindWorkflowDeliveryStage Kind = "workflow_delivery_stage"
	// KindInvocationStarted reports the start of one invocation.
	KindInvocationStarted Kind = "invocation_started"
	// KindInvocationCompleted reports the completion of one invocation.
	KindInvocationCompleted Kind = "invocation_completed"
	// KindInvocationRetrying reports one retry of an invocation.
	KindInvocationRetrying Kind = "invocation_retrying"

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
	// PrefixReset is present only for the typed prefix-stability reset event
	// (KindPrefixReset). It is not copied into generic content/input/output
	// envelopes and carries no prompt or digest content (INV-68-7).
	PrefixReset *PrefixResetEvent
}

// CompactionEvent is the sealed, content-free progress payload for context
// compaction. It intentionally has no generic content/input/output fields.
type CompactionEvent struct {
	Trigger        string                   `json:"trigger"`
	BeforeTokens   int                      `json:"before_tokens"`
	AfterTokens    int                      `json:"after_tokens"`
	ElidedMessages int                      `json:"elided_messages"`
	ElidedBytes    int                      `json:"elided_bytes"`
	SourceRange    contextstate.SourceRange `json:"source_range"`
	SummaryVersion uint32                   `json:"summary_version"`
	sealed         bool                     `json:"-"`
}

// CompactionEventParams is the only constructor input for CompactionEvent.
// ElidedMessages and ElidedBytes are optional content-free aggregates.
type CompactionEventParams struct {
	Trigger        string
	BeforeTokens   int
	AfterTokens    int
	ElidedMessages int
	ElidedBytes    int
	SourceRange    contextstate.SourceRange
	SummaryVersion uint32
}

// NewCompactionEvent constructs the only valid compaction event. Callers get
// a value, not a pointer, so the event bus cannot mutate the constructor's
// private state through a shared object.
func NewCompactionEvent(p CompactionEventParams) (CompactionEvent, error) {
	event := CompactionEvent{
		Trigger: p.Trigger, BeforeTokens: p.BeforeTokens, AfterTokens: p.AfterTokens,
		ElidedMessages: p.ElidedMessages, ElidedBytes: p.ElidedBytes,
		SourceRange: p.SourceRange, SummaryVersion: p.SummaryVersion, sealed: true,
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
	if e.ElidedMessages < 0 || e.ElidedBytes < 0 {
		return fmt.Errorf("invalid compaction event: elision counters")
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

// HitPercent returns the cache hit rate as an integer percent. It guards
// the division: zero input tokens reads as 0.
func (e CacheUsageEvent) HitPercent() int {
	if e.InputTokens <= 0 {
		return 0
	}
	return e.CachedInputTokens * 100 / e.InputTokens
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

// prefixResetCategoryAllowlist is the fixed set of category names a
// PrefixResetEvent may carry. Each name is one wire-affecting identity
// dimension; the names are fixed so the event can never smuggle prompt
// content, digest preimages, tool-schema bodies, or tool-argument values
// (INV-68-7).
var prefixResetCategoryAllowlist = map[string]struct{}{
	"model":         {},
	"reasoning":     {},
	"tools":         {},
	"system_prompt": {},
	// memory reports a changed core-memory context frame (the user-role
	// message at index 1): it alters wire bytes after the stable prefix, so
	// identity equality stays equivalent to byte-equal prefixes (INV-68-1).
	"memory":         {},
	"agent_switch":   {},
	"tool_admission": {},
}

// maxPrefixResetCategoryLen bounds one category name. The allowlist names are
// all well under this bound; the check exists so an oversized wire category is
// rejected on its own terms rather than surfacing as a generic unknown.
const maxPrefixResetCategoryLen = 16

// PrefixResetEvent is the sealed, content-free payload reporting that the
// session's byte-prefix stability identity changed. It intentionally has no
// generic content/input/output fields: it carries only allowlisted changed
// category names and the outgoing/incoming generation counters, never prompt
// content, digest preimages, tool-schema bodies, or tool-argument values
// (INV-68-7).
type PrefixResetEvent struct {
	Categories                []string `json:"categories"`
	OutgoingModelGeneration   uint64   `json:"outgoing_model_generation"`
	IncomingModelGeneration   uint64   `json:"incoming_model_generation"`
	OutgoingSurfaceGeneration uint64   `json:"outgoing_surface_generation"`
	IncomingSurfaceGeneration uint64   `json:"incoming_surface_generation"`
	sealed                    bool     `json:"-"`
}

// PrefixResetEventParams is the only constructor input for PrefixResetEvent.
// The generation counters ride as observability only: a republish that changes
// only a monotonic counter is byte-stable and must not emit a reset (INV-68-2,
// test-plan correction 4).
type PrefixResetEventParams struct {
	Categories                []string
	OutgoingModelGeneration   uint64
	IncomingModelGeneration   uint64
	OutgoingSurfaceGeneration uint64
	IncomingSurfaceGeneration uint64
}

// NewPrefixResetEvent constructs the only valid prefix-reset event. Callers
// get a value, not a pointer, so the event bus cannot mutate the constructor's
// private state through a shared object.
func NewPrefixResetEvent(p PrefixResetEventParams) (PrefixResetEvent, error) {
	event := PrefixResetEvent{Categories: slices.Clone(p.Categories), OutgoingModelGeneration: p.OutgoingModelGeneration, IncomingModelGeneration: p.IncomingModelGeneration, OutgoingSurfaceGeneration: p.OutgoingSurfaceGeneration, IncomingSurfaceGeneration: p.IncomingSurfaceGeneration, sealed: true}
	return event, event.Validate()
}

func (e PrefixResetEvent) Validate() error {
	if !e.sealed {
		return fmt.Errorf("invalid prefix reset event: constructor seal missing")
	}
	if len(e.Categories) == 0 {
		return fmt.Errorf("invalid prefix reset event: no categories")
	}
	seen := make(map[string]struct{}, len(e.Categories))
	for _, category := range e.Categories {
		if len(category) > maxPrefixResetCategoryLen {
			return fmt.Errorf("invalid prefix reset event: category %q is oversized", category)
		}
		for _, r := range category {
			if r < 0x20 || r == 0x7f {
				return fmt.Errorf("invalid prefix reset event: category contains control character")
			}
		}
		if _, ok := prefixResetCategoryAllowlist[category]; !ok {
			return fmt.Errorf("invalid prefix reset event: unknown category %q", category)
		}
		if _, dup := seen[category]; dup {
			return fmt.Errorf("invalid prefix reset event: duplicate category %q", category)
		}
		seen[category] = struct{}{}
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
