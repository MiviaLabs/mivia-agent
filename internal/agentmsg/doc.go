// Package agentmsg is the leaf surface for typed agent-to-agent messages.
//
// It defines the fixed message-kind vocabulary, strict validation, and
// construction helpers. This package may import only the standard library and
// internal/sdkadapter. It must never import ledger, coordinator, or cli: those
// layers own serialization into lifecycle payloads and delivery.
//
// A2A mapping (documented only; not implemented):
//
//	Message  → A2A Message
//	Finding  → A2A Artifact
//	Question → A2A input-required status update
//	Answer   → A2A Message (reply)
//	Steer    → A2A Message (task-scoped guidance)
//	Ask      → A2A Message (parent-routed referral)
//
// Progress/heartbeat is deliberately not an envelope kind; it stays on the
// existing agent.EventKind stream.
package agentmsg
