package agent

// AllEventKinds returns every EventKind this package declares, as a copy.
//
// It exists so a consumer's exhaustiveness test drives the real set instead
// of a list written by hand beside it. A hand list is how a kind ships dead:
// EventAssistantReset was added to the producer, the bus, the relay and the
// projector, and the test that was supposed to prove every kind reaches a
// renderer never mentioned it - so three of the four renderers dropped it
// with every suite green.
//
// The list is kept honest by TestAllEventKindsMatchesTheDeclaredConstants,
// which parses this package's source rather than trusting this function.
func AllEventKinds() []EventKind {
	return append([]EventKind(nil), allEventKinds...)
}

var allEventKinds = []EventKind{
	EventAssistant,
	EventAssistantReset,
	EventToolPending,
	EventToolStart,
	EventToolEnd,
	EventStep,
	EventHeartbeat,
	EventPrune,
	EventToolParallel,
	EventSubagentStart,
	EventSubagentEnd,
	EventSubagentHeartbeat,
	EventSubagentBegin,
	EventSubagentDone,
	EventThinking,
	EventHook,
	EventCompaction,
	EventCacheUsage,
	EventTokenUsage,
	EventWorkLimit,
	EventSchemaRetry,
	EventEmptyResponseRetry,
	EventUnactedContinuation,
}
