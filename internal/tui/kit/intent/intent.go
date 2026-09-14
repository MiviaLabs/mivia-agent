// Package intent carries user intents from the UI to the harness, the
// input side of the uievent contract. This phase ships only what
// ports.Conversation.Send needs to type-check; Cancel/SwitchModel/Approve
// and their kin are added when a consumer needs them.
package intent

// Send is a user's chat submission.
type Send struct {
	Text string

	// PersistedText, when non-empty, is what enters conversation history and
	// gets replayed on every later turn - Text is what the provider sees for
	// THIS request only. Empty means the two are identical (the ordinary
	// case). Exists for UI-only expansions such as slash skills, whose full
	// instruction body (thousands of tokens) belongs in the one request that
	// needs it, not in a permanent history entry replayed forever after.
	PersistedText string
}
