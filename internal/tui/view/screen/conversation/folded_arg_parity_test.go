package conversation

import (
	"reflect"
	"testing"

	agentpkg "github.com/MiviaLabs/mivia-agent/internal/agent"
)

// Folded-key resolution exists twice, by settled import-layer policy: the
// operator surface's foldedArg (this package) and the dispatch preview's
// foldedTaskArg (internal/agent). The preview is the payload this package
// re-parses, so the two must pick the same value for every key or the row
// ids built here stop matching the preview's ids - the stalled-row/dead-
// cancel defect class. Nothing else in the build connects the two helpers,
// so this test IS the connection: it runs one table through both and
// fails on the first divergence, before a drift can ship.
//
// The import is test-only; scripts/check_import_layers.py scopes edges to
// non-test files, so this gate adds no policy edge.
func TestFoldedArgMatchesAgentPreviewFold(t *testing.T) {
	cases := []struct {
		name string
		m    map[string]any
		keys []string
	}{
		{
			name: "exact and folded spelling of one key",
			m:    map[string]any{"id": "a", "ID": "b", "Agent": "auditor"},
			keys: []string{"id", "agent", "ID"},
		},
		{
			name: "two non-exact folded spellings",
			m:    map[string]any{"Id": "x", "iD": "y"},
			keys: []string{"id"},
		},
		{
			name: "subagent spellings plus unrelated keys",
			m:    map[string]any{"Subagent": "go", "subagent": "rev", "role": "builder", "Skill": "capture"},
			keys: []string{"subagent", "ROLE", "skill"},
		},
		{
			name: "non-string values survive identically",
			m:    map[string]any{"id": 7, "Workflow": true},
			keys: []string{"id", "workflow"},
		},
		{
			name: "nil value vs absent key",
			m:    map[string]any{"id": nil, "agent": "left"},
			keys: []string{"id", "agent", "name"},
		},
		{
			name: "empty map",
			m:    map[string]any{},
			keys: []string{"id", "agent", "type"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, key := range tc.keys {
				want := foldedArg(tc.m, key)
				got := agentpkg.FoldedTaskArgForTest(tc.m, key)
				if !reflect.DeepEqual(want, got) {
					t.Fatalf("foldedArg(%q) = %#v but agent preview picked %#v; the two fold helpers have drifted",
						key, want, got)
				}
			}
		})
	}
}
