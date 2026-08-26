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

func TestMaxEditFileBytesMigratesFromMaxReadBytesWhenUnset(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeToolResultCapConfig(t,
		`max_read_bytes = 65536`)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tools.MaxEditFileBytes != 65536 {
		t.Fatalf("MaxEditFileBytes = %d, want 65536 migrated from max_read_bytes", res.Tools.MaxEditFileBytes)
	}

	// Explicit max_edit_file_bytes takes precedence over max_read_bytes
	res2, err := Load(LoadOptions{ConfigPath: writeToolResultCapConfig(t,
		"max_read_bytes = 65536\nmax_edit_file_bytes = 131072")})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Tools.MaxEditFileBytes != 131072 {
		t.Fatalf("MaxEditFileBytes = %d, want explicit 131072", res2.Tools.MaxEditFileBytes)
	}

	// Neither set falls back to memory backstop bytes
	res3, err := Load(LoadOptions{ConfigPath: writeToolResultCapConfig(t, "")})
	if err != nil {
		t.Fatal(err)
	}
	wantDefault := DefaultMemoryBackstopMB << 20
	if res3.Tools.MaxEditFileBytes != wantDefault {
		t.Fatalf("MaxEditFileBytes = %d, want backstop default %d", res3.Tools.MaxEditFileBytes, wantDefault)
	}
}
