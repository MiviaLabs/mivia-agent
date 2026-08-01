package agents

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// Source-boundary enforcement (INV-AG-29 / plan 05 phase 03):
// inheritance may only occur within one trust origin. A USER agent
// (~/.mivia/agents/) must NOT inherit a WORKSPACE agent (<ws>/.mivia/agents/),
// and a WORKSPACE agent must NOT inherit a USER agent. Both directions must
// fail closed with an error naming the source boundary.
//
// These are RED tests: the current resolver returns nil
// (checkInheritanceSourceBoundary is a literal no-op and resolveParent
// resolves purely by name), so both tests fail until the boundary is enforced.
func TestCrossSource_UserChildInheritsWorkspaceParent(t *testing.T) {
	inputs := []ResolveInput{
		{
			Name:   "ws_parent",
			Source: config.AgentSourceWorkspace,
			Path:   "/repo/.mivia/agents/ws_parent.toml",
			Spec: config.AgentFileSpec{
				Name:         strp("ws_parent"),
				Description:  strp("workspace parent"),
				Tools:        slicep("read_file", "grep", "glob"),
				SystemPrompt: strp("workspace-controlled prompt"),
			},
		},
		{
			Name:   "user_child",
			Source: config.AgentSourceUser,
			Path:   "/home/u/.mivia/agents/user_child.toml",
			Spec: config.AgentFileSpec{
				Name:        strp("user_child"),
				Description: strp("user child"),
				Inherits:    strp("ws_parent"),
			},
		},
	}
	reg, _, err := ResolveAll(inputs, baseOpts())
	if err == nil {
		child, ok := reg.Get("user_child")
		if !ok {
			t.Fatal("user_child not published")
		}
		t.Fatalf("source boundary unenforced: user agent inherited workspace agent "+
			"(effective tools=%v, system prompt=%q); want error containing 'source' or 'inheritance'",
			child.EffectiveTools, child.SystemPrompt)
	}
	if !strings.Contains(err.Error(), "source") && !strings.Contains(err.Error(), "inheritance") {
		t.Fatalf("want source-boundary error, got: %v", err)
	}
}

func TestCrossSource_WorkspaceChildInheritsUserParent(t *testing.T) {
	inputs := []ResolveInput{
		{
			Name:   "user_parent",
			Source: config.AgentSourceUser,
			Path:   "/home/u/.mivia/agents/user_parent.toml",
			Spec: config.AgentFileSpec{
				Name:         strp("user_parent"),
				Description:  strp("user parent"),
				Tools:        slicep("read_file", "grep", "glob"),
				SystemPrompt: strp("user-trusted prompt"),
			},
		},
		{
			Name:   "ws_child",
			Source: config.AgentSourceWorkspace,
			Path:   "/repo/.mivia/agents/ws_child.toml",
			Spec: config.AgentFileSpec{
				Name:        strp("ws_child"),
				Description: strp("workspace child"),
				Inherits:    strp("user_parent"),
			},
		},
	}
	reg, _, err := ResolveAll(inputs, baseOpts())
	if err == nil {
		child, ok := reg.Get("ws_child")
		if !ok {
			t.Fatal("ws_child not published")
		}
		t.Fatalf("source boundary unenforced: workspace agent inherited user agent "+
			"(effective tools=%v, system prompt=%q); want error containing 'source' or 'inheritance'",
			child.EffectiveTools, child.SystemPrompt)
	}
	if !strings.Contains(err.Error(), "source") && !strings.Contains(err.Error(), "inheritance") {
		t.Fatalf("want source-boundary error, got: %v", err)
	}
}
