package config

import (
	"testing"
)

// TestResolveToolsConfigMaxFetchKBZeroBecomesDefault verifies that an unset
// or explicit 0 MaxFetchKB resolves to the built-in default (4096 KiB). The
// default lives in config, not in the tools registry: the registry's old
// hardcoded 1024 KiB coercion is gone.
func TestResolveToolsConfigMaxFetchKBZeroBecomesDefault(t *testing.T) {
	for _, tc0 := range []ToolsConfig{{}, {MaxFetchKB: 0}} {
		tc := resolveToolsConfig(tc0)
		if tc.MaxFetchKB != DefaultToolsConfig.MaxFetchKB {
			t.Fatalf("resolveToolsConfig(MaxFetchKB=%d) = %d, want the built-in default %d",
				tc0.MaxFetchKB, tc.MaxFetchKB, DefaultToolsConfig.MaxFetchKB)
		}
		if tc.MaxFetchKB != 4096 {
			t.Fatalf("built-in default is %d, want 4096", tc.MaxFetchKB)
		}
	}
}

// TestResolveToolsConfigMaxFetchKBPreservesPositive verifies that a positive
// operator value passes through resolveToolsConfig untouched.
func TestResolveToolsConfigMaxFetchKBPreservesPositive(t *testing.T) {
	for _, v := range []int{1, 1024, 8192, 1 << 20} {
		tc := resolveToolsConfig(ToolsConfig{MaxFetchKB: v})
		if tc.MaxFetchKB != v {
			t.Fatalf("resolveToolsConfig(MaxFetchKB=%d) = %d, want it unchanged", v, tc.MaxFetchKB)
		}
	}
}

// TestMaxFetchKBZeroInTOMLResolvesToDefault pins the load-path behavior:
// max_fetch_kb = 0 in a TOML file resolves to the built-in default (4096),
// exactly like the unset case - Go cannot tell an explicit 0 from an unset
// int, and both fall back to the default.
func TestMaxFetchKBZeroInTOMLResolvesToDefault(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeMinimalConfig(t, "\n[tools]\nmax_fetch_kb = 0\n")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tools.MaxFetchKB != DefaultToolsConfig.MaxFetchKB {
		t.Fatalf("max_fetch_kb = 0 resolved to %d, want the default %d",
			res.Tools.MaxFetchKB, DefaultToolsConfig.MaxFetchKB)
	}
	if res.Tools.MaxFetchKB != 4096 {
		t.Fatalf("resolved to %d, want 4096", res.Tools.MaxFetchKB)
	}
}

// TestMaxFetchKBPositiveInTOMLPreserved pins the load-path behavior for a
// positive operator value.
func TestMaxFetchKBPositiveInTOMLPreserved(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeMinimalConfig(t, "\n[tools]\nmax_fetch_kb = 8192\n")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tools.MaxFetchKB != 8192 {
		t.Fatalf("max_fetch_kb = 8192 resolved to %d, want 8192", res.Tools.MaxFetchKB)
	}
}
