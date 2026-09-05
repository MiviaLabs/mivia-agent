package cliagents

// agent_surface_defaults_test.go covers the fallback branches of
// agent_surface.go: the empty-workspace-root default in
// buildAgentScopedSurface, and the unwired-remainder-spool seam in
// dispatcherOptsForSurface.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestBuildAgentScopedSurfaceDefaultsRootToWorkingDirectory pins the fallback
// a state with no recorded workspace root takes: skills must be loaded from
// the process working directory, not from an empty path (which loads nothing
// and would silently strip every project skill from the binding).
func TestBuildAgentScopedSurfaceDefaultsRootToWorkingDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()
	skillDir := filepath.Join(root, ".agents", "skills", "cwdskill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: cwdskill\ndescription: loaded from the working directory\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	res := &config.Resolved{Model: "m", ProviderName: "p"}
	sess := chat.NewSession(res, stubAgentCompleter{})
	state := &AgentSessionState{
		// WorkspaceRoot is deliberately empty: this is the branch under test.
		AllowProjectSkills: true,
		ToolBase:           tools.NewRegistry(),
	}
	selected := &agents.ResolvedAgent{Name: "dev"}
	surface, err := buildAgentScopedSurface(sess, res, state, selected)
	if err != nil {
		t.Fatalf("buildAgentScopedSurface: %v", err)
	}
	if surface.dispatcher != nil {
		t.Cleanup(func() { surface.dispatcher.Close() })
	}
	if surface.skillRegFull == nil {
		t.Fatal("skillRegFull = nil, want the registry loaded from the defaulted root")
	}
	def, ok := surface.skillRegFull.Get("cwdskill")
	if !ok {
		t.Fatal("the working directory's project skill was not loaded; the empty root did not default to \".\"")
	}
	if def.Origin != skills.OriginProject {
		t.Fatalf("origin = %q, want the project origin", def.Origin)
	}
}

// TestDispatcherOptsForSurfaceWithoutRemainderSpoolSeam pins the unwired-seam
// fallback: a process that never installed RemainderSpoolFromRegistryVar gets
// a nil spool rather than a panic, and a wired seam's spool is passed through
// so the surface keeps the session's existing truncated-output refs.
func TestDispatcherOptsForSurfaceWithoutRemainderSpoolSeam(t *testing.T) {
	prev := RemainderSpoolFromRegistryVar
	t.Cleanup(func() { RemainderSpoolFromRegistryVar = prev })
	RemainderSpoolFromRegistryVar = nil

	res := &config.Resolved{Model: "m", ProviderName: "p"}
	sess := chat.NewSession(res, stubAgentCompleter{})
	state := &AgentSessionState{ToolBase: tools.NewRegistry()}
	binding := sess.CurrentBinding()
	registry := tools.NewRegistry()

	opts := dispatcherOptsForSurface(sess, res, state, binding, registry, registry,
		skills.NewRegistry(), AgentSkillScope{}, ToolTierPlan{}, "/tmp/workspace")
	if opts.RemainderSpool != nil {
		t.Fatalf("RemainderSpool = %v, want nil when the seam is unwired", opts.RemainderSpool)
	}
	if opts.WorkspaceRoot != "/tmp/workspace" {
		t.Fatalf("WorkspaceRoot = %q, want the root passed in", opts.WorkspaceRoot)
	}

	// With the seam wired the spool it returns is carried onto the options, so
	// the nil above comes from the guard and not from a discarded field.
	sentinel := remainder.NewSpool(nil)
	RemainderSpoolFromRegistryVar = func(_ *tools.Registry) *remainder.Spool { return sentinel }
	opts = dispatcherOptsForSurface(sess, res, state, binding, registry, registry,
		skills.NewRegistry(), AgentSkillScope{}, ToolTierPlan{}, "/tmp/workspace")
	if opts.RemainderSpool != sentinel {
		t.Fatalf("RemainderSpool = %v, want the seam's spool", opts.RemainderSpool)
	}
}

// TestBuildAgentScopedSurfaceRejectsUnavailableToolBase covers
// buildAgentScopedSurface's entryBase==nil guard: a state with no ToolBase
// and a session with no ToolBaseResolver leaves entryBase() nothing to
// return.
func TestBuildAgentScopedSurfaceRejectsUnavailableToolBase(t *testing.T) {
	res := &config.Resolved{Model: "m", ProviderName: "p"}
	sess := chat.NewSession(res, stubAgentCompleter{})
	state := &AgentSessionState{AllowProjectSkills: false}
	if _, err := buildAgentScopedSurface(sess, res, state, &agents.ResolvedAgent{Name: "dev"}); err == nil {
		t.Fatal("buildAgentScopedSurface accepted a state with no available tool base")
	}
}

// TestBuildWidenedWithRejectsUnavailableToolBase covers buildWidenedWith's
// own entryBase==nil guard, same shape as buildAgentScopedSurface's but
// reached only once SkillRegFull is already captured.
func TestBuildWidenedWithRejectsUnavailableToolBase(t *testing.T) {
	res := &config.Resolved{Model: "m", ProviderName: "p"}
	sess := chat.NewSession(res, stubAgentCompleter{})
	state := &AgentSessionState{SkillRegFull: skills.NewRegistry()}
	if _, err := buildWidenedWith(sess, res, state, nil); err == nil {
		t.Fatal("buildWidenedWith accepted a state with no available tool base")
	}
}
