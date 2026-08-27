package clichat

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
)

// fakeMergePRClient is a test double for delivery.PRClient. IsMerged returns
// the configured verdict and error, and records the call count so a test can
// assert that Merged fell through to the remote probe.
type fakeMergePRClient struct {
	merged      bool
	isMergedErr error
	calls       int
}

func (f *fakeMergePRClient) FindByHead(context.Context, string, string) (*delivery.PRRef, error) {
	return nil, nil
}

func (f *fakeMergePRClient) Create(context.Context, string, delivery.PRInput) (delivery.PRRef, error) {
	return delivery.PRRef{}, nil
}

func (f *fakeMergePRClient) IsMerged(context.Context, string, string) (bool, error) {
	f.calls++
	return f.merged, f.isMergedErr
}

// exitCodeGitRunner returns an exec.ExitError with the configured code for
// every command. Code 1 is git's "not an ancestor" answer; code 128 is git's
// answer for a missing ref or a missing commit.
type exitCodeGitRunner struct{ code int }

func (r exitCodeGitRunner) Run(context.Context, delivery.GitContext, ...string) (string, error) {
	return "", &exec.ExitError{ProcessState: fakeExitStatus(r.code)}
}

// plainErrGitRunner fails with an error that is not an exec.ExitError, which
// is what a missing git binary or a context cancellation looks like.
type plainErrGitRunner struct{ err error }

func (r plainErrGitRunner) Run(context.Context, delivery.GitContext, ...string) (string, error) {
	return "", r.err
}

// exitCodeEnv tells the helper process below which exit code to use.
const exitCodeEnv = "MIVIA_TEST_HELPER_EXIT_CODE"

// TestMergeCheckerExitHelperProcess is not a test. It is the helper process
// fakeExitStatus re-executes to get a real exit code. Without the environment
// variable it skips, so a normal run does nothing.
func TestMergeCheckerExitHelperProcess(t *testing.T) {
	raw := os.Getenv(exitCodeEnv)
	if raw == "" {
		t.Skip("helper process; not part of the suite")
	}
	code, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("%s = %q: %v", exitCodeEnv, raw, err)
	}
	os.Exit(code)
}

func fakeExitStatus(code int) *os.ProcessState {
	// os.ProcessState cannot be constructed directly. Re-execute this test
	// binary as a helper process that exits with the requested code. A shell
	// is not used: the repo forbids shell execution in Go sources.
	cmd := exec.Command(os.Args[0], "-test.run=^TestMergeCheckerExitHelperProcess$")
	cmd.Env = append(os.Environ(), fmt.Sprintf("%s=%d", exitCodeEnv, code))
	_ = cmd.Run()
	return cmd.ProcessState
}

// TestMergeCheckerPropagatesProbeError: the local probe answered "not an
// ancestor" and the remote probe failed. No probe knows the verdict, so
// Merged must report the unavailable sentinel and never merged=true.
func TestMergeCheckerPropagatesProbeError(t *testing.T) {
	probeErr := errors.New("github api unavailable")
	checker := gitMergeChecker{
		git: exitCodeGitRunner{code: 1},
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
	if !errors.Is(err, errMergeProbeUnavailable) {
		t.Errorf("Merged error = %v, want errMergeProbeUnavailable", err)
	}
}

// TestMergeCheckerNonAncestorFallsThroughToRemote: git exit 1 says the commit
// is not an ancestor, which a squash merge also produces. The remote probe
// decides.
func TestMergeCheckerNonAncestorFallsThroughToRemote(t *testing.T) {
	pr := &fakeMergePRClient{merged: true}
	checker := gitMergeChecker{git: exitCodeGitRunner{code: 1}, pr: pr, gc: delivery.GitContext{}}

	merged, err := checker.Merged(context.Background(), "feature/x", "main", "abc123", "owner/repo", true)
	if err != nil {
		t.Fatalf("Merged error = %v, want nil", err)
	}
	if !merged {
		t.Errorf("Merged = false, want true from the remote probe")
	}
	if pr.calls != 1 {
		t.Errorf("IsMerged calls = %d, want 1", pr.calls)
	}
}

// TestMergeCheckerMissingRefFallsThroughToRemote: git exit 128 (the base
// remote-tracking ref is gone after the parent chunk squash-merged and the
// branch was pruned) makes the local probe inconclusive, not fatal. The
// remote probe must still answer.
func TestMergeCheckerMissingRefFallsThroughToRemote(t *testing.T) {
	pr := &fakeMergePRClient{merged: true}
	checker := gitMergeChecker{git: exitCodeGitRunner{code: 128}, pr: pr, gc: delivery.GitContext{}}

	merged, err := checker.Merged(context.Background(), "feature/x", "chunk-1", "abc123", "owner/repo", true)
	if err != nil {
		t.Fatalf("Merged error = %v, want nil", err)
	}
	if !merged {
		t.Errorf("Merged = false, want true from the remote probe")
	}
	if pr.calls != 1 {
		t.Errorf("IsMerged calls = %d, want 1", pr.calls)
	}
}

// TestMergeCheckerRemoteReportsNotMerged: a confident remote "not merged"
// after an unavailable local probe is a verdict, not an error.
func TestMergeCheckerRemoteReportsNotMerged(t *testing.T) {
	pr := &fakeMergePRClient{merged: false}
	checker := gitMergeChecker{git: exitCodeGitRunner{code: 128}, pr: pr, gc: delivery.GitContext{}}

	merged, err := checker.Merged(context.Background(), "feature/x", "chunk-1", "abc123", "owner/repo", true)
	if err != nil {
		t.Fatalf("Merged error = %v, want nil", err)
	}
	if merged {
		t.Errorf("Merged = true, want false")
	}
}

// TestMergeCheckerBothProbesUnavailable: the local probe cannot answer (exit
// 128) and the remote probe fails. Merged must report the sentinel and not
// collapse the unknown into "not merged".
func TestMergeCheckerBothProbesUnavailable(t *testing.T) {
	probeErr := errors.New("gh: connection refused")
	pr := &fakeMergePRClient{isMergedErr: probeErr}
	checker := gitMergeChecker{git: exitCodeGitRunner{code: 128}, pr: pr, gc: delivery.GitContext{}}

	merged, err := checker.Merged(context.Background(), "feature/x", "chunk-1", "abc123", "owner/repo", true)
	if merged {
		t.Errorf("Merged = true, want false")
	}
	if !errors.Is(err, errMergeProbeUnavailable) || !errors.Is(err, probeErr) {
		t.Fatalf("Merged error = %v, want errMergeProbeUnavailable wrapping %v", err, probeErr)
	}
}

// TestMergeCheckerLocalUnavailableNoRemoteConfigured: with no remote probe
// configured an unavailable local probe leaves the verdict unknown.
func TestMergeCheckerLocalUnavailableNoRemoteConfigured(t *testing.T) {
	gitErr := errors.New("exec: git: not found")
	checker := gitMergeChecker{git: plainErrGitRunner{err: gitErr}, gc: delivery.GitContext{}}

	merged, err := checker.Merged(context.Background(), "feature/x", "main", "abc123", "", true)
	if merged {
		t.Errorf("Merged = true, want false")
	}
	if !errors.Is(err, errMergeProbeUnavailable) || !errors.Is(err, gitErr) {
		t.Fatalf("Merged error = %v, want errMergeProbeUnavailable wrapping %v", err, gitErr)
	}
}

// TestMergeCheckerNonAncestorNoRemoteConfigured: the local probe answered.
// Without a remote probe that answer stands as "not merged".
func TestMergeCheckerNonAncestorNoRemoteConfigured(t *testing.T) {
	checker := gitMergeChecker{git: exitCodeGitRunner{code: 1}, gc: delivery.GitContext{}}

	merged, err := checker.Merged(context.Background(), "feature/x", "main", "abc123", "", true)
	if err != nil {
		t.Fatalf("Merged error = %v, want nil", err)
	}
	if merged {
		t.Errorf("Merged = true, want false")
	}
}
