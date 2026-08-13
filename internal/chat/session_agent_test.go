package chat

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func newAgentSettingsTestSession(t *testing.T) *Session {
	t.Helper()
	return NewSession(&config.Resolved{Model: "m", SystemPrompt: "you are a helpful agent", MaxSteps: intpSession(4)}, &fakeCompleter{})
}

// TestAgentSettingsReturnsBaseNotComposed is the direct contract assertion
// for plan 77's E3: AgentSettings() must return BaseSystemPrompt, never the
// composed SystemPrompt - the whole point of the redesign after the Step 0
// hostile review BLOCKed idempotent stripping (AR-1, AR-2).
func TestAgentSettingsReturnsBaseNotComposed(t *testing.T) {
	s := newAgentSettingsTestSession(t)
	s.SetAgentSettings("root prompt", 4, "- promoted fact: worth remembering")

	if s.SystemPrompt == s.BaseSystemPrompt {
		t.Fatalf("SystemPrompt and BaseSystemPrompt must differ when a memory block is composed")
	}
	if !strings.Contains(s.SystemPrompt, "core-memory-context") {
		t.Fatalf("SystemPrompt missing the composed block:\n%s", s.SystemPrompt)
	}

	base, _ := s.AgentSettings()
	if base != "root prompt" {
		t.Fatalf("AgentSettings() = %q, want the uncomposed base %q", base, "root prompt")
	}
	if strings.Contains(base, "core-memory-context") {
		t.Fatalf("AgentSettings() leaked the memory block into the base:\n%s", base)
	}
}

// TestSetAgentSettingsReadModifyWriteNeverDuplicatesBlock is the literal
// AR-1 regression: tool_admission.go's applyDeferredToolPrompt and
// agent_switch.go's selectedAgentSettings both do
// `base, maxSteps := sess.AgentSettings(); base = base + tail;
// sess.SetAgentSettings(base, maxSteps, block)` - mirrored here exactly.
// The original idempotent-strip design failed this because the tail was
// appended AFTER reading the (then-composed) prompt, so the closing tag was
// no longer at the string's end. The redesign must produce exactly one
// block regardless of how many read-modify-write cycles run.
func TestSetAgentSettingsReadModifyWriteNeverDuplicatesBlock(t *testing.T) {
	s := newAgentSettingsTestSession(t)
	block := "- promoted fact: worth remembering"

	s.SetAgentSettings("root prompt", 4, block)
	if n := strings.Count(s.SystemPrompt, "<core-memory-context>"); n != 1 {
		t.Fatalf("after first compose: %d opening tags, want 1:\n%s", n, s.SystemPrompt)
	}

	// Mirrors tool_admission.go's applyDeferredToolPrompt: read the base,
	// append a tail, write back - with the SAME memory block, as a real
	// caller would (coreMemoryBlockForState re-queries the same store).
	base, maxSteps := s.AgentSettings()
	base = base + "\n\ndeferred tool index: foo, bar"
	s.SetAgentSettings(base, maxSteps, block)

	if n := strings.Count(s.SystemPrompt, "<core-memory-context>"); n != 1 {
		t.Fatalf("after read-modify-write cycle: %d opening tags, want exactly 1 (AR-1 regression):\n%s", n, s.SystemPrompt)
	}
	if !strings.Contains(s.SystemPrompt, "deferred tool index: foo, bar") {
		t.Fatalf("the appended tail was lost:\n%s", s.SystemPrompt)
	}

	// A second read-modify-write cycle must still hold - this is meant to
	// happen repeatedly (every tool admission), not just once.
	base, maxSteps = s.AgentSettings()
	base = base + "\n\ndeferred tool index: foo, bar, baz"
	s.SetAgentSettings(base, maxSteps, block)
	if n := strings.Count(s.SystemPrompt, "<core-memory-context>"); n != 1 {
		t.Fatalf("after second read-modify-write cycle: %d opening tags, want exactly 1:\n%s", n, s.SystemPrompt)
	}
}

// TestSetAgentSettingsEmptyBlockClearsStaleContent is the literal AR-2
// regression: a demote/delete/InjectCore-off must not leave a stale block
// behind. The original idempotent-strip design skipped stripping entirely
// on the empty-block early-return path.
func TestSetAgentSettingsEmptyBlockClearsStaleContent(t *testing.T) {
	s := newAgentSettingsTestSession(t)
	s.SetAgentSettings("root prompt", 4, "- promoted fact: worth remembering")
	if !strings.Contains(s.SystemPrompt, "core-memory-context") {
		t.Fatalf("setup: expected a composed block:\n%s", s.SystemPrompt)
	}

	s.SetAgentSettings("root prompt", 4, "")
	if strings.Contains(s.SystemPrompt, "core-memory-context") {
		t.Fatalf("stale memory block survived an empty-block recompose (AR-2 regression):\n%s", s.SystemPrompt)
	}
	if s.SystemPrompt != "root prompt" {
		t.Fatalf("SystemPrompt = %q, want exactly the base prompt with nothing appended", s.SystemPrompt)
	}
}

// TestToolAdmissionNeverDuplicatesMemoryBlockAcrossRepeatedAdmissions is the
// literal Bug 1 regression from Step 5 hostile audit of the plan 77
// implementation: publishPendingAdmission (admission_status.go) read
// s.SystemPrompt (composed) instead of s.BaseSystemPrompt when building the
// AgentSurfacePublication.Prompt it hands to the widener - which is exactly
// the production shape (internal/cli's newSurfaceWidener sets
// req.MemoryBlock then calls TryPublishAgentSurface), reproduced here with
// a widener that mimics that host callback precisely.
func TestToolAdmissionNeverDuplicatesMemoryBlockAcrossRepeatedAdmissions(t *testing.T) {
	sess := newAdmissionSession(t)
	block := "- promoted fact: worth remembering"
	sess.SetAgentSettings("root prompt", 4, block)
	if n := strings.Count(sess.SystemPrompt, "<core-memory-context>"); n != 1 {
		t.Fatalf("setup: %d opening tags, want 1:\n%s", n, sess.SystemPrompt)
	}

	widener := &recordingWidener{publish: func(req AgentSurfacePublication) (bool, error) {
		// Mirrors internal/cli's newSurfaceWidener exactly: set the memory
		// block, then publish through TryPublishAgentSurface.
		req.MemoryBlock = block
		return sess.TryPublishAgentSurface(req), nil
	}}
	sess.SetSurfaceWidener(widener.fn)

	for i := 0; i < 3; i++ {
		if _, err := sess.StageToolAdmission([]string{"grep"}, 0); err != nil {
			t.Fatalf("stage admission %d: %v", i, err)
		}
		sess.mu.Lock()
		sess.activeTurns = 1
		sess.mu.Unlock()
		sess.PublishPendingAdmission()
		if n := strings.Count(sess.SystemPrompt, "<core-memory-context>"); n != 1 {
			t.Fatalf("after admission %d: %d opening tags, want exactly 1 (Bug 1 regression):\n%s", i, n, sess.SystemPrompt)
		}
	}
}

// TestPublishAgentSurfaceAndSetAgentSettingsShareBaseTracking confirms
// PublishAgentSurface and SetAgentSettings interoperate through the same
// BaseSystemPrompt field - an /agent switch (PublishAgentSurface) followed
// by a tool admission (SetAgentSettings) must not duplicate either's block.
func TestPublishAgentSurfaceAndSetAgentSettingsShareBaseTracking(t *testing.T) {
	s := newAgentSettingsTestSession(t)
	block := "- promoted fact: worth remembering"

	s.PublishAgentSurface("agent prompt", 4, nil, nil, nil, block)
	if n := strings.Count(s.SystemPrompt, "<core-memory-context>"); n != 1 {
		t.Fatalf("after PublishAgentSurface: %d opening tags, want 1:\n%s", n, s.SystemPrompt)
	}

	base, maxSteps := s.AgentSettings()
	if strings.Contains(base, "core-memory-context") {
		t.Fatalf("AgentSettings() after PublishAgentSurface leaked the block:\n%s", base)
	}
	s.SetAgentSettings(base, maxSteps, block)
	if n := strings.Count(s.SystemPrompt, "<core-memory-context>"); n != 1 {
		t.Fatalf("after PublishAgentSurface then SetAgentSettings: %d opening tags, want 1:\n%s", n, s.SystemPrompt)
	}
}
