package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGapLoadWrapsMemoryResolutionError pins the wrapped error Load returns
// when the [memory] section of a workspace config fails resolution. The wrap
// ("config <path>: %w") is the error surface operators see, so it needs a
// test of its own even though resolveMemoryConfig's individual bounds are
// covered directly.
func TestGapLoadWrapsMemoryResolutionError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := filepath.Join(t.TempDir(), "mivia.toml")
	body := "[memory]\nmax_entry_bytes = 100\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(LoadOptions{ConfigPath: cfg})
	if err == nil {
		t.Fatal("Load must fail when [memory] max_entry_bytes is out of range")
	}
	if !strings.Contains(err.Error(), "config "+cfg+":") {
		t.Errorf("error %q must wrap the selected config path %q", err, cfg)
	}
	if !strings.Contains(err.Error(), "max_entry_bytes") {
		t.Errorf("error %q must mention max_entry_bytes", err)
	}
}

// TestGapResolveMemoryConfigNoUserConfigPath pins the empty-UserConfigPath
// branch of resolveMemoryConfig's org_id switch: when no home directory is
// available (so UserConfigPath is ""), the org store is scoped away and the
// load still succeeds. This exercises the branch that readUserMemoryOrgID
// must never be called from.
func TestGapResolveMemoryConfigNoUserConfigPath(t *testing.T) {
	t.Setenv("HOME", "")
	if got := UserConfigPath(); got != "" {
		t.Fatalf("UserConfigPath = %q, want empty when HOME is unavailable", got)
	}
	mc, err := resolveMemoryConfig(File{Memory: MemoryConfig{OrgID: "github.com/evil"}}, "/ws/mivia.toml", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if mc.OrgID != "" {
		t.Errorf("org_id = %q, want empty when no user config path exists", mc.OrgID)
	}
}
