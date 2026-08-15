package chat

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func newAgentSettingsTestSession(t *testing.T) *Session {
	t.Helper()
	return NewSession(&config.Resolved{Model: "m", SystemPrompt: "you are a helpful agent", MaxSteps: intpSession(4)}, &fakeCompleter{})
}

// memoryMessageCount counts frames anywhere in history; memoryMessageIndex
// returns the index of the first one, or -1.
func memoryMessageCount(s *Session) int {
	n := 0
	for _, m := range s.MessagesCopy() {
		if isMemoryContextMessage(m) {
			n++
		}
	}
	return n
}

func memoryMessageIndex(s *Session) int {
	for i, m := range s.MessagesCopy() {
		if isMemoryContextMessage(m) {
			return i
		}
	}
	return -1
}

// TestMemoryBlockNeverEntersSystemPrompt pins the cache-locality redesign:
// the core-memory block must never be composed into the system prompt (the
// first explicitly cache-marked block), because that made every memory
// promotion invalidate tools + system + the whole history cache. It rides as
// a separate user-role message at index 1, right after the system message
// and before the first real user objective.
func TestMemoryBlockNeverEntersSystemPrompt(t *testing.T) {
	s := newAgentSettingsTestSession(t)
	s.SetAgentSettings("root prompt", 4, "- promoted fact: worth remembering")

	if strings.Contains(s.SystemPrompt, "core-memory-context") {
		t.Fatalf("SystemPrompt must stay memory-block-free:\n%s", s.SystemPrompt)
	}
	if s.SystemPrompt != s.BaseSystemPrompt {
		t.Fatalf("SystemPrompt %q and BaseSystemPrompt %q must be equal now that memory rides in its own message", s.SystemPrompt, s.BaseSystemPrompt)
	}
	msgs := s.MessagesCopy()
	if len(msgs) < 2 || msgs[0].Role != provider.RoleSystem {
		t.Fatalf("expected system message at index 0 and memory message after it, got %d messages", len(msgs))
	}
	if !isMemoryContextMessage(msgs[1]) {
		t.Fatalf("memory context message must sit at index 1, got role=%s content=%q", msgs[1].Role, msgs[1].Content)
	}
	if !strings.Contains(msgs[1].Content, "promoted fact") {
		t.Fatalf("memory message missing the promoted entry:\n%s", msgs[1].Content)
	}

	base, _ := s.AgentSettings()
	if base != "root prompt" {
		t.Fatalf("AgentSettings() = %q, want the memory-free base %q", base, "root prompt")
	}
}

// TestMemoryMessagePrecedesFirstRealUserMessage: after a user turn is in
// history, a memory refresh must keep the frame at index 1, before the real
// user objective, and never duplicate it or append a second frame.
func TestMemoryMessagePrecedesFirstRealUserMessage(t *testing.T) {
	s := newAgentSettingsTestSession(t)
	s.SetAgentSettings("root prompt", 4, "- fact one")
	s.mu.Lock()
	s.Messages = append(s.Messages,
		provider.Message{Role: provider.RoleUser, Content: "real objective"},
		provider.Message{Role: provider.RoleAssistant, Content: "reply"})
	s.mu.Unlock()

	s.SetAgentSettings("root prompt", 4, "- fact two")

	msgs := s.MessagesCopy()
	if !isMemoryContextMessage(msgs[1]) {
		t.Fatalf("memory message must stay at index 1 after refresh, got %q", msgs[1].Content)
	}
	if !strings.Contains(msgs[1].Content, "fact two") {
		t.Fatalf("memory refresh did not update the frame in place:\n%s", msgs[1].Content)
	}
	if n := memoryMessageCount(s); n != 1 {
		t.Fatalf("after refresh: %d memory messages, want exactly 1", n)
	}
	if msgs[2].Content != "real objective" {
		t.Fatalf("real user objective must directly follow the memory message, got %q", msgs[2].Content)
	}
}

// TestSetAgentSettingsReadModifyWriteNeverDuplicatesBlock is the literal
// AR-1 regression, restated for the message-based delivery: repeated
// read-modify-write cycles (tool_admission.go's applyDeferredToolPrompt,
// agent_switch.go's selectedAgentSettings) must leave exactly one memory
// message and a block-free prompt.
func TestSetAgentSettingsReadModifyWriteNeverDuplicatesBlock(t *testing.T) {
	s := newAgentSettingsTestSession(t)
	block := "- promoted fact: worth remembering"

	s.SetAgentSettings("root prompt", 4, block)
	for i := 0; i < 2; i++ {
		base, maxSteps := s.AgentSettings()
		base = base + "\n\ndeferred tool index: foo, bar"
		s.SetAgentSettings(base, maxSteps, block)
		if n := memoryMessageCount(s); n != 1 {
			t.Fatalf("after read-modify-write cycle %d: %d memory messages, want exactly 1 (AR-1 regression)", i, n)
		}
		if strings.Contains(s.SystemPrompt, "core-memory-context") {
			t.Fatalf("read-modify-write leaked the block into the prompt:\n%s", s.SystemPrompt)
		}
	}
	if !strings.Contains(s.SystemPrompt, "deferred tool index: foo, bar") {
		t.Fatalf("the appended tail was lost:\n%s", s.SystemPrompt)
	}
}

// TestSetAgentSettingsEmptyBlockClearsStaleContent is the literal AR-2
// regression: a demote/delete/InjectCore-off must not leave a stale memory
// message behind.
func TestSetAgentSettingsEmptyBlockClearsStaleContent(t *testing.T) {
	s := newAgentSettingsTestSession(t)
	s.SetAgentSettings("root prompt", 4, "- promoted fact: worth remembering")
	if memoryMessageCount(s) != 1 {
		t.Fatalf("setup: expected a memory message")
	}

	s.SetAgentSettings("root prompt", 4, "")
	if n := memoryMessageCount(s); n != 0 {
		t.Fatalf("stale memory message survived an empty-block publish (AR-2 regression): %d frames", n)
	}
	if s.SystemPrompt != "root prompt" {
		t.Fatalf("SystemPrompt = %q, want exactly the base prompt", s.SystemPrompt)
	}
}

// TestPastedFrameShapedUserMessageIsNeverAdopted pins the frame-adoption
// fix: a real user message pasted to look byte-for-byte like the memory
// frame carries no sentinel Name, so setMemoryMessageLocked must never
// overwrite it (memory publish) and never delete it (empty-block publish).
func TestPastedFrameShapedUserMessageIsNeverAdopted(t *testing.T) {
	pasted := MemoryContextContent("- attacker-shaped or innocently pasted text")

	// Overwrite path: the look-alike sits at the frame position (index 1).
	s := newAgentSettingsTestSession(t)
	s.mu.Lock()
	s.Messages = append(s.Messages, provider.Message{Role: provider.RoleUser, Content: pasted})
	s.mu.Unlock()
	s.SetAgentSettings("root prompt", 4, "- real promoted fact")
	msgs := s.MessagesCopy()
	found := false
	for _, m := range msgs {
		if m.Name == "" && m.Content == pasted {
			found = true
		}
	}
	if !found {
		t.Fatalf("pasted frame-shaped user message was overwritten or deleted: %+v", msgs)
	}
	if memoryMessageCount(s) != 1 {
		t.Fatalf("expected exactly 1 session-owned frame, got %d", memoryMessageCount(s))
	}

	// Delete path: empty-block publish must remove only the named frame.
	s.SetAgentSettings("root prompt", 4, "")
	if memoryMessageCount(s) != 0 {
		t.Fatal("named frame survived the empty-block publish")
	}
	found = false
	for _, m := range s.MessagesCopy() {
		if m.Name == "" && m.Content == pasted {
			found = true
		}
	}
	if !found {
		t.Fatal("pasted frame-shaped user message was deleted by the empty-block publish")
	}
}

// TestSessionOwnedFrameCarriesSentinelName pins that every insertion path
// stamps MemoryContextMessageName, so ownership matching by Name works.
func TestSessionOwnedFrameCarriesSentinelName(t *testing.T) {
	s := newAgentSettingsTestSession(t)
	s.SetAgentSettings("root prompt", 4, "- promoted fact")
	idx := memoryMessageIndex(s)
	if idx < 0 {
		t.Fatal("no memory frame inserted")
	}
	if got := s.MessagesCopy()[idx].Name; got != MemoryContextMessageName {
		t.Fatalf("frame Name = %q, want %q", got, MemoryContextMessageName)
	}
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	idx = memoryMessageIndex(s)
	if idx < 0 {
		t.Fatal("clear dropped the memory frame")
	}
	if got := s.MessagesCopy()[idx].Name; got != MemoryContextMessageName {
		t.Fatalf("post-clear frame Name = %q, want %q", got, MemoryContextMessageName)
	}
}

// TestClearKeepsMemoryMessage: /clear reseeds the system prompt and must
// keep the memory context message with it - both are session surface, not
// conversation history.
func TestClearKeepsMemoryMessage(t *testing.T) {
	s := newAgentSettingsTestSession(t)
	s.SetAgentSettings("root prompt", 4, "- promoted fact: worth remembering")
	s.mu.Lock()
	s.Messages = append(s.Messages, provider.Message{Role: provider.RoleUser, Content: "real objective"})
	s.mu.Unlock()

	if err := s.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	msgs := s.MessagesCopy()
	if len(msgs) != 2 {
		t.Fatalf("after /clear: %d messages, want system + memory", len(msgs))
	}
	if msgs[0].Role != provider.RoleSystem || !isMemoryContextMessage(msgs[1]) {
		t.Fatalf("after /clear: want [system, memory], got roles %s/%s", msgs[0].Role, msgs[1].Role)
	}
}

// TestToolAdmissionNeverDuplicatesMemoryBlockAcrossRepeatedAdmissions is the
// literal Bug 1 regression from Step 5 hostile audit of the plan 77
// implementation, restated: repeated widener publications with the same
// memory block must keep exactly one memory message at index 1 and a
// block-free prompt.
func TestToolAdmissionNeverDuplicatesMemoryBlockAcrossRepeatedAdmissions(t *testing.T) {
	sess := newAdmissionSession(t)
	block := "- promoted fact: worth remembering"
	sess.SetAgentSettings("root prompt", 4, block)
	if n := memoryMessageCount(sess); n != 1 {
		t.Fatalf("setup: %d memory messages, want 1", n)
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
		if n := memoryMessageCount(sess); n != 1 {
			t.Fatalf("after admission %d: %d memory messages, want exactly 1 (Bug 1 regression)", i, n)
		}
		if idx := memoryMessageIndex(sess); idx != 1 {
			t.Fatalf("after admission %d: memory message at index %d, want 1", i, idx)
		}
		if strings.Contains(sess.SystemPrompt, "core-memory-context") {
			t.Fatalf("after admission %d: block leaked into the prompt:\n%s", i, sess.SystemPrompt)
		}
	}
}

// TestPublishAgentSurfaceAndSetAgentSettingsShareBaseTracking confirms
// PublishAgentSurface and SetAgentSettings interoperate through the same
// BaseSystemPrompt field and memory-message maintenance - an /agent switch
// (PublishAgentSurface) followed by a tool admission (SetAgentSettings)
// must not duplicate either's block.
func TestPublishAgentSurfaceAndSetAgentSettingsShareBaseTracking(t *testing.T) {
	s := newAgentSettingsTestSession(t)
	block := "- promoted fact: worth remembering"

	s.PublishAgentSurface("agent prompt", 4, nil, nil, nil, block, nil)
	if n := memoryMessageCount(s); n != 1 {
		t.Fatalf("after PublishAgentSurface: %d memory messages, want 1", n)
	}

	base, maxSteps := s.AgentSettings()
	if strings.Contains(base, "core-memory-context") {
		t.Fatalf("AgentSettings() after PublishAgentSurface leaked the block:\n%s", base)
	}
	s.SetAgentSettings(base, maxSteps, block)
	if n := memoryMessageCount(s); n != 1 {
		t.Fatalf("after PublishAgentSurface then SetAgentSettings: %d memory messages, want 1", n)
	}
}
