package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeToolResultCapConfig writes a minimal config with the given [tools]
// max_tool_result_bytes line (empty string omits the key entirely).
func writeToolResultCapConfig(t *testing.T, capLine string) string {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "mivia.toml")
	body := "[provider]\nname = \"deepseek\"\n"
	if capLine != "" {
		body += "\n[tools]\n" + capLine + "\n"
	}
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestMaxToolResultBytesDefaultsToUncapped(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeToolResultCapConfig(t, "")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tools.MaxToolResultBytes != 0 {
		t.Fatalf("unset max_tool_result_bytes resolved to %d, want 0 (uncapped)", res.Tools.MaxToolResultBytes)
	}
}

func TestMaxToolResultBytesFromTOML(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeToolResultCapConfig(t, "max_tool_result_bytes = 8192")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tools.MaxToolResultBytes != 8192 {
		t.Fatalf("max_tool_result_bytes = 8192 resolved to %d", res.Tools.MaxToolResultBytes)
	}
}

func TestMaxToolResultBytesFloorIsALoadError(t *testing.T) {
	// A positive value below 1024 starves every tool envelope; reject at load.
	_, err := Load(LoadOptions{ConfigPath: writeToolResultCapConfig(t, "max_tool_result_bytes = 500")})
	if err == nil {
		t.Fatal("max_tool_result_bytes = 500 was accepted; want load error")
	}
	if !strings.Contains(err.Error(), "max_tool_result_bytes") {
		t.Fatalf("error %q does not name max_tool_result_bytes", err)
	}

	// The floor itself is valid.
	res, err := Load(LoadOptions{ConfigPath: writeToolResultCapConfig(t, "max_tool_result_bytes = 1024")})
	if err != nil {
		t.Fatalf("max_tool_result_bytes = 1024 rejected: %v", err)
	}
	if res.Tools.MaxToolResultBytes != 1024 {
		t.Fatalf("resolved to %d, want 1024", res.Tools.MaxToolResultBytes)
	}

	// Negative normalizes to 0 (uncapped), not an error.
	res, err = Load(LoadOptions{ConfigPath: writeToolResultCapConfig(t, "max_tool_result_bytes = -100")})
	if err != nil {
		t.Fatalf("negative max_tool_result_bytes rejected: %v", err)
	}
	if res.Tools.MaxToolResultBytes != 0 {
		t.Fatalf("negative value resolved to %d, want 0", res.Tools.MaxToolResultBytes)
	}
}
