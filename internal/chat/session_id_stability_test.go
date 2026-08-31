package chat

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// Session.SessionID identifies one conversation for its whole life. Chat
// session sync is about to key durable remote state on it: the CLI stores a
// local-id -> remote-session-id mapping plus a sequence high-water mark, and
// resumes from that mapping after a restart.
//
// That design only holds while the id is stable for operations that keep the
// same conversation. If /clear or an agent switch silently minted a new id,
// every such operation would orphan the remote session and start a fresh one,
// while everything local kept working - a failure that shows up as duplicate
// half-empty sessions on someone's tablet and nowhere else.
//
// RotateSessionID exists and correctly mints a new principal, but nothing in
// the product calls it today. These tests pin the boundary so that stays a
// decision rather than an accident: a future caller wiring rotation into one of
// these paths fails here first.

// TestClearKeepsTheSessionID covers the documented contract that /clear purges
// the transcript and nothing else. The conversation continues; only its history
// is gone.
func TestClearKeepsTheSessionID(t *testing.T) {
	sess := NewSession(&config.Resolved{Model: "m"}, &fakeCompleter{out: "reply"})
	sess.SystemPrompt = "SYS"
	sess.Messages = []provider.Message{
		{Role: provider.RoleSystem, Content: "SYS"},
		{Role: provider.RoleUser, Content: "first"},
	}
	before := sess.SessionID
	if before == "" {
		t.Fatal("a new session has no id")
	}

	if err := sess.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}

	if sess.SessionID != before {
		t.Errorf("Clear changed SessionID from %q to %q; remote sync state is keyed on it, so a change orphans the remote session", before, sess.SessionID)
	}
	// Guard against the test passing because Clear silently did nothing.
	if len(sess.Messages) > 1 {
		t.Errorf("Clear left %d messages; it should have purged the transcript", len(sess.Messages))
	}
}

// TestAgentSwitchKeepsTheSessionID covers the other path that rewrites most of
// the session surface. SetAgentSettings replaces the system prompt, the step
// limit, and the memory frame, and publishes a prefix reset - it changes what
// the model sees, not which conversation this is.
func TestAgentSwitchKeepsTheSessionID(t *testing.T) {
	sess := NewSession(&config.Resolved{Model: "m"}, &fakeCompleter{out: "reply"})
	sess.SystemPrompt = "SYS"
	sess.Messages = []provider.Message{{Role: provider.RoleSystem, Content: "SYS"}}
	before := sess.SessionID

	sess.SetAgentSettings("a different agent prompt", 42, "")

	if sess.SessionID != before {
		t.Errorf("SetAgentSettings changed SessionID from %q to %q; switching agents keeps the conversation", before, sess.SessionID)
	}
	if sess.SystemPrompt != "a different agent prompt" {
		t.Errorf("SystemPrompt = %q; the switch did not take effect, so this test proves nothing", sess.SystemPrompt)
	}
	if sess.MaxSteps != 42 {
		t.Errorf("MaxSteps = %d, want 42", sess.MaxSteps)
	}
}

// TestRotateSessionIDStillMintsANewID pins the escape hatch itself. The two
// tests above are only meaningful while rotation is a real capability that
// those paths deliberately do not use; if RotateSessionID quietly became a
// no-op, they would pass for the wrong reason.
func TestRotateSessionIDStillMintsANewID(t *testing.T) {
	sess := NewSession(&config.Resolved{Model: "m"}, &fakeCompleter{out: "reply"})
	before := sess.SessionID

	after, err := sess.RotateSessionID()
	if err != nil {
		t.Fatalf("RotateSessionID: %v", err)
	}
	if after == before {
		t.Fatal("RotateSessionID returned the same id")
	}
	if sess.SessionID != after {
		t.Errorf("SessionID = %q, want the rotated %q", sess.SessionID, after)
	}
}
