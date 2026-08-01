package contextstate

import "fmt"

// ValidateSourceEvent is the named source-boundary validator used by storage
// and import adapters. The implementation delegates to the DTO validator so
// callers have one stable entry point for source records.
func ValidateSourceEvent(event SourceEvent) error {
	return event.Validate()
}

func ValidateSourceEvents(events []SourceEvent, sessionID string, firstSequence uint64) error {
	if exceedsLimit(len(events), CurrentLimits().CommitEvents) {
		return invalid("source_events", "too many events")
	}
	for i, event := range events {
		if err := event.Validate(); err != nil {
			return fmt.Errorf("source event %d: %w", i, err)
		}
		if event.ID.SessionID != sessionID || event.ID.Sequence != firstSequence+uint64(i) {
			return invalid("source_events", "events are not owner-scoped and contiguous")
		}
	}
	return nil
}
