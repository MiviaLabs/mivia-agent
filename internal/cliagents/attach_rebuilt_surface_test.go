package cliagents

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
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

// TestAttachRebuiltSurface_DefaultsEmptyWorkspaceRootToDot pins the
// WorkspaceRoot == "" fallback: a rebuild with no cached skill registry
// AND no workspace root configured must still succeed, loading skills
// against "." rather than erroring or crashing on an empty root.
func TestAttachRebuiltSurface_DefaultsEmptyWorkspaceRootToDot(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file"})
	fixture.state.SkillRegFull = nil
	fixture.state.WorkspaceRoot = ""

	published, err := AttachRebuiltSurface(fixture.sess, fixture.res, fixture.state)
	if err != nil {
		t.Fatalf("AttachRebuiltSurface with an empty WorkspaceRoot: %v", err)
	}
	if !published {
		t.Fatal("AttachRebuiltSurface did not report a publication")
	}
	if fixture.state.SkillRegFull == nil {
		t.Fatal("AttachRebuiltSurface left SkillRegFull nil after a rebuild")
	}
}

// TestAttachRebuiltSurface_WidenerErrorSurfaces pins the final
// NewSurfaceWidener(...)(...) error wrap: a state with no available tool
// base (mirroring TestBuildWidenedWithRejectsUnavailableToolBase's own
// precondition) makes buildWidenedWith fail inside the widener closure.
func TestAttachRebuiltSurface_WidenerErrorSurfaces(t *testing.T) {
	res := &config.Resolved{Model: "m", ProviderName: "p"}
	sess := chat.NewSession(res, stubAgentCompleter{})
	state := &AgentSessionState{
		SkillRegFull: skills.NewRegistry(),
		TierPlan:     ToolTierPlan{Tiers: tools.Tiers{Core: []string{"placeholder"}}},
	}
	published, err := AttachRebuiltSurface(sess, res, state)
	if err == nil {
		t.Fatal("AttachRebuiltSurface accepted a state with no available tool base")
	}
	if published {
		t.Fatal("AttachRebuiltSurface reported a publication despite the widener error")
	}
	if !strings.Contains(err.Error(), "attach rebuilt surface") {
		t.Fatalf("err = %v, want the attach-rebuilt-surface wrap", err)
	}
}
