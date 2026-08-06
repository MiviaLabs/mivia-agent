//go:build integration

package verifier

import (
	"context"
	"errors"
	"testing"
)

func TestSandboxRunsRepositoryProfile(t *testing.T) {
	if _, err := sandboxBubblewrapPath(); err != nil {
		t.Fatalf("bubblewrap is unavailable: %v", err)
	}
	root := sandboxRepositoryRoot(t)
	baseline, err := CaptureGoModuleBaseline(root)
	if err != nil {
		t.Fatal(err)
	}
	err = runSandboxedCommand(context.Background(), root, baseline, secretPolicy(t), "go", "test", "./...")
	if err == nil {
		return
	}
	var failure *commandFailure
	if errors.As(err, &failure) {
		t.Fatalf("sandbox error = %v; detail = %q", err, failure.detail)
	}
	t.Fatal(err)
}
