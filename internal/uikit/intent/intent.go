// Package intent carries user intents from the UI to the harness, the
// input side of the uievent contract. This phase ships only what
// ports.Conversation.Send needs to type-check; Cancel/SwitchModel/Approve
// and their kin are added when a consumer needs them.
package intent

// Send is a user's chat submission.
type Send struct {
	Text string
}
