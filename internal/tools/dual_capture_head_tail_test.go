package tools

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// TestDualCaptureKeepsErrorTailUnderBudget: under a tight shared max, a large
// stdout flood must not drop the late stderr failure tail (compiler errors
// print last). Head+tail retention + elision marker is required.
func TestDualCaptureKeepsErrorTailUnderBudget(t *testing.T) {
	const max = 90
	d := newDualCapture(max)
	// Simulate a noisy build: lots of progress on stdout, then the error.
	_, _ = d.Stdout().Write([]byte(strings.Repeat("build-line\n", 40))) // 440 bytes
	const errTail = "ERROR: undefined: FooBar in main.go:42\n"
	_, _ = d.Stderr().Write([]byte(errTail))

	if !d.Truncated() {
		t.Fatal("expected truncation under tight budget")
	}
	if d.Retained() > max {
		t.Fatalf("retained=%d > max=%d", d.Retained(), max)
	}
	stderr := d.StderrString()
	if !strings.Contains(stderr, "ERROR: undefined: FooBar") {
		t.Fatalf("failing-build error tail lost from stderr: %q", stderr)
	}
	stdout := d.StdoutString()
	if !strings.Contains(stdout, captureElisionMarker) && len(stdout) >= max {
		// When stdout alone exceeded the budget, elision should appear there.
		t.Fatalf("stdout should show head/tail elision when flooded: %q", stdout[:min(len(stdout), 200)])
	}
}

// TestDualCaptureNoFalseElisionWhenUnderBudget: head filling alone must not
// mark truncated/elide when total written ≤ max (no middle dropped). max=90
// → headQuota=30, tailQuota=60; a 50-byte stdout write spans head+tail without
// discard and must return the exact payload with no elision marker.
func TestDualCaptureNoFalseElisionWhenUnderBudget(t *testing.T) {
	const max = 90
	payload := strings.Repeat("x", 50)
	d := newDualCapture(max)
	n, err := d.Stdout().Write([]byte(payload))
	if err != nil || n != 50 {
		t.Fatalf("Write n=%d err=%v", n, err)
	}
	if d.Truncated() {
		t.Fatal("Truncated() true when written (50) ≤ max (90); middle was not dropped")
	}
	got := d.StdoutString()
	if got != payload {
		t.Fatalf("StdoutString = %q (len=%d), want exact 50-byte payload", got, len(got))
	}
	if strings.Contains(got, captureElisionMarker) {
		t.Fatalf("false elision marker under budget: %q", got)
	}
}

// TestCappedBufferNoFalseElisionWhenUnderBudget mirrors dualCapture: filling
// headQuota must not set truncated when all bytes fit in head+tail.
func TestCappedBufferNoFalseElisionWhenUnderBudget(t *testing.T) {
	const max = 90
	payload := strings.Repeat("y", 50)
	c := newCappedBuffer(max)
	n, err := c.Write([]byte(payload))
	if err != nil || n != 50 {
		t.Fatalf("Write n=%d err=%v", n, err)
	}
	if c.Truncated() {
		t.Fatal("Truncated() true when written (50) ≤ max (90); middle was not dropped")
	}
	got := string(c.Bytes())
	if got != payload {
		t.Fatalf("Bytes = %q (len=%d), want exact 50-byte payload", got, len(got))
	}
	if strings.Contains(got, captureElisionMarker) {
		t.Fatalf("false elision marker under budget: %q", got)
	}
}

// TestRunCommandFailingBuildKeepsErrorTail: end-to-end through run_command with
// a tight max_output_bytes — the process prints noise then a late error on
// stderr; the model-visible body must keep that error and exit framing.
func TestRunCommandFailingBuildKeepsErrorTail(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh path")
	}
	dir := t.TempDir()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	const maxOut = 256
	reg := NewDefaultRegistry(DefaultOptions{
		Workspace: ws, RunAllowlist: []string{"sh"},
		RunTimeoutSec: 5, MaxOutputBytes: maxOut,
	})
	// Large stdout then a distinctive error on stderr; non-zero exit.
	script := `i=0; while [ $i -lt 200 ]; do echo "compile unit $i ok"; i=$((i+1)); done; echo "ERROR: undefined: UniqueSymbolXYZ" >&2; exit 1`
	raw, _ := json.Marshal(map[string]any{"argv": []string{"sh", "-c", script}})
	out, err := reg.Execute(context.Background(), "run_command", raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "UniqueSymbolXYZ") {
		t.Fatalf("failing-build error tail missing from result:\n%s", out[:min(len(out), 800)])
	}
	if !strings.Contains(out, "exit=") && !strings.Contains(out, "exit status") && !strings.Contains(out, "exit code") {
		// Header carries exit status outside the capture buffer.
		if !strings.Contains(strings.ToLower(out), "exit") {
			t.Fatalf("exit framing missing from result head:\n%s", out[:min(len(out), 400)])
		}
	}
	if !strings.Contains(out, "truncated") && !strings.Contains(out, "elided") {
		// Either tool-level truncation notice or stream elision marker.
		t.Fatalf("expected truncation/elision signal under tight max_output_bytes:\n%s", out[:min(len(out), 400)])
	}
}

func TestDualCaptureDoesNotAllocateDefaultTailBeforeOutputNeedsIt(t *testing.T) {
	d := newDualCapture(defaultMemoryBackstopBytes)
	if len(d.ring) != 0 || len(d.ringOut) != 0 {
		t.Fatalf("default tail allocated eagerly: bytes=%d tags=%d", len(d.ring), len(d.ringOut))
	}
	if _, err := d.Stdout().Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	if len(d.ring) != 0 || len(d.ringOut) != 0 {
		t.Fatalf("small output allocated tail: bytes=%d tags=%d", len(d.ring), len(d.ringOut))
	}
}
