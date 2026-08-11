package config

import (
	"runtime"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestResolveToolsConfigWritePathBlocklistPassesThrough(t *testing.T) {
	// resolveToolsConfig normalizes but does not inject the built-in defaults;
	// the workflow registry composes defaults + additions at build time so a
	// directly-constructed Resolved still gets the defaults.
	tc := resolveToolsConfig(ToolsConfig{WritePathBlocklist: []string{"go.mod", ".mivia/workflows"}})
	if len(tc.WritePathBlocklist) != 2 {
		t.Fatalf("blocklist = %v, want the two project entries only", tc.WritePathBlocklist)
	}
	if tc.WritePathBlocklist[0] != "go.mod" || tc.WritePathBlocklist[1] != ".mivia/workflows" {
		t.Fatalf("blocklist = %v", tc.WritePathBlocklist)
	}
}

func TestResolveToolsConfigWritePathBlocklistNormalizes(t *testing.T) {
	tc := resolveToolsConfig(ToolsConfig{WritePathBlocklist: []string{" go.mod/ ", "a//b"}})
	got := map[string]bool{}
	for _, e := range tc.WritePathBlocklist {
		got[e] = true
	}
	if !got["go.mod"] {
		t.Fatalf("blocklist = %v, want trimmed trailing-slash go.mod", tc.WritePathBlocklist)
	}
	if !got["a/b"] {
		t.Fatalf("blocklist = %v, want normalized a/b", tc.WritePathBlocklist)
	}
}

func TestResolveToolsConfigWritePathBlocklistNilSafe(t *testing.T) {
	// An unset key stays empty after resolve; the registry applies the
	// built-in defaults at build time.
	if tc := resolveToolsConfig(ToolsConfig{}); len(tc.WritePathBlocklist) != 0 {
		t.Fatalf("blocklist = %v, want empty", tc.WritePathBlocklist)
	}
}

func TestValidateWritePathBlocklist(t *testing.T) {
	valid := ToolsConfig{WritePathBlocklist: []string{".git", ".mivia/workflows", "go.mod"}}
	if err := validateWritePathBlocklist(valid); err != nil {
		t.Fatalf("valid blocklist rejected: %v", err)
	}
	for _, bad := range []string{".", "", "   ", "go.mod/..", "/etc/passwd", "..", "a/../.."} {
		tc := ToolsConfig{WritePathBlocklist: []string{bad}}
		if err := validateWritePathBlocklist(tc); err == nil {
			t.Fatalf("blocklist entry %q accepted, want load error", bad)
		}
	}
	// A backslash separator is a load error on non-Windows hosts: it can
	// never match a workspace-relative path there.
	if runtime.GOOS != "windows" {
		tc := ToolsConfig{WritePathBlocklist: []string{"sub\\dir"}}
		if err := validateWritePathBlocklist(tc); err == nil {
			t.Fatal("backslash entry accepted on a non-Windows host, want load error")
		}
	}
}

func TestWritePathBlocklistTOMLKey(t *testing.T) {
	raw := []byte("[tools]\nwrite_path_blocklist = [\".mivia/workflows\", \"go.mod\"]\n")
	var file struct {
		Tools ToolsConfig `toml:"tools"`
	}
	if err := toml.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Tools.WritePathBlocklist) != 2 || file.Tools.WritePathBlocklist[0] != ".mivia/workflows" {
		t.Fatalf("WritePathBlocklist = %v", file.Tools.WritePathBlocklist)
	}
}

func TestValidateRejectsInvalidWritePathBlocklist(t *testing.T) {
	res := &Resolved{
		ProviderName: "deepseek", Model: "model", BaseURL: "https://example.test", APIKeyEnv: "KEY",
		// resolveToolsConfig defaults the other [tools] knobs so only the
		// blocklist validation is under test.
		Tools: resolveToolsConfig(ToolsConfig{WritePathBlocklist: []string{"."}}),
	}
	err := res.Validate()
	if err == nil || !strings.Contains(err.Error(), "write_path_blocklist") {
		t.Fatalf("Validate() error = %v, want write_path_blocklist error", err)
	}
}
