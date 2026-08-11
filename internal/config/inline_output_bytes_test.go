package config

import (
	"testing"
)

// TestInlineOutputBytesExplicitZeroPreserved pins the config contract that an
// explicit [subagents] inline_output_bytes = 0 means "always use refs (never
// inline)" and must survive resolution. Before the fix, an explicit 0 was
// indistinguishable from an absent key, so resolveSubagentConfig overwrote it
// with the 4096 default and the documented "always use refs" mode was
// unreachable through config. Fails before the fix (resolves to 4096), passes
// after.
func TestInlineOutputBytesExplicitZeroPreserved(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeMinimalConfig(t, `
[subagents]
inline_output_bytes = 0
`)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Subagents.InlineOutputBytes != 0 {
		t.Fatalf("explicit inline_output_bytes = 0 resolved to %d, want 0 (always use refs)", res.Subagents.InlineOutputBytes)
	}
}

// TestInlineOutputBytesAbsentDefaultsTo4096 is the negative guard: an absent
// key keeps the historical 4096 default so existing configs load unchanged.
func TestInlineOutputBytesAbsentDefaultsTo4096(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeMinimalConfig(t, "")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Subagents.InlineOutputBytes != defaultInlineOutputBytes {
		t.Fatalf("absent inline_output_bytes resolved to %d, want default %d", res.Subagents.InlineOutputBytes, defaultInlineOutputBytes)
	}
}

// TestInlineOutputBytesExplicitPositivePreserved pins that an explicit
// positive value passes through resolution unchanged.
func TestInlineOutputBytesExplicitPositivePreserved(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeMinimalConfig(t, `
[subagents]
inline_output_bytes = 100
`)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Subagents.InlineOutputBytes != 100 {
		t.Fatalf("explicit inline_output_bytes = 100 resolved to %d, want 100", res.Subagents.InlineOutputBytes)
	}
}
