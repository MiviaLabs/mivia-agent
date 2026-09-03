package events

import (
	"fmt"
	"slices"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// HookEvent is the structured payload for one lifecycle hook run.
type HookEvent struct {
	// Phase is PreToolUse, PostToolUse or Stop.
	Phase string `json:"phase"`
	// Program is the script's name, never its path: a path is machine
	// topology, and this payload crosses to other processes and machines.
	Program string `json:"program"`
	// Tool is the call the hook ran for. Empty for a Stop hook, which runs
	// for the turn rather than for one call.
	Tool string `json:"tool"`
	// Denied is true for the run that BLOCKED the call. A blocked call emits
	// no tool_end, so this flag is the only account of why it never ran.
	Denied bool `json:"denied"`
	// Output is what the hook PRINTED. It is carried here rather than read
	// from the generic Event.Output, which appends an operator diagnostic
	// naming the hook's absolute path - fine on the machine that ran it, not
	// something to send to a remote viewer.
	Output string `json:"output"`
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
	// Summarized reports whether an LLM summary of the dropped messages was
	// actually produced. A compaction can succeed structurally with no
	// summary at all (the workspace never configured one, or the summary
	// call degraded), and SummaryVersion cannot express that: the validator
	// requires it to be non-zero, so it was hardcoded to 1 and claimed a
	// summary existed either way. A renderer that shows a clean "compacted"
	// banner for a summary-less compaction is why an operator sees an
	// instant, LLM-free compact and concludes compaction is broken.
	Summarized bool `json:"summarized"`
	// Reason names why Summarized is false, as a fixed, classified,
	// content-free string (e.g. "no summarizer is configured for this
	// session") - never a raw provider/library error, which could carry
	// prompt or response fragments onto this sealed wire contract. Empty
	// when Summarized is true. Without this a renderer could only ever tell
	// an operator to "enable" summarization, even when it was already
	// enabled and something else (a missing credential, an unresolved
	// endpoint, a failed provider call) was the real cause.
	Reason string `json:"reason,omitempty"`
	sealed bool   `json:"-"`
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
	Summarized     bool
	Reason         string
}

// NewCompactionEvent constructs the only valid compaction event. Callers get
// a value, not a pointer, so the event bus cannot mutate the constructor's
// private state through a shared object.
func NewCompactionEvent(p CompactionEventParams) (CompactionEvent, error) {
	event := CompactionEvent{
		Trigger: p.Trigger, BeforeTokens: p.BeforeTokens, AfterTokens: p.AfterTokens,
		ElidedMessages: p.ElidedMessages, ElidedBytes: p.ElidedBytes,
		SourceRange: p.SourceRange, SummaryVersion: p.SummaryVersion,
		Summarized: p.Summarized, Reason: p.Reason, sealed: true,
	}
	return event, event.Validate()
}

// RehydrateCompactionEvent seals a compaction payload reconstructed from a
// cross-process wire projection (e.g. internal/hub's WireCompaction). The
// original publisher validated the values through NewCompactionEvent;
// re-validating here is impossible without the sealed flag and re-deriving it
// would drop a relayed event for rules the sender already met. An unsealed
// reconstruction fails its own Validate, which is the trap this exists to
// close for later consumers.
func RehydrateCompactionEvent(c CompactionEvent) *CompactionEvent {
	c.sealed = true
	return &c
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
	if len(e.Reason) > 256 {
		return fmt.Errorf("invalid compaction event: reason")
	}
	for _, r := range e.Reason {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("invalid compaction event: reason contains control character")
		}
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
