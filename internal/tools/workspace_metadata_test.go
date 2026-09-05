package tools

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func TestRegistryDerivationsRetainLiveWorkspace(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	src := NewDefaultRegistry(DefaultOptions{Workspace: ws})
	cases := map[string]*Registry{
		"default":    src,
		"clone":      src.Clone(),
		"generation": src.CloneForGeneration(),
		"excluding":  src.CloneForGenerationExcluding("write_file"),
		"root":       ScopedRegistry(src, ScopeOptions{Mode: ScopeRoot}),
		"spawned":    ScopedRegistry(src, ScopeOptions{Mode: ScopeSpawned}),
		"empty":      ScopedRegistry(src, ScopeOptions{Allowlist: map[string]struct{}{}}),
		"tail":       ScopedRegistryWithTail(src, ScopeOptions{Allowlist: map[string]struct{}{}}, []string{"read_file"}),
	}
	for name, reg := range cases {
		t.Run(name, func(t *testing.T) {
			if got := reg.WorkspaceRoot(); got != ws.Abs {
				t.Errorf("root = %q, want %q", got, ws.Abs)
			}
			for _, on := range []bool{false, true, false} {
				ws.SetUnrestricted(on)
				if got := reg.WorkspaceUnrestricted(); got != on {
					t.Errorf("live access = %v, want %v", got, on)
				}
			}
		})
	}
}
