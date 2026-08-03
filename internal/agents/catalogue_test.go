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

// TestLoadAndResolveErrors exercises failure paths for LoadAndResolve. The
// user agent directory is the trusted boundary and stays fail-closed: a
// malformed user agent file or a user agent colliding with a skill name is
// still a hard error, while the corresponding workspace failures are tolerated
// (see TestLoadAndResolveToleratesMalformedWorkspaceFile and the wantSkip
// cases below).
func TestLoadAndResolveErrors(t *testing.T) {
	tests := []struct {
		name       string
		userAgents map[string]string // filename → body, written under ~/.mivia/agents
		agents     map[string]string // filename → body, written under ws/.mivia/agents
		skillNames map[string]struct{}
		wantErr    string // substring; empty means the case must resolve tolerantly
		wantSkip   string // agent name expected to be skipped (tolerance cases only)
	}{
		{
			name: "malformed user agent file fails load",
			userAgents: map[string]string{
				"bad.toml": "name = \"bad\"\nunknown = true\n",
			},
			wantErr: "strict mode",
		},
		{
			name: "workspace agent name colliding with skill is skipped",
			agents: map[string]string{
				"researcher.toml": "name = \"researcher\"\ndescription = \"r\"\ntools = [\"read_file\"]\n",
			},
			skillNames: map[string]struct{}{"researcher": {}},
			wantSkip:   "researcher",
		},
		{
			name: "user agent name colliding with skill still fails",
			userAgents: map[string]string{
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
			for filename, body := range tc.userAgents {
				writeAgent(t, config.UserAgentsDir(), filename, body)
			}
			for filename, body := range tc.agents {
				writeAgent(t, config.WorkspaceAgentsDir(ws), filename, body)
			}

			reg, _, warnings, err := LoadAndResolve(ws, tc.skillNames)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("LoadAndResolve error = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadAndResolve error = %v, want tolerance", err)
			}
			if reg == nil {
				t.Fatal("registry is nil on success")
			}
			if _, ok := reg.Get(tc.wantSkip); ok {
				t.Fatalf("agent %q must not be published: %v", tc.wantSkip, reg.Names())
			}
			joined := strings.Join(warnings, " ")
			if !strings.Contains(joined, "skipped workspace agent") || !strings.Contains(joined, tc.wantSkip) {
				t.Fatalf("warnings = %q, want a skip notice naming %q", joined, tc.wantSkip)
			}
		})
	}
}

// TestLoadAndResolveToleratesMalformedWorkspaceFile pins INV-AG-34: a
// malformed workspace agent file next to a valid one must not abort startup.
// The valid agent loads, the malformed file is reported as a class-only skip
// warning, and no error is returned.
func TestLoadAndResolveToleratesMalformedWorkspaceFile(t *testing.T) {
	home, ws := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	writeAgent(t, config.WorkspaceAgentsDir(ws), "researcher.toml", "name = \"researcher\"\ndescription = \"r\"\ntools = [\"read_file\"]\n")
	writeAgent(t, config.WorkspaceAgentsDir(ws), "bad.toml", "name = \"bad\"\nunknown = true\n")

	reg, _, warnings, err := LoadAndResolve(ws, nil)
	if err != nil {
		t.Fatalf("LoadAndResolve error = %v, want success despite malformed workspace file", err)
	}
	if reg == nil {
		t.Fatal("registry is nil on success")
	}
	if _, ok := reg.Get("researcher"); !ok {
		t.Fatalf("valid agent missing from registry %v", reg.Names())
	}
	if _, ok := reg.Get("bad"); ok {
		t.Fatalf("malformed agent must not be published: %v", reg.Names())
	}
	if len(warnings) == 0 {
		t.Fatal("expected a warning for the skipped malformed workspace file")
	}
	joined := strings.Join(warnings, " ")
	if !strings.Contains(joined, "skipped") || !strings.Contains(joined, "bad") {
		t.Fatalf("warnings = %q, want a skip notice naming the bad file", joined)
	}
}

// TestLoadAndResolveToleratesUnknownToolWorkspaceAgent pins INV-AG-34 for the
// resolution boundary: a parseable workspace agent that references an unknown
// tool must not abort startup. The valid sibling loads, the unknown-tool agent
// is skipped with a warning, and no error is returned.
func TestLoadAndResolveToleratesUnknownToolWorkspaceAgent(t *testing.T) {
	home, ws := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	writeAgent(t, config.WorkspaceAgentsDir(ws), "researcher.toml", "name = \"researcher\"\ndescription = \"r\"\ntools = [\"read_file\"]\n")
	writeAgent(t, config.WorkspaceAgentsDir(ws), "broken.toml", "name = \"broken\"\ndescription = \"b\"\ntools = [\"read_file\", \"no_such_tool\"]\n")

	reg, _, warnings, err := LoadAndResolve(ws, nil)
	if err != nil {
		t.Fatalf("LoadAndResolve error = %v, want success despite unknown tool in workspace agent", err)
	}
	if reg == nil {
		t.Fatal("registry is nil on success")
	}
	if _, ok := reg.Get("researcher"); !ok {
		t.Fatalf("valid agent missing from registry %v", reg.Names())
	}
	if _, ok := reg.Get("broken"); ok {
		t.Fatalf("unknown-tool agent must not be published: %v", reg.Names())
	}
	joined := strings.Join(warnings, " ")
	if !strings.Contains(joined, "skipped workspace agent") || !strings.Contains(joined, "broken") {
		t.Fatalf("warnings = %q, want a skip notice naming the unknown-tool agent", joined)
	}
}

// TestLoadAndResolveEmptyToolsetWorkspaceTolerated pins the empty-toolset
// boundary: a workspace agent with tools=[] is skipped with a warning while a
// valid sibling loads, and no error is returned.
func TestLoadAndResolveEmptyToolsetWorkspaceTolerated(t *testing.T) {
	home, ws := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	writeAgent(t, config.WorkspaceAgentsDir(ws), "researcher.toml", "name = \"researcher\"\ndescription = \"r\"\ntools = [\"read_file\"]\n")
	writeAgent(t, config.WorkspaceAgentsDir(ws), "empty.toml", "name = \"empty\"\ndescription = \"e\"\ntools = []\n")

	reg, _, warnings, err := LoadAndResolve(ws, nil)
	if err != nil {
		t.Fatalf("LoadAndResolve error = %v, want success despite empty toolset in workspace agent", err)
	}
	if reg == nil {
		t.Fatal("registry is nil on success")
	}
	if _, ok := reg.Get("researcher"); !ok {
		t.Fatalf("valid agent missing from registry %v", reg.Names())
	}
	if _, ok := reg.Get("empty"); ok {
		t.Fatalf("empty-toolset agent must not be published: %v", reg.Names())
	}
	joined := strings.Join(warnings, " ")
	if !strings.Contains(joined, "skipped workspace agent") || !strings.Contains(joined, "empty") {
		t.Fatalf("warnings = %q, want a skip notice naming the empty-toolset agent", joined)
	}
}
