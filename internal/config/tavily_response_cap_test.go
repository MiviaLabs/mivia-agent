package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTavilyResponseCapConfig writes a minimal config with the given [tools]
// max_tavily_response_bytes line (empty string omits the key entirely).
func writeTavilyResponseCapConfig(t *testing.T, capLine string) string {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "mivia.toml")
	body := "[provider]\nname = \"deepseek\"\n\n[providers.deepseek]\nmodels = [{name=\"deepseek-v4-flash\", context_window_tokens=128000}]\n\n[chat]\nmax_tokens = 8192\n"
	if capLine != "" {
		body += "\n[tools]\n" + capLine + "\n"
	}
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// Unset must resolve to the built-in default, never to "unlimited": the
// dispatcher's output backstop is derived from finite tool-declared budgets,
// so an unlimited web response would leave the destruction defect unfixed.
func TestMaxTavilyResponseBytesDefaultsToBuiltIn(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeTavilyResponseCapConfig(t, "")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tools.MaxTavilyResponseBytes != DefaultToolsConfig.MaxTavilyResponseBytes {
		t.Fatalf("unset max_tavily_response_bytes resolved to %d, want the built-in default %d",
			res.Tools.MaxTavilyResponseBytes, DefaultToolsConfig.MaxTavilyResponseBytes)
	}
	if res.Tools.MaxTavilyResponseBytes != 4<<20 {
		t.Fatalf("built-in default is %d, want 4 MiB", res.Tools.MaxTavilyResponseBytes)
	}
}

func TestMaxTavilyResponseBytesFromTOML(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeTavilyResponseCapConfig(t, "max_tavily_response_bytes = 8388608")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tools.MaxTavilyResponseBytes != 8388608 {
		t.Fatalf("max_tavily_response_bytes = 8388608 resolved to %d", res.Tools.MaxTavilyResponseBytes)
	}
}

// Zero and negative mean "use the default", not "no bound".
func TestMaxTavilyResponseBytesNonPositiveResolvesToDefault(t *testing.T) {
	for _, line := range []string{"max_tavily_response_bytes = 0", "max_tavily_response_bytes = -1"} {
		res, err := Load(LoadOptions{ConfigPath: writeTavilyResponseCapConfig(t, line)})
		if err != nil {
			t.Fatalf("%s rejected: %v", line, err)
		}
		if res.Tools.MaxTavilyResponseBytes != DefaultToolsConfig.MaxTavilyResponseBytes {
			t.Fatalf("%s resolved to %d, want the default %d",
				line, res.Tools.MaxTavilyResponseBytes, DefaultToolsConfig.MaxTavilyResponseBytes)
		}
	}
}

// A value below the floor fails every legitimate response; reject at load
// rather than let every web call error at runtime.
func TestMaxTavilyResponseBytesFloorIsALoadError(t *testing.T) {
	_, err := Load(LoadOptions{ConfigPath: writeTavilyResponseCapConfig(t, "max_tavily_response_bytes = 500")})
	if err == nil {
		t.Fatal("max_tavily_response_bytes = 500 was accepted; want load error")
	}
	if !strings.Contains(err.Error(), "max_tavily_response_bytes") {
		t.Fatalf("error %q does not name max_tavily_response_bytes", err)
	}

	res, err := Load(LoadOptions{ConfigPath: writeTavilyResponseCapConfig(t, "max_tavily_response_bytes = 1024")})
	if err != nil {
		t.Fatalf("max_tavily_response_bytes = 1024 rejected: %v", err)
	}
	if res.Tools.MaxTavilyResponseBytes != 1024 {
		t.Fatalf("resolved to %d, want 1024", res.Tools.MaxTavilyResponseBytes)
	}
}

// An unbounded-in-practice value overflows the dispatcher's ceiling
// derivation (budget + input allowance + slack), which would silently drop
// the backstop back to its 256 KiB floor while the wire read stayed
// effectively infinite - the exact defect this knob exists to close. Cap it.
func TestMaxTavilyResponseBytesCeilingIsALoadError(t *testing.T) {
	_, err := Load(LoadOptions{ConfigPath: writeTavilyResponseCapConfig(t, "max_tavily_response_bytes = 9223372036854775807")})
	if err == nil {
		t.Fatal("max_tavily_response_bytes = MaxInt64 was accepted; want load error")
	}
	if !strings.Contains(err.Error(), "max_tavily_response_bytes") {
		t.Fatalf("error %q does not name max_tavily_response_bytes", err)
	}

	res, err := Load(LoadOptions{ConfigPath: writeTavilyResponseCapConfig(t, "max_tavily_response_bytes = 67108864")})
	if err != nil {
		t.Fatalf("max_tavily_response_bytes = 64 MiB rejected: %v", err)
	}
	if res.Tools.MaxTavilyResponseBytes != 67108864 {
		t.Fatalf("resolved to %d, want 67108864", res.Tools.MaxTavilyResponseBytes)
	}
}
