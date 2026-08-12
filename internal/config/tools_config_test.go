package config

import (
	"slices"
	"testing"
)

// [tools] ref_only_tools - opt-in list of tool names whose results are always
// spooled and replaced by a ref-only notice (plan tools/06). resolveToolsConfig
// normalizes it: trim whitespace, drop empty/whitespace-only entries, and
// dedupe exact strings preserving first-seen order.

func TestRefOnlyToolsNormalizedFromTOML(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeToolResultCapConfig(t,
		`ref_only_tools = [" read_file ", "", "grep", "read_file", " glob "]`)})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"read_file", "grep", "glob"}
	if !slices.Equal(res.Tools.RefOnlyTools, want) {
		t.Fatalf("ref_only_tools resolved to %q, want %q", res.Tools.RefOnlyTools, want)
	}
}

func TestRefOnlyToolsAbsentResolvesEmpty(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeToolResultCapConfig(t, "")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tools.RefOnlyTools != nil {
		t.Fatalf("absent ref_only_tools resolved to %q, want nil", res.Tools.RefOnlyTools)
	}
}

func TestRefOnlyToolsWhitespaceOnlyDropped(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeToolResultCapConfig(t,
		`ref_only_tools = [" ", " \t ", "\n"]`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tools.RefOnlyTools) != 0 {
		t.Fatalf("whitespace-only ref_only_tools resolved to %q, want empty", res.Tools.RefOnlyTools)
	}
}
