// Package conversation is the base screen: the transcript, composer,
// transient status line, and inline approval prompt, driven by a real
// ports.Conversation. It never calls a harness directly, only Send,
// Cancel, and Approver.Resolve, exactly the ports surface.
//
// It may import internal/tui/kit and internal/tui/view packages only. It
// must not import internal/cli, internal/chat, internal/agent,
// internal/coordinator, or internal/hub.
package conversation
