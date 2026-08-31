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
// The probe found four API defects on its first run: an oversized payload
// returned 500 with the failing SQL, a batch could carry its own sequence gap,
// a second consume of the same input reported success, and events still
// appended to an ended session. All four were fixed in apps/api and verified
// green against the deployment on 2026-08-31, so a red run now means a real
// regression rather than known debt.
package chatsync
