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
// registry because the prompt index and the shortened advertised description
// (internal/cli's shortDescTool) are the two places a deferred tool's raw
// description gets machine-truncated to one line - a truncation bug here
// would surface in both. Cutting at the first period truncated list_dir
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
//
// The quote/bracket cases below are the ones that isolate the delimiter
// tracking. Every period they protect is followed by a space, so the
// followed-by-space rule alone would cut there: only quote toggling and
// bracket depth keep the one-liner whole.
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
		// A sentence-looking period inside a quoted phrase: the closing quote
		// must reopen cutting, and the opening one must suppress it.
		{"quoted sentence", `Say "one. two". Done.`, `Say "one. two"`},
		// The same inside brackets, with the period followed by a space.
		{"bracketed sentence", "Match [a. b] tokens. More.", "Match [a. b] tokens"},
		// An unmatched closing bracket must not drive the depth negative and
		// disarm a later real group - "a) ... b) ..." enumerations are ordinary
		// in tool descriptions.
		{"unmatched close", "Modes: a) fast, b) slow (see notes. here). Done.", "Modes: a) fast, b) slow (see notes. here)"},
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
