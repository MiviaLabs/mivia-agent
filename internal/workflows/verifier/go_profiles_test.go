package verifier

import (
	"context"
	"errors"
	"testing"
)

// TestGoProfileSurfacesContextErrorFromRun verifies that a context error
// surfaced by a sandboxed run is returned to the controller instead of being
// swallowed into a failed host-class check with a nil error. The controller
// detects the context error and settles the run as timed_out; swallowing it
// would fabricate a host failure. Regression for
// verifier-gate-deadline-swallowed-as-host-failure.
func TestGoProfileSurfacesContextErrorFromRun(t *testing.T) {
	profile := newGoProfile("context-timeout", []commandSpec{
		{check: "first", program: "go", args: []string{"test", "./..."}},
		{check: "second", program: "go", args: []string{"vet", "./..."}},
	}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	profile.run = func(ctx context.Context, workDir, program string, args ...string) error {
		if len(args) > 0 && args[0] == "test" {
			// The first check fails normally so the returned result is a
			// failed result, not a passed one.
			return &commandFailure{class: "source", detail: "tests failed", err: errors.New("source check failed")}
		}
		cancel()
		return hostFailure(ctx.Err())
	}
	result, err := profile.Verify(ctx, Request{WorkDir: t.TempDir()})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Verify err = %v, want context.Canceled", err)
	}
	if result.Status != "failed" {
		t.Fatalf("result status = %q, want failed", result.Status)
	}
}
