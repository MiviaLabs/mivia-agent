package events

import (
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// MarshalCompactionEvent serializes only the typed compaction shape. Generic
// events cannot be passed to this boundary, so summary/content envelopes have
// no conversion path into a compaction progress event.
func MarshalCompactionEvent(event CompactionEvent) ([]byte, error) {
	if err := event.Validate(); err != nil {
		return nil, err
	}
	return contextstate.MarshalCanonical(event)
}

// UnmarshalCompactionEvent restores the constructor seal only after all wire
// fields have been decoded and validated.
func UnmarshalCompactionEvent(data []byte) (CompactionEvent, error) {
	var wire struct {
		Trigger        string                   `json:"trigger"`
		BeforeTokens   int                      `json:"before_tokens"`
		AfterTokens    int                      `json:"after_tokens"`
		ElidedMessages int                      `json:"elided_messages"`
		ElidedBytes    int                      `json:"elided_bytes"`
		SourceRange    contextstate.SourceRange `json:"source_range"`
		SummaryVersion uint32                   `json:"summary_version"`
		Summarized     bool                     `json:"summarized"`
	}
	if err := contextstate.UnmarshalCanonical(data, &wire); err != nil {
		return CompactionEvent{}, err
	}
	return NewCompactionEvent(CompactionEventParams{
		Trigger: wire.Trigger, BeforeTokens: wire.BeforeTokens, AfterTokens: wire.AfterTokens,
		ElidedMessages: wire.ElidedMessages, ElidedBytes: wire.ElidedBytes,
		SourceRange: wire.SourceRange, SummaryVersion: wire.SummaryVersion,
		Summarized: wire.Summarized,
	})
}

// MarshalPrefixResetEvent serializes only the typed prefix-reset shape. Generic
// events cannot be passed to this boundary, so content envelopes have no
// conversion path into a prefix-reset event.
func MarshalPrefixResetEvent(event PrefixResetEvent) ([]byte, error) {
	if err := event.Validate(); err != nil {
		return nil, err
	}
	return contextstate.MarshalCanonical(event)
}

// UnmarshalPrefixResetEvent restores the constructor seal only after all wire
// fields have been decoded and validated, so a wire payload with empty,
// unknown, duplicate, oversized, or control-character categories is rejected
// through re-validation (INV-68-7).
func UnmarshalPrefixResetEvent(data []byte) (PrefixResetEvent, error) {
	var wire struct {
		Categories                []string `json:"categories"`
		OutgoingModelGeneration   uint64   `json:"outgoing_model_generation"`
		IncomingModelGeneration   uint64   `json:"incoming_model_generation"`
		OutgoingSurfaceGeneration uint64   `json:"outgoing_surface_generation"`
		IncomingSurfaceGeneration uint64   `json:"incoming_surface_generation"`
	}
	if err := contextstate.UnmarshalCanonical(data, &wire); err != nil {
		return PrefixResetEvent{}, err
	}
	return NewPrefixResetEvent(PrefixResetEventParams{Categories: wire.Categories, OutgoingModelGeneration: wire.OutgoingModelGeneration, IncomingModelGeneration: wire.IncomingModelGeneration, OutgoingSurfaceGeneration: wire.OutgoingSurfaceGeneration, IncomingSurfaceGeneration: wire.IncomingSurfaceGeneration})
}
