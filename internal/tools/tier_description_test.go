package tools_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// TestDeferredIndexRendersEveryShippedToolIntact walks the live default
// registry because the index one-liner is the only description the model ever
// sees for a deferred tool. Cutting at the first period truncated list_dir
// inside its own quoted default, leaving an unbalanced quote and dropping every
// parameter.
func TestDeferredIndexRendersEveryShippedToolIntact(t *testing.T) {
	dir := t.TempDir()
	// find_references registers only when a workspace looks like a project.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example\n\ngo 1.22\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewDefaultRegistry(tools.DefaultOptions{
		Workspace:    ws,
		TavilyAPIKey: "test-key-not-real",
		RunAllowlist: []string{"echo"},
	})
	listed := registry.List()
	if len(listed) == 0 {
		t.Fatal("default registry registered no tools")
	}
	for _, tool := range listed {
		name := tool.Name()
		index := tools.DeferredIndex([]tools.TierCandidate{{Name: name, Description: tool.Description()}})
		line := entryLine(t, index, name)
		if !strings.HasPrefix(line, "- "+name+": ") {
			t.Fatalf("%s rendered without a description: %q", name, line)
		}
		if strings.Count(line, `"`)%2 != 0 {
			t.Fatalf("%s one-liner has an unbalanced quote: %q", name, line)
		}
		if strings.Count(line, "(") != strings.Count(line, ")") {
			t.Fatalf("%s one-liner has unbalanced parentheses: %q", name, line)
		}
	}
}

// entryLine returns the index's bullet for name.
func entryLine(t *testing.T, index, name string) string {
	t.Helper()
	for _, line := range strings.Split(index, "\n") {
		if strings.HasPrefix(line, "- "+name) {
			return line
		}
	}
	t.Fatalf("index has no entry for %s: %q", name, index)
	return ""
}

// TestDeferredIndexKeepsQuotedAndAbbreviatedPeriods pins the cut rule itself:
// a period only ends the one-liner when it terminates a sentence outside any
// open quote or bracket.
func TestDeferredIndexKeepsQuotedAndAbbreviatedPeriods(t *testing.T) {
	cases := []struct {
		name        string
		description string
		want        string
	}{
		{"quoted", `Walk a folder (default "."). Params: path.`, `Walk a folder (default ".")`},
		{"leading", ". leading dot", ". leading dot"},
		{"bracketed", "Match [a.b] tokens. More.", "Match [a.b] tokens"},
		{"sentence", "Read a file. Params: path.", "Read a file"},
		{"nofinal", "Read a file", "Read a file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			index := tools.DeferredIndex([]tools.TierCandidate{{Name: "t", Description: tc.description}})
			if want := "- t: " + tc.want + "\n"; !strings.Contains(index, want) {
				t.Fatalf("index = %q, want it to contain %q", index, want)
			}
		})
	}
}

func TestSentenceEndSkipsAnAbbreviationPeriod(t *testing.T) {
	// A period with no following space is inside a token (a version, a file
	// name, an abbreviation), not the end of the sentence.
	index := tools.DeferredIndex([]tools.TierCandidate{
		{Name: "probe", Description: "Reads config.toml and reports. Then stops."},
	})
	if !strings.Contains(index, "- probe: Reads config.toml and reports\n") {
		t.Fatalf("index = %q, want the cut after the first real sentence", index)
	}
}
