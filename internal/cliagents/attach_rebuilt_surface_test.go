package cliagents

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// TestAttachRebuiltSurface_NoCompleterIsSilentNoop pins the no-completer
// guard, distinct from the nil-sess/nil-state guard above it: a session
// with no wired completer at all must not attempt a rebuild.
func TestAttachRebuiltSurface_NoCompleterIsSilentNoop(t *testing.T) {
	res := &config.Resolved{Model: "m", ProviderName: "p"}
	sess := chat.NewSession(res, nil)
	state := &AgentSessionState{}
	published, err := AttachRebuiltSurface(sess, res, state)
	if err != nil {
		t.Fatalf("AttachRebuiltSurface with no completer: %v", err)
	}
	if published {
		t.Fatal("AttachRebuiltSurface with no completer must not report a publication")
	}
}

// TestAttachRebuiltSurface_ReloadsSkillsWhenMissing pins the SkillRegFull-nil
// branch: a rebuild with no cached skill registry must load one (defaulting
// root to "." when WorkspaceRoot is empty) rather than publish with none.
func TestAttachRebuiltSurface_ReloadsSkillsWhenMissing(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file"})
	fixture.state.SkillRegFull = nil

	published, err := AttachRebuiltSurface(fixture.sess, fixture.res, fixture.state)
	if err != nil {
		t.Fatalf("AttachRebuiltSurface: %v", err)
	}
	if !published {
		t.Fatal("AttachRebuiltSurface did not report a publication")
	}
	if fixture.state.SkillRegFull == nil {
		t.Fatal("AttachRebuiltSurface left SkillRegFull nil after a rebuild")
	}
}
