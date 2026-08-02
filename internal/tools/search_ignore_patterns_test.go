package tools

import (
	"reflect"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// searchToolIgnorePatterns returns the ignore-pattern list a registry wired
// into its grep/glob tools, so tests can assert what the registry actually
// passed through.
func searchToolIgnorePatterns(t *testing.T, name string, reg *Registry) []string {
	t.Helper()
	tool, ok := reg.Get(name)
	if !ok {
		t.Fatalf("%s not registered", name)
	}
	switch tt := tool.(type) {
	case *grepTool:
		if tt.ignore == nil {
			return nil
		}
		return tt.ignore.Patterns()
	case *globTool:
		if tt.ignore == nil {
			return nil
		}
		return tt.ignore.Patterns()
	}
	t.Fatalf("%s has unexpected type %T", name, tool)
	return nil
}

// With no operator configuration, grep/glob skip exactly the built-in
// defaults - the new key must not change the out-of-the-box behavior.
func TestSearchIgnorePatternsDefaultsAreBuiltIn(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws})
	for _, name := range []string{"grep", "glob"} {
		if got := searchToolIgnorePatterns(t, name, reg); !reflect.DeepEqual(got, defaultIgnorePatterns) {
			t.Errorf("%s ignore patterns = %v, want built-in defaults %v", name, got, defaultIgnorePatterns)
		}
	}
}

// A configured search_ignore_patterns EXTENDS the built-in defaults instead of
// replacing them: grep/glob must skip .git/node_modules/vendor AND the
// operator's entries.
func TestSearchIgnorePatternsExtendDefaults(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{
		Workspace:            ws,
		SearchIgnorePatterns: []string{"dist", ".cache"},
	})
	want := append([]string(nil), defaultIgnorePatterns...)
	want = append(want, "dist", ".cache")
	for _, name := range []string{"grep", "glob"} {
		if got := searchToolIgnorePatterns(t, name, reg); !reflect.DeepEqual(got, want) {
			t.Errorf("%s ignore patterns = %v, want %v (defaults extended)", name, got, want)
		}
	}
}
