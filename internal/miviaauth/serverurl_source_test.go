package miviaauth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveServerURLNamesItsSource pins the provenance half of the
// resolver. A sync that uploads to the wrong backend is diagnosed by asking
// WHICH file set the URL, and the URL alone cannot answer that.
func TestResolveServerURLNamesItsSource(t *testing.T) {
	sandboxServerURLEnv(t)
	t.Setenv("MIVIA_API_BASE_URL", "")

	if url, src := ResolveServerURL(); url != DefaultServerURL || src != ServerURLSourceDefault {
		t.Fatalf("unset: ResolveServerURL() = %q, %q; want the default and %q", url, src, ServerURLSourceDefault)
	}

	miviaDir := filepath.Join(os.Getenv("HOME"), ".mivia")
	if err := os.MkdirAll(miviaDir, 0o700); err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(miviaDir, ".env")
	if err := os.WriteFile(envFile, []byte("MIVIA_API_BASE_URL=http://127.0.0.1:8090\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	url, src := ResolveServerURL()
	if url != "http://127.0.0.1:8090" {
		t.Fatalf("env file: url = %q", url)
	}
	if !strings.Contains(src, envFile) {
		t.Fatalf("env file: source = %q, want it to name %s so the user knows which file to fix", src, envFile)
	}

	t.Setenv("MIVIA_API_BASE_URL", "https://from-process-env.invalid")
	url, src = ResolveServerURL()
	if url != "https://from-process-env.invalid" || !strings.Contains(src, "process env") {
		t.Fatalf("process env: ResolveServerURL() = %q, %q; want the process value and a source naming the process env", url, src)
	}
}
