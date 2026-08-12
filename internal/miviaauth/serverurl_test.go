package miviaauth

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// sandboxServerURLEnv isolates ServerURLFromEnv's two file-lookup
// candidates (cwd ./.env and ~/.mivia/.env) to empty temp directories, so
// these tests never read the developer's real ~/.mivia/.env or a repo-root
// .env file left over from other tooling.
func sandboxServerURLEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
}

func TestServerURLFromEnvUnsetReturnsDefault(t *testing.T) {
	sandboxServerURLEnv(t)
	t.Setenv("MIVIA_API_BASE_URL", "")

	if got := ServerURLFromEnv(); got != DefaultServerURL {
		t.Errorf("ServerURLFromEnv() = %q, want %q", got, DefaultServerURL)
	}
}

func TestServerURLFromEnvSetReturnsOverride(t *testing.T) {
	sandboxServerURLEnv(t)
	t.Setenv("MIVIA_API_BASE_URL", "https://api.staging.mivia.app")

	want := "https://api.staging.mivia.app"
	if got := ServerURLFromEnv(); got != want {
		t.Errorf("ServerURLFromEnv() = %q, want %q", got, want)
	}
}

func TestServerURLFromEnvWhitespaceOnlyFallsBackToDefault(t *testing.T) {
	sandboxServerURLEnv(t)
	t.Setenv("MIVIA_API_BASE_URL", "   ")

	if got := ServerURLFromEnv(); got != DefaultServerURL {
		t.Errorf("ServerURLFromEnv() = %q, want %q", got, DefaultServerURL)
	}
}

func TestServerURLFromEnvReadsUserEnvFileWhenProcessEnvUnset(t *testing.T) {
	sandboxServerURLEnv(t)
	t.Setenv("MIVIA_API_BASE_URL", "") // unset in process env, forcing the file fallback

	miviaDir := filepath.Join(os.Getenv("HOME"), ".mivia")
	if err := os.MkdirAll(miviaDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", miviaDir, err)
	}
	envFile := filepath.Join(miviaDir, ".env")
	if err := os.WriteFile(envFile, []byte("MIVIA_API_BASE_URL=http://127.0.0.1:8090\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", envFile, err)
	}

	want := "http://127.0.0.1:8090"
	if got := ServerURLFromEnv(); got != want {
		t.Errorf("ServerURLFromEnv() = %q, want %q (value from ~/.mivia/.env)", got, want)
	}
}

func TestServerURLFromEnvProcessEnvWinsOverUserEnvFile(t *testing.T) {
	sandboxServerURLEnv(t)

	miviaDir := filepath.Join(os.Getenv("HOME"), ".mivia")
	if err := os.MkdirAll(miviaDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", miviaDir, err)
	}
	envFile := filepath.Join(miviaDir, ".env")
	if err := os.WriteFile(envFile, []byte("MIVIA_API_BASE_URL=http://from-file.invalid\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", envFile, err)
	}
	t.Setenv("MIVIA_API_BASE_URL", "https://from-process-env.invalid")

	want := "https://from-process-env.invalid"
	if got := ServerURLFromEnv(); got != want {
		t.Errorf("ServerURLFromEnv() = %q, want %q (process env must win over the file)", got, want)
	}
}

func TestDefaultServerURLIsValidHTTPSURL(t *testing.T) {
	if _, err := config.ValidateHTTPSURL(DefaultServerURL); err != nil {
		t.Errorf("ValidateHTTPSURL(DefaultServerURL) error = %v, want nil", err)
	}
}
