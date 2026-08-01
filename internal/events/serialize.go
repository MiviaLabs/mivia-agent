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
		SourceRange    contextstate.SourceRange `json:"source_range"`
		SummaryVersion uint32                   `json:"summary_version"`
	}
	if err := contextstate.UnmarshalCanonical(data, &wire); err != nil {
		return CompactionEvent{}, err
	}
	return NewCompactionEvent(wire.Trigger, wire.BeforeTokens, wire.AfterTokens, wire.SourceRange, wire.SummaryVersion)
}
