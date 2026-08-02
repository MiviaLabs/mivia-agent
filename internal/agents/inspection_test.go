package agents

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// TestReplaceInspectionRow verifies that a diagnostic row is replaced in place
// when name+path match, and appended otherwise. Inspect uses this to promote a
// resolvable-but-failed file to the malformed class without losing independent
// rows for unrelated files.
func TestReplaceInspectionRow(t *testing.T) {
	loaded := config.AgentFileDiagnostic{
		Name: "a", Source: config.AgentSourceWorkspace, Path: "/ws/a.toml", State: config.AgentFileLoaded,
	}
	malformed := config.AgentFileDiagnostic{
		Name: "a", Source: config.AgentSourceWorkspace, Path: "/ws/a.toml", State: config.AgentFileMalformed,
	}
	other := config.AgentFileDiagnostic{
		Name: "b", Source: config.AgentSourceWorkspace, Path: "/ws/b.toml", State: config.AgentFileLoaded,
	}

	tests := []struct {
		name        string
		rows        []config.AgentFileDiagnostic
		replacement config.AgentFileDiagnostic
		wantLen     int
		wantState   map[string]config.AgentFileState // key: path
	}{
		{
			name:        "replaces existing row with matching name and path",
			rows:        []config.AgentFileDiagnostic{loaded, other},
			replacement: malformed,
			wantLen:     2,
			wantState:   map[string]config.AgentFileState{"/ws/a.toml": config.AgentFileMalformed, "/ws/b.toml": config.AgentFileLoaded},
		},
		{
			name:        "same name different path appends",
			rows:        []config.AgentFileDiagnostic{loaded},
			replacement: config.AgentFileDiagnostic{Name: "a", Source: config.AgentSourceUser, Path: "/user/a.toml", State: config.AgentFileMalformed},
			wantLen:     2,
			wantState:   map[string]config.AgentFileState{"/ws/a.toml": config.AgentFileLoaded, "/user/a.toml": config.AgentFileMalformed},
		},
		{
			name:        "different name appends",
			rows:        []config.AgentFileDiagnostic{loaded},
			replacement: config.AgentFileDiagnostic{Name: "c", Source: config.AgentSourceWorkspace, Path: "/ws/c.toml", State: config.AgentFileMalformed},
			wantLen:     2,
			wantState:   map[string]config.AgentFileState{"/ws/a.toml": config.AgentFileLoaded, "/ws/c.toml": config.AgentFileMalformed},
		},
		{
			name:        "empty rows appends",
			rows:        nil,
			replacement: malformed,
			wantLen:     1,
			wantState:   map[string]config.AgentFileState{"/ws/a.toml": config.AgentFileMalformed},
		},
		{
			name:        "identical row: only first occurrence replaced",
			rows:        []config.AgentFileDiagnostic{loaded, loaded},
			replacement: malformed,
			wantLen:     2,
			wantState:   nil, // can't express "first replaced, second unchanged" in map
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rows := append([]config.AgentFileDiagnostic(nil), tc.rows...)
			replaceInspectionRow(&rows, tc.replacement)
			if len(rows) != tc.wantLen {
				t.Fatalf("len(rows) = %d, want %d: %v", len(rows), tc.wantLen, rows)
			}
			for path, state := range tc.wantState {
				found := false
				for _, row := range rows {
					if row.Path == path {
						found = true
						if row.State != state {
							t.Errorf("row %q state = %q, want %q", path, row.State, state)
						}
					}
				}
				if !found {
					t.Errorf("row %q not present in %v", path, rows)
				}
			}
		})
	}
}
