package cliorchestrate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/miviaauth"
)

func swapSyncProbe(t *testing.T, probe func(context.Context, string) (bool, string)) {
	t.Helper()
	prev := syncProbe
	syncProbe = probe
	t.Cleanup(func() { syncProbe = prev })
}

// TestDoctorReportsTheSyncEndpointAndItsSource is the S3.5 discriminator.
// With nothing set, doctor names the production default and says so; with
// MIVIA_API_BASE_URL in an env file it names that file. A reporter that
// hard-coded the default instead of calling the resolver passes the first
// half and fails the second.
func TestDoctorReportsTheSyncEndpointAndItsSource(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	t.Setenv("MIVIA_API_BASE_URL", "")
	t.Setenv("DEEPSEEK_API_KEY", "example-token")
	configPath := writeDoctorConfig(t, t.TempDir())
	workspace := t.TempDir()
	var probed []string
	swapSyncProbe(t, func(_ context.Context, url string) (bool, string) {
		probed = append(probed, url)
		return true, "reachable (HTTP 200)"
	})

	var out, errOut strings.Builder
	if err := RunDoctorWithIO([]string{"--config", configPath, "--workspace", workspace}, &out, &errOut); err != nil {
		t.Fatalf("doctor: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "  sync_api:   https://api.mivia.app (default)\n") {
		t.Errorf("default case: output does not name the default endpoint and its source:\n%s", text)
	}
	if !strings.Contains(text, "sync_probe: skipped (not logged in)") || len(probed) != 0 {
		t.Errorf("not logged in: probe ran %d times, want skipped; output:\n%s", len(probed), text)
	}

	// Now an env file supplies the URL and a login exists: both lines change
	// and the probe runs against the resolved URL, not the default.
	miviaDir := filepath.Join(os.Getenv("HOME"), ".mivia")
	if err := os.MkdirAll(miviaDir, 0o700); err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(miviaDir, ".env")
	if err := os.WriteFile(envFile, []byte("MIVIA_API_BASE_URL=http://127.0.0.1:3001\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := miviaauth.Save(config.UserAuthPath(), miviaauth.Token{
		Bearer: "test-bearer", RefreshToken: "test-refresh", ExpiresAt: time.Now().Add(2 * time.Hour),
	}); err != nil {
		t.Fatalf("save token: %v", err)
	}

	out.Reset()
	if err := RunDoctorWithIO([]string{"--config", configPath, "--workspace", workspace}, &out, &errOut); err != nil {
		t.Fatalf("doctor: %v", err)
	}
	text = out.String()
	if !strings.Contains(text, "sync_api:   http://127.0.0.1:3001 (MIVIA_API_BASE_URL ("+envFile+"))") {
		t.Errorf("env-file case: output does not name the override and the file that set it:\n%s", text)
	}
	if !strings.Contains(text, "sync_login: present") {
		t.Errorf("login present: output does not say so:\n%s", text)
	}
	if !strings.Contains(text, "sync_probe: reachable (HTTP 200)") {
		t.Errorf("probe result missing:\n%s", text)
	}
	if len(probed) != 1 || probed[0] != "http://127.0.0.1:3001" {
		t.Errorf("probed %v, want exactly one probe of the resolved URL", probed)
	}
}

// TestDoctorJSONCarriesTheSyncFields holds the machine-readable copy to the
// human one.
func TestDoctorJSONCarriesTheSyncFields(t *testing.T) {
	configPath, workspace, _ := setupDoctorJSONTest(t)
	t.Chdir(t.TempDir())
	var out, errOut strings.Builder
	if err := RunDoctorWithIO([]string{"--config", configPath, "--json", "--workspace", workspace}, &out, &errOut); err != nil {
		t.Fatalf("doctor --json: %v", err)
	}
	var dj doctorJSON
	if err := json.Unmarshal([]byte(out.String()), &dj); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if dj.SyncAPIURL != "https://api.mivia.app" || dj.SyncAPISource != "default" {
		t.Errorf("sync_api_url=%q sync_api_source=%q", dj.SyncAPIURL, dj.SyncAPISource)
	}
	if !strings.HasPrefix(dj.SyncLogin, "absent") || !strings.HasPrefix(dj.SyncProbe, "skipped") {
		t.Errorf("sync_login=%q sync_probe=%q", dj.SyncLogin, dj.SyncProbe)
	}
}

// TestDoctorSaysWhenSyncIsDisabled: an opted-out sync has no endpoint worth
// probing, and reporting one would invite a user to fix a URL nothing uses.
func TestDoctorSaysWhenSyncIsDisabled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY", "example-token")
	dir := t.TempDir()
	path := filepath.Join(dir, "mivia.toml")
	body := "[provider]\nname = \"deepseek\"\n\n[providers.deepseek]\n" +
		"models = [{ name = \"deepseek-v4-pro\", context_window_tokens = 128000 }]\n" +
		"default_model = \"deepseek-v4-pro\"\n\n[sync]\nenabled = false\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	swapSyncProbe(t, func(context.Context, string) (bool, string) {
		t.Fatal("probe ran for a disabled sync")
		return false, ""
	})
	var out, errOut strings.Builder
	if err := RunDoctorWithIO([]string{"--config", path, "--workspace", t.TempDir()}, &out, &errOut); err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if !strings.Contains(out.String(), "sync_api:   disabled") {
		t.Errorf("output:\n%s", out.String())
	}
}
