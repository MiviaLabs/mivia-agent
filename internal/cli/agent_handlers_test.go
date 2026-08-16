package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
)

// TestApplySelectedAgentPromptInjectsMemoryWithNoAgentSelected is a
// regression for a gap a live smoke test found in plan 77's implementation:
// applySelectedAgentPrompt used to return immediately when selected was nil
// (the common case - a bare `mivia chat` with no --agent flag), so it never
// called sess.SetAgentSettings and core-tier memory injection never
// happened for the single most common invocation shape. chat_command.go's
// own fallback prompt resolution runs BEFORE the memory store opens and is
// hardcoded to no injection, so this call site, right after
// configureChatWorkspace, was the only chance to inject for a no-agent
// session - and it was skipped entirely.
func TestApplySelectedAgentPromptInjectsMemoryWithNoAgentSelected(t *testing.T) {
	root := t.TempDir()
	res := memoryTestResolved(true)
	res.Memory.InjectCore = true
	sess := chat.NewSession(res, nil)

	state := &agentSessionState{}
	memClose, err := configureChatWorkspace(sess, root, true, res, state, false, false, false)
	if err != nil {
		t.Fatalf("configureChatWorkspace: %v", err)
	}
	defer memClose()

	saveRes, err := state.Memory.Save(context.Background(), memory.Entry{
		Title: "promoted fact", Scope: memory.ScopeProject, Verdict: memory.VerdictGood,
		Summary: "worth remembering", Why: "test",
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := state.Memory.PromoteToCore(context.Background(), saveRes.ID); err != nil {
		t.Fatalf("promote: %v", err)
	}

	// selected is nil - the no-agent case the fix targets.
	applySelectedAgentPrompt(sess, res, nil, state)

	// The block never enters the system prompt anymore (cache locality):
	// it rides in a dedicated user-role message right after the system
	// message. Assert on the conversation, not the prompt.
	if strings.Contains(sess.SystemPrompt, "core-memory-context") {
		t.Fatalf("memory block leaked into the system prompt:\n%s", sess.SystemPrompt)
	}
	var memoryMsg string
	for _, m := range sess.MessagesCopy() {
		if strings.Contains(m.Content, "<core-memory-context>") {
			memoryMsg = m.Content
			break
		}
	}
	if memoryMsg == "" {
		t.Fatalf("no-agent session did not get core-memory injection as a message")
	}
	if !strings.Contains(memoryMsg, "promoted fact") {
		t.Fatalf("injected block missing the promoted entry:\n%s", memoryMsg)
	}
}

// TestApplySelectedAgentPromptStillAppliesSelectedAgentSettings confirms
// the fix didn't change behavior for the selected-agent case: the agent's
// own SystemPrompt/MaxTurns must still win.
func TestApplySelectedAgentPromptStillAppliesSelectedAgentSettings(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{Model: "m"}, nil)
	maxTurns := 7
	selected := &agents.ResolvedAgent{SystemPrompt: "agent prompt", MaxTurns: &maxTurns}
	applySelectedAgentPrompt(sess, nil, selected, nil)

	if !strings.Contains(sess.SystemPrompt, "agent prompt") {
		t.Fatalf("selected agent's own prompt was not applied:\n%s", sess.SystemPrompt)
	}
	if sess.MaxSteps != maxTurns {
		t.Fatalf("MaxSteps = %d, want %d", sess.MaxSteps, maxTurns)
	}
}
