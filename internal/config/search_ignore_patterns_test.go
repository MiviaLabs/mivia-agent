package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeSearchIgnoreConfig writes a minimal config with the given [tools]
// search_ignore_patterns line (empty string omits the key entirely).
func writeSearchIgnoreConfig(t *testing.T, ignoreLine string) string {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "mivia.toml")
	body := "[provider]\nname = \"deepseek\"\n\n[providers.deepseek]\nmodels = [{name=\"deepseek-v4-flash\", context_window_tokens=128000}]\n\n[chat]\nmax_tokens = 8192\n"
	if ignoreLine != "" {
		body += "\n[tools]\n" + ignoreLine + "\n"
	}
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// Unset search_ignore_patterns carries no config-level entries. The built-in
// defaults (.git, node_modules, vendor) are owned by the tools registry
// (internal/tools/default_registry.go) and are merged there, so the resolved
// config itself stays empty.
func TestSearchIgnorePatternsUnsetIsEmpty(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeSearchIgnoreConfig(t, "")})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tools.SearchIgnorePatterns) != 0 {
		t.Fatalf("unset search_ignore_patterns resolved to %v, want none at config level", res.Tools.SearchIgnorePatterns)
	}
}

// A configured search_ignore_patterns is preserved verbatim. It EXTENDS the
// built-in defaults rather than replacing them; the merge happens in the tools
// registry (internal/tools/default_registry.go).
func TestSearchIgnorePatternsFromTOML(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeSearchIgnoreConfig(t, `search_ignore_patterns = ["dist", ".cache"]`)})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"dist", ".cache"}
	if len(res.Tools.SearchIgnorePatterns) != len(want) {
		t.Fatalf("search_ignore_patterns resolved to %v, want %v", res.Tools.SearchIgnorePatterns, want)
	}
	for i := range want {
		if res.Tools.SearchIgnorePatterns[i] != want[i] {
			t.Fatalf("search_ignore_patterns resolved to %v, want %v", res.Tools.SearchIgnorePatterns, want)
		}
	}
}

// resolveToolsConfig must neither drop nor mutate the field.
func TestResolveToolsConfigPreservesSearchIgnorePatterns(t *testing.T) {
	tc := resolveToolsConfig(ToolsConfig{SearchIgnorePatterns: []string{"dist"}})
	if len(tc.SearchIgnorePatterns) != 1 || tc.SearchIgnorePatterns[0] != "dist" {
		t.Fatalf("resolveToolsConfig changed SearchIgnorePatterns: %v", tc.SearchIgnorePatterns)
	}
}
