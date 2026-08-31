// Package chatsync will hold the CLI side of chat session sync: pushing local
// session events to the mivia API and long-polling it for remote input.
//
// None of that exists yet. The API half shipped first, deliberately, and the
// client design is still being planned. What this package holds today is only
// the live contract probe in live_contract_test.go, behind the `livechat`
// build tag, which checks the deployed /v1/chat-sessions surface end to end.
//
// The probe is deliberately not a client. It speaks raw HTTP and keeps its own
// wire structs, so it pins what the SERVER does without freezing decisions
// about how the CLI should be built. When the client lands, promote those
// structs out of the test file rather than writing new ones from the docs: the
// probe's versions are the ones proven against a running deployment.
//
// Four probes fail on purpose. They are API defects the probe found, not
// harness bugs, and each goes green when the API is fixed. See
// docs/development/agent-workflow.md, "Live chat-session probe".
package chatsync
