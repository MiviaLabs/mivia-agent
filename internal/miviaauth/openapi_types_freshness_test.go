package miviaauth

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestOpenAPITypesFreshness is the in-repo half of the drift gate for
// openapi_types.gen.go: it re-runs the exact command generate.go's
// go:generate directive declares and diffs the output against the
// checked-in file. It proves openapi_types.gen.go matches
// api/openapi/auth.v2.yaml -- it does NOT and cannot prove that vendored
// spec still matches go-mivia's current one, since go-mivia is a separate
// repo with its own release cadence (see generate.go for the manual resync
// command).
//
// Skips (does not fail) when `go tool oapi-codegen` cannot run at all --
// e.g. the module cache lacks the tool and the environment is offline --
// so an unrelated CI run without network access does not fail on this
// check. A genuine mismatch between the tool's output and the checked-in
// file always fails, never skips.
func TestOpenAPITypesFreshness(t *testing.T) {
	root := openAPIFreshnessRepoRoot(t)
	wantPath := filepath.Join(root, "internal", "miviaauth", "openapi_types.gen.go")
	want, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read %s: %v", wantPath, err)
	}

	outPath := filepath.Join(t.TempDir(), "openapi_types.gen.go")
	cmd := exec.Command("go", "tool", "oapi-codegen",
		"-generate", "types",
		"-package", "miviaauth",
		"-o", outPath,
		filepath.Join(root, "api", "openapi", "auth.v2.yaml"),
	)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("go tool oapi-codegen unavailable, skipping freshness check: %v\n%s", err, out)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read generated output %s: %v", outPath, err)
	}

	// Compare EOL-normalized content: the generator always writes LF, while
	// a windows-latest checkout may materialize the checked-in file as CRLF
	// (core.autocrlf=true). Real drift still fails; checkout-mangled line
	// endings must not.
	if !bytes.Equal(bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n")),
		bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))) {
		t.Fatalf(
			"internal/miviaauth/openapi_types.gen.go is stale relative to api/openapi/auth.v2.yaml.\n\n"+
				"Regenerate and commit with:\n  go generate ./internal/miviaauth/...\n\n"+
				"live output length=%d bytes; checked-in file length=%d bytes",
			len(got), len(want),
		)
	}
}

// openAPIFreshnessRepoRoot resolves the module root from this test file's
// known location (internal/miviaauth is 2 levels under the repo root).
func openAPIFreshnessRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("resolved repo root %q has no go.mod: %v", root, err)
	}
	return root
}
