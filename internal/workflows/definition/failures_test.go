package definition

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestExtractFailuresGoTest(t *testing.T) {
	out := "ok  github.com/x/pkg/a\t0.1s\n" +
		"--- FAIL: TestAlpha (0.00s)\n" +
		"    a_test.go:12: got 1, want 2\n" +
		"--- FAIL: TestBeta (0.00s)\n" +
		"    b_test.go:7: line 0 has width 121, want 120\n" +
		"FAIL\tgithub.com/x/pkg\t1.2s\n" +
		"FAIL\n"
	f := extractFailures([]byte(out))
	joined := strings.Join(f, "\n")
	for _, want := range []string{
		"--- FAIL: TestAlpha",
		"--- FAIL: TestBeta",
		"a_test.go:12: got 1, want 2",
		"b_test.go:7: line 0 has width 121, want 120",
		"FAIL\tgithub.com/x/pkg",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("extractFailures missing %q; got %q", want, f)
		}
	}
}

func TestExtractFailuresCompileError(t *testing.T) {
	out := "# github.com/x/pkg [github.com/x/pkg.test]\n" +
		"internal/cli/x_test.go:117:22: cannot range over cmds (variable of func type)\n" +
		"FAIL\n"
	f := extractFailures([]byte(out))
	joined := strings.Join(f, "\n")
	for _, want := range []string{
		"# github.com/x/pkg",
		"internal/cli/x_test.go:117:22: cannot range over cmds",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("extractFailures missing %q; got %q", want, f)
		}
	}
}

func TestExtractFailuresPytest(t *testing.T) {
	out := "tests/test_a.py::test_x FAILED\n" +
		"tests/test_a.py::test_y ERROR\n" +
		"===== 1 failed, 1 error in 0.5s =====\n"
	f := extractFailures([]byte(out))
	joined := strings.Join(f, "\n")
	if !strings.Contains(joined, "FAILED") || !strings.Contains(joined, "ERROR") {
		t.Fatalf("extractFailures missing pytest markers; got %q", f)
	}
}

func TestExtractFailuresJUnit(t *testing.T) {
	out := "Tests run: 5, Failures: 2, Errors: 0, Skipped: 0\n"
	f := extractFailures([]byte(out))
	if len(f) == 0 || !strings.Contains(f[0], "Tests run: 5, Failures: 2") {
		t.Fatalf("extractFailures missing junit summary; got %q", f)
	}
}

// TestExtractFailuresHardStructureLineIsAlwaysCaptured pins the guarantee a
// structural gate's repair step depends on: a "HARD <check>: <path> ...
// <fix hint>" line (check_go_structure.py's HARD comment-block/file-LOC/
// function-LOC format) is captured by the explicit ^HARD marker, not by the
// output-tail fallback. Without the marker, enough distinct WARN/NOTE lines
// from the same run push the one HARD line - the agent's only actionable
// hint about which file and lines to shorten - out of the tail window
// entirely, and the repair step gets no hint at all.
func TestExtractFailuresHardStructureLineIsAlwaysCaptured(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("HARD comment block: internal/foo/bar.go L5-L40 (36 lines, max 30). Move the rationale to docs/ and link it.\n")
	for i := 0; i < 25; i++ {
		sb.WriteString(fmt.Sprintf("WARN file LOC: internal/other/noise%d.go has 600 lines (soft max 500). Consider splitting soon.\n", i))
	}
	sb.WriteString("check_go_structure: 1 hard violation(s), 25 warning(s).\n")
	f := extractFailures([]byte(sb.String()))
	joined := strings.Join(f, "\n")
	if !strings.Contains(joined, "HARD comment block: internal/foo/bar.go L5-L40") {
		t.Fatalf("extractFailures dropped the HARD comment-block hint when buried above the tail window; got %q", f)
	}
}

func TestExtractFailuresFallbackTail(t *testing.T) {
	out := "line one\nline two\nsummary line\n"
	f := extractFailures([]byte(out))
	if len(f) != 3 || f[2] != "summary line" {
		t.Fatalf("fallback must keep the output tail in order; got %q", f)
	}
}

func TestExtractFailuresBounded(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 50; i++ {
		sb.WriteString("--- FAIL: TestN")
		sb.WriteString(string(rune('A' + i%26)))
		sb.WriteString("\n")
	}
	f := extractFailures([]byte(sb.String()))
	if len(f) != maxFailureLines {
		t.Fatalf("extractFailures = %d entries, want cap %d", len(f), maxFailureLines)
	}
}

func TestExtractFailuresLineBound(t *testing.T) {
	long := "--- FAIL: " + strings.Repeat("x", maxFailureLineBytes+100)
	f := extractFailures([]byte(long))
	if len(f) != 1 {
		t.Fatalf("expected one failure, got %d", len(f))
	}
	if len([]rune(f[0])) > maxFailureLineBytes+1 {
		t.Fatalf("failure line not bounded: %d runes", len([]rune(f[0])))
	}
}

func TestExtractFailuresPassingOutputEmpty(t *testing.T) {
	f := extractFailures([]byte("ok  github.com/x/pkg\t0.1s\nok  github.com/y/pkg\t0.2s\n"))
	if len(f) != 2 {
		t.Fatalf("fallback should surface the tail of a short passing output; got %q", f)
	}
}

func TestFailureEvidenceCarriesFailures(t *testing.T) {
	failure := &commandFailure{
		class:    "source",
		detail:   "full diagnostic",
		failures: []string{"--- FAIL: TestAlpha"},
		err:      errors.New("boom"),
	}
	class, detail, failures := failureEvidence(failure)
	if class != "source" || detail != "full diagnostic" {
		t.Fatalf("failureEvidence = %q %q", class, detail)
	}
	if len(failures) != 1 || failures[0] != "--- FAIL: TestAlpha" {
		t.Fatalf("failureEvidence failures = %q", failures)
	}
}

func TestFailureEvidencePlainErrorNoFailures(t *testing.T) {
	class, detail, failures := failureEvidence(errors.New("plain"))
	if class != "source" || detail != "host verifier command failed" || failures != nil {
		t.Fatalf("failureEvidence = %q %q %v", class, detail, failures)
	}
}

func TestCheckJSONIncludesFailures(t *testing.T) {
	c := Check{Name: "go-test", Status: "failed", Class: "source", Detail: "d", Failures: []string{"--- FAIL: T"}}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"failures":["--- FAIL: T"]`) {
		t.Fatalf("check JSON missing failures: %s", b)
	}
}

func TestCommandProfileFailuresPopulated(t *testing.T) {
	profile, err := NewCommandProfile("check", "true", nil)
	if err != nil {
		t.Fatal(err)
	}
	cp := profile.(*CommandProfile)
	cp.run = func(ctx context.Context, workDir, program string, args ...string) error {
		return &commandFailure{
			class:    "source",
			detail:   "d",
			failures: []string{"--- FAIL: TestAlpha"},
			err:      errors.New("source check failed"),
		}
	}
	res, err := cp.Verify(context.Background(), Request{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "failed" || len(res.Checks) != 1 {
		t.Fatalf("result = %#v", res)
	}
	check := res.Checks[0]
	if len(check.Failures) != 1 || check.Failures[0] != "--- FAIL: TestAlpha" {
		t.Fatalf("check failures = %q", check.Failures)
	}
}
