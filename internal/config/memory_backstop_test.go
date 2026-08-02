package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeMemoryBackstopConfig(t *testing.T, toolsBody string) string {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "mivia.toml")
	body := "[provider]\nname = \"deepseek\"\n\n[providers.deepseek]\nmodels = [{name=\"deepseek-v4-flash\", context_window_tokens=128000}]\n\n[chat]\nmax_tokens = 8192\n"
	if toolsBody != "" {
		body += "\n[tools]\n" + toolsBody + "\n"
	}
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestMemoryBackstopMBDefault256(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeMemoryBackstopConfig(t, "")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tools.MemoryBackstopMB != DefaultMemoryBackstopMB {
		t.Fatalf("MemoryBackstopMB = %d, want default %d", res.Tools.MemoryBackstopMB, DefaultMemoryBackstopMB)
	}
}

func TestMemoryBackstopMBOverride(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeMemoryBackstopConfig(t, "memory_backstop_mb = 128")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tools.MemoryBackstopMB != 128 {
		t.Fatalf("MemoryBackstopMB = %d, want 128", res.Tools.MemoryBackstopMB)
	}
}

func TestMemoryBackstopMBZeroBecomesDefault(t *testing.T) {
	tc := resolveToolsConfig(ToolsConfig{MemoryBackstopMB: 0})
	if tc.MemoryBackstopMB != DefaultMemoryBackstopMB {
		t.Fatalf("0 resolved to %d, want default %d", tc.MemoryBackstopMB, DefaultMemoryBackstopMB)
	}
	tc = resolveToolsConfig(ToolsConfig{MemoryBackstopMB: -1})
	if tc.MemoryBackstopMB != DefaultMemoryBackstopMB {
		t.Fatalf("negative resolved to %d, want default %d", tc.MemoryBackstopMB, DefaultMemoryBackstopMB)
	}
}
