package agents

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// TestLoadAndResolve exercises the Layer-B discovery+resolve entry point:
// agent files discovered under the workspace are parsed and resolved into an
// immutable registry, and skill collisions / malformed files fail closed.
// TestLoadAndResolveSuccess exercises success paths for LoadAndResolve.
func TestLoadAndResolveSuccess(t *testing.T) {
	tests := []struct {
		name       string
		agents     map[string]string // filename → body, written under ws/.mivia/agents
		skillNames map[string]struct{}
		wantNames  []string
	}{
		{
			name:      "empty workspace yields empty registry",
			wantNames: nil,
		},
		{
			name: "workspace agent resolves",
			agents: map[string]string{
				"worker.toml": "name = \"worker\"\ndescription = \"a worker\"\ntools = [\"read_file\"]\n",
			},
			wantNames: []string{"worker"},
		},
		{
			name: "unrelated skill names pass through",
			agents: map[string]string{
				"worker.toml": "name = \"worker\"\ndescription = \"w\"\ntools = [\"read_file\"]\n",
			},
			skillNames: map[string]struct{}{"code_review": {}},
			wantNames:  []string{"worker"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home, ws := t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			for filename, body := range tc.agents {
				writeAgent(t, config.WorkspaceAgentsDir(ws), filename, body)
			}

			reg, global, warnings, err := LoadAndResolve(ws, tc.skillNames)
			if err != nil {
				t.Fatalf("LoadAndResolve error = %v", err)
			}
			if len(warnings) != 0 {
				t.Fatalf("unexpected warnings: %v", warnings)
			}
			if !global.FailOnEmptyToolset {
				t.Fatal("default global must keep fail_on_empty_toolset on")
			}
			if reg == nil {
				t.Fatal("registry is nil on success")
			}
			if len(tc.wantNames) == 0 {
				if reg.Len() != 0 {
					t.Fatalf("registry = %v, want empty", reg.Names())
				}
				return
			}
			for _, name := range tc.wantNames {
				if _, ok := reg.Get(name); !ok {
					t.Fatalf("agent %q missing from registry %v", name, reg.Names())
				}
			}
			if reg.Len() != len(tc.wantNames) {
				t.Fatalf("registry len = %d, want %d (%v)", reg.Len(), len(tc.wantNames), reg.Names())
			}
		})
	}
}

// TestLoadAndResolveErrors exercises failure paths for LoadAndResolve.
func TestLoadAndResolveErrors(t *testing.T) {
	tests := []struct {
		name       string
		agents     map[string]string
		skillNames map[string]struct{}
		wantErr    string // substring
	}{
		{
			name: "malformed agent file fails load",
			agents: map[string]string{
				"bad.toml": "name = \"bad\"\nunknown = true\n",
			},
			wantErr: "strict mode",
		},
		{
			name: "agent name colliding with skill refused",
			agents: map[string]string{
				"researcher.toml": "name = \"researcher\"\ndescription = \"r\"\ntools = [\"read_file\"]\n",
			},
			skillNames: map[string]struct{}{"researcher": {}},
			wantErr:    "collides with a skill",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home, ws := t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			for filename, body := range tc.agents {
				writeAgent(t, config.WorkspaceAgentsDir(ws), filename, body)
			}

			_, _, _, err := LoadAndResolve(ws, tc.skillNames)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("LoadAndResolve error = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}
