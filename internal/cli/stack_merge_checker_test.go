package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
)

// fakeMergePRClient is a test double for delivery.PRClient that returns a
// configured error from IsMerged.
type fakeMergePRClient struct {
	isMergedErr error
}

func (f *fakeMergePRClient) FindByHead(context.Context, string, string) (*delivery.PRRef, error) {
	return nil, nil
}

func (f *fakeMergePRClient) Create(context.Context, string, delivery.PRInput) (delivery.PRRef, error) {
	return delivery.PRRef{}, nil
}

func (f *fakeMergePRClient) IsMerged(context.Context, string, string) (bool, error) {
	return false, f.isMergedErr
}

// exitOneGitRunner returns an exec.ExitError with code 1 for every command,
// simulating "not an ancestor" so Merged falls through to the remote check.
type exitOneGitRunner struct{}

func (exitOneGitRunner) Run(context.Context, delivery.GitContext, ...string) (string, error) {
	return "", &exec.ExitError{ProcessState: fakeExitStatus(1)}
}

func fakeExitStatus(code int) *os.ProcessState {
	// os.ProcessState cannot be constructed directly; use a helper process that
	// exits with the requested code.
	cmd := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code))
	_ = cmd.Run()
	return cmd.ProcessState
}

func TestMergeCheckerPropagatesProbeError(t *testing.T) {
	probeErr := errors.New("github api unavailable")
	checker := gitMergeChecker{
		git: exitOneGitRunner{},
		pr:  &fakeMergePRClient{isMergedErr: probeErr},
		gc:  delivery.GitContext{},
	}

	merged, err := checker.Merged(context.Background(), "feature/x", "main", "abc123", "owner/repo", true)
	if merged {
		t.Errorf("Merged = true, want false")
	}
	if err == nil {
		t.Fatalf("Merged error = nil, want error wrapping probe error")
	}
	if !errors.Is(err, probeErr) {
		t.Errorf("Merged error = %v, want error wrapping %v", err, probeErr)
	}
}
