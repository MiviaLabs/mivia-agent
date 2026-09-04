package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func TestResolveMemoryConfigDefaults(t *testing.T) {
	mc, err := resolveMemoryConfig(File{}, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !mc.IsEnabled() {
		t.Error("memory must default to enabled")
	}
	if mc.StoreBackend != "markdown" {
		t.Errorf("store_backend = %q, want markdown", mc.StoreBackend)
	}
	if mc.MaxEntryBytes != 8192 || mc.MaxEntries != 500 || mc.MaxSearchResults != 8 {
		t.Errorf("defaults wrong: %+v", mc)
	}
	if mc.OrgID != "" {
		t.Errorf("org_id must default empty, got %q", mc.OrgID)
	}
}

func TestResolveMemoryConfigExplicit(t *testing.T) {
	enabled := true
	mc, err := resolveMemoryConfig(File{Memory: MemoryConfig{
		Enabled:          &enabled,
		StoreBackend:     "memory",
		StorePath:        ".mivia/memory.db",
		MaxEntryBytes:    4096,
		MaxEntries:       10,
		MaxSearchResults: 5,
	}}, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if mc.StoreBackend != "memory" || mc.StorePath != ".mivia/memory.db" {
		t.Errorf("explicit values lost: %+v", mc)
	}
	if mc.MaxEntryBytes != 4096 || mc.MaxEntries != 10 || mc.MaxSearchResults != 5 {
		t.Errorf("explicit caps lost: %+v", mc)
	}
}

// TestResolveMemoryConfigExplicitBeatsProjectTier pins the precedence at the
// tier boundary: with a real project config present (projectFound=true) an
// explicit store_path still wins over the workspace default. The explicit
// test above runs with projectFound=false, so a precedence flip toward the
// project tier would have passed every existing test. Review finding 4c.
func TestResolveMemoryConfigExplicitBeatsProjectTier(t *testing.T) {
	root := t.TempDir()
	mc, err := resolveMemoryConfig(File{Memory: MemoryConfig{StorePath: ".mivia/memory.db"}}, "", root, true)
	if err != nil {
		t.Fatal(err)
	}
	if mc.StorePath != ".mivia/memory.db" {
		t.Errorf("StorePath = %q, want the explicit value to win over the project tier", mc.StorePath)
	}
	if mc.StorePath == workspace.MemoryDBPath(root) {
		t.Errorf("StorePath = %q; the project-tier default must not overwrite an explicit store_path", mc.StorePath)
	}
}

func TestResolveMemoryConfigDisabledSkipsValidation(t *testing.T) {
	disabled := false
	mc, err := resolveMemoryConfig(File{Memory: MemoryConfig{Enabled: &disabled, StoreBackend: "bogus"}}, "", "", false)
	if err != nil {
		t.Fatalf("disabled memory must not fail on a bogus backend: %v", err)
	}
	if mc.IsEnabled() {
		t.Error("memory must stay disabled")
	}
}

func TestResolveMemoryConfigRejectsInvalidValues(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*MemoryConfig)
		want string
	}{
		{"bad backend", func(m *MemoryConfig) { m.StoreBackend = "nope" }, "store_backend"},
		{"entry floor", func(m *MemoryConfig) { m.MaxEntryBytes = 100 }, "max_entry_bytes"},
		{"entry ceiling", func(m *MemoryConfig) { m.MaxEntryBytes = 100000 }, "max_entry_bytes"},
		{"search ceiling", func(m *MemoryConfig) { m.MaxSearchResults = 51 }, "max_search_results"},
		{"bad block pattern", func(m *MemoryConfig) { m.BlockPatterns = []string{"["} }, "block_patterns"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mc := DefaultMemoryConfig
			tc.mut(&mc)
			_, err := resolveMemoryConfig(File{Memory: mc}, "", "", false)
			if err == nil {
				t.Fatal("expected load error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// writeUserMemoryConfig writes a user-level config under $HOME and returns
// the path. Named distinctly from agents_test.go's writeUserConfig.
func writeUserMemoryConfig(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".mivia")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "mivia.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveMemoryConfigOrgIDFromUserFileWhenWorkspaceSelected(t *testing.T) {
	userPath := writeUserMemoryConfig(t, "[memory]\norg_id = \"github.com/acme\"\n")
	if got := UserConfigPath(); got != userPath {
		t.Fatalf("UserConfigPath = %q, want %q", got, userPath)
	}
	// Workspace config sets org_id; the user file must win.
	mc, err := resolveMemoryConfig(File{Memory: MemoryConfig{OrgID: "github.com/evil"}}, "/ws/mivia.toml", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if mc.OrgID != "github.com/acme" {
		t.Errorf("org_id = %q, want the user-file value", mc.OrgID)
	}
}

func TestResolveMemoryConfigOrgIDWhenUserFileIsSelected(t *testing.T) {
	userPath := writeUserMemoryConfig(t, "[memory]\norg_id = \"github.com/acme\"\n")
	mc, err := resolveMemoryConfig(File{Memory: MemoryConfig{OrgID: "github.com/acme"}}, userPath, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if mc.OrgID != "github.com/acme" {
		t.Errorf("org_id = %q, want the selected user-file value", mc.OrgID)
	}
}

func TestResolveMemoryConfigIgnoresWorkspaceOrgIDWithoutUserFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	mc, err := resolveMemoryConfig(File{Memory: MemoryConfig{OrgID: "github.com/evil"}}, "/ws/mivia.toml", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if mc.OrgID != "" {
		t.Errorf("workspace org_id must be ignored without a user file, got %q", mc.OrgID)
	}
}

func TestResolveMemoryConfigNormalizesOrgID(t *testing.T) {
	writeUserMemoryConfig(t, "[memory]\norg_id = \"  GitHub.com/MiviaLabs  \"\n")
	mc, err := resolveMemoryConfig(File{}, "/ws/mivia.toml", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if mc.OrgID != "github.com/mivialabs" {
		t.Errorf("org_id = %q, want normalized lowercase", mc.OrgID)
	}
}

func TestResolveMemoryConfigRejectsInvalidUserOrgID(t *testing.T) {
	writeUserMemoryConfig(t, "[memory]\norg_id = \"has space\"\n")
	if _, err := resolveMemoryConfig(File{}, "/ws/mivia.toml", "", false); err == nil {
		t.Fatal("invalid user org_id must fail the load")
	}
}

func TestResolveMemoryConfigMalformedUserFileDegrades(t *testing.T) {
	writeUserMemoryConfig(t, "not [ valid toml")
	mc, err := resolveMemoryConfig(File{}, "/ws/mivia.toml", "", false)
	if err != nil {
		t.Fatalf("a malformed user file must not break the workspace config: %v", err)
	}
	if mc.OrgID != "" {
		t.Errorf("org_id = %q, want empty", mc.OrgID)
	}
}

func TestResolveMemoryConfigInjectCoreDefaultsFalse(t *testing.T) {
	mc, err := resolveMemoryConfig(File{}, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if mc.InjectCore {
		t.Error("inject_core must default to false")
	}
}

func TestResolveMemoryConfigInjectCoreExplicitTrue(t *testing.T) {
	mc, err := resolveMemoryConfig(File{Memory: MemoryConfig{InjectCore: true}}, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !mc.InjectCore {
		t.Error("explicit inject_core = true must be preserved")
	}
}

func TestResolveMemoryConfigMarkdownHasNoProjectStorePath(t *testing.T) {
	root := t.TempDir()
	mc, err := resolveMemoryConfig(File{}, "", root, true)
	if err != nil {
		t.Fatal(err)
	}
	if mc.StorePath != "" {
		t.Errorf("StorePath = %q, want empty for Markdown backend", mc.StorePath)
	}
}

func TestResolveMemoryConfigMarkdownHasNoTempStorePath(t *testing.T) {
	root := t.TempDir()
	mc, err := resolveMemoryConfig(File{}, "", root, false)
	if err != nil {
		t.Fatal(err)
	}
	if mc.StorePath != "" {
		t.Errorf("StorePath = %q, want empty for Markdown backend", mc.StorePath)
	}
}

func TestResolveMemoryConfigLeavesStorePathEmptyWhenMarkdownIsDisabled(t *testing.T) {
	disabled := false

	t.Run("project tier", func(t *testing.T) {
		root := t.TempDir()
		mc, err := resolveMemoryConfig(File{Memory: MemoryConfig{Enabled: &disabled}}, "", root, true)
		if err != nil {
			t.Fatal(err)
		}
		if mc.IsEnabled() {
			t.Fatal("memory must stay disabled")
		}
		if mc.StorePath != "" {
			t.Errorf("StorePath = %q, want empty for disabled Markdown memory", mc.StorePath)
		}
	})

	t.Run("temp tier", func(t *testing.T) {
		root := t.TempDir()
		mc, err := resolveMemoryConfig(File{Memory: MemoryConfig{Enabled: &disabled}}, "", root, false)
		if err != nil {
			t.Fatal(err)
		}
		if mc.IsEnabled() {
			t.Fatal("memory must stay disabled")
		}
		if mc.StorePath != "" {
			t.Errorf("StorePath = %q, want empty for disabled Markdown memory", mc.StorePath)
		}
	})
}

func TestMemoryConfigIsEnabledNilMeansTrue(t *testing.T) {
	var mc MemoryConfig
	if !mc.IsEnabled() {
		t.Fatal("nil enabled must mean enabled")
	}
	disabled := false
	mc.Enabled = &disabled
	if mc.IsEnabled() {
		t.Fatal("explicit false must disable")
	}
}
