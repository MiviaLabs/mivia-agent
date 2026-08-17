package verifier

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// repoRootForTest resolves the repository root from this test file's own
// location, independent of the `go test` working directory.
func repoRootForTest(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// internal/workflows/verifier/<this file> -> repo root is three levels up.
	root, err := filepath.Abs(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "scripts", "check_go_structure.py")); err != nil {
		t.Fatalf("resolved repo root %q does not contain scripts/check_go_structure.py: %v", root, err)
	}
	return root
}

// writeHardCommentBlockFixture writes a Go file whose leading comment block
// runs 35 lines: over the hard cap (30), well over the soft cap (25) that
// would only WARN.
func writeHardCommentBlockFixture(t *testing.T, dir string) string {
	t.Helper()
	var sb strings.Builder
	sb.WriteString("package fixture\n\n")
	for i := 0; i < 35; i++ {
		sb.WriteString("// rationale line filling the block past the hard cap\n")
	}
	sb.WriteString("var _ = 0\n")
	p := filepath.Join(dir, "hard_comment_block.go")
	if err := os.WriteFile(p, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// writeWarnNoiseFixtures writes n distinct Go files, each over the file-LOC
// soft cap (500) but under the hard cap (800): each produces its own unique
// WARN line, so dedup can't collapse them into the HARD line's tail window.
// This is the exact mechanism that pushed the HARD line out of the fallback
// tail before the ^HARD marker was added.
func writeWarnNoiseFixtures(t *testing.T, dir string, n int) []string {
	t.Helper()
	paths := make([]string, 0, n)
	for i := 0; i < n; i++ {
		var sb strings.Builder
		sb.WriteString("package fixture\n\n")
		for j := 0; j < 550; j++ {
			sb.WriteString("var _ = " + strconv.Itoa(j) + "\n")
		}
		p := filepath.Join(dir, fmt.Sprintf("warn_noise_%02d.go", i))
		if err := os.WriteFile(p, []byte(sb.String()), 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	return paths
}

// TestCheckGoStructureHardCommentHintReachesRepairEvidence is a true
// end-to-end integration test: it runs the ACTUAL scripts/check_go_structure.py
// against real fixture files (one HARD comment-block violation plus 25
// distinct file-LOC WARN violations, replicating the exact failure shape
// that buried the hint in wfr-inv-0db263bf6f08df3f30862788d9c67d28), through
// the real CommandProfile -> failureEvidence -> extractFailures pipeline
// (no mocked Result), and asserts the HARD line - the repair agent's only
// actionable "which file, which lines" hint - survives into Check.Failures,
// which is exactly what the workflow's context binding
// ({from = "steps.preflight_structure.output", as = "failed_evidence"})
// hands the repair step.
//
// Regression: before the ^HARD marker (failures.go), this exact fixture
// shape reproduced the drop: the HARD line sat above the 20-line tail
// window once >=20 distinct WARN lines followed it.
func TestCheckGoStructureHardCommentHintReachesRepairEvidence(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	root := repoRootForTest(t)
	scriptPath := filepath.Join(root, "scripts", "check_go_structure.py")
	dir := t.TempDir()
	hardFile := writeHardCommentBlockFixture(t, dir)
	paths := append([]string{hardFile}, writeWarnNoiseFixtures(t, dir, 25)...)

	declaredArgs := append([]string{scriptPath}, paths...)
	profile, err := NewCommandProfile("go-structure", "python3", declaredArgs)
	if err != nil {
		t.Fatal(err)
	}
	cp := profile.(*CommandProfile)
	// Run the real script as a direct subprocess (bypassing the sandbox,
	// which needs bubblewrap and is exercised separately in sandbox_test.go)
	// so this test is fast and deterministic while still exercising the
	// REAL check_go_structure.py output through the REAL extraction path.
	cp.run = func(ctx context.Context, workDir, program string, cmdArgs ...string) error {
		out, runErr := exec.CommandContext(ctx, program, cmdArgs...).CombinedOutput()
		if runErr == nil {
			return nil
		}
		return sourceCommandFailure(string(out), runErr)
	}

	result, err := cp.Verify(context.Background(), Request{WorkDir: dir})
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if result.Status != "failed" || len(result.Checks) != 1 {
		t.Fatalf("result = %#v, want one failed check", result)
	}
	check := result.Checks[0]
	if check.Class != "source" || !result.Repairable() {
		t.Fatalf("check = %#v, want class=source and repairable (a repair step must be reachable)", check)
	}
	joined := strings.Join(check.Failures, "\n")
	if !strings.Contains(joined, "HARD comment block:") || !strings.Contains(joined, "hard_comment_block.go") {
		t.Fatalf("Check.Failures dropped the HARD comment-block hint; repair step would get no actionable file/line info. Failures = %q\nDetail = %q", check.Failures, check.Detail)
	}
	// Sanity: prove the burial scenario was real. The ^HARD marker makes
	// extractFailures' forward pass match ONLY the HARD line (WARN lines
	// match no marker), which is itself the point: without the marker,
	// zero lines would match, extractFailures would fall back to the raw
	// output's last 20 lines, and the HARD line - emitted BEFORE all 25
	// WARN lines in check_go_structure.py's file-processing order - would
	// have been silently dropped. Confirm the raw detail actually contains
	// that many WARN lines, so this fixture genuinely reproduces the burial
	// shape rather than trivially passing on a short output.
	warnCount := strings.Count(check.Detail, "WARN file LOC:")
	if warnCount < 20 {
		t.Fatalf("fixture only produced %d WARN lines in the raw output, want >= 20 to reproduce the burial scenario (a tail-only fallback keeps just the last 20 lines): %q", warnCount, check.Detail)
	}
}
