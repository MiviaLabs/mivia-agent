package delivery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// scriptedGit fakes GitRunner with a per-command script: each call maps its
// full joined argument list to a canned (output, error). An unexpected call
// fails the test, which pins the exact git call sequence a helper makes.
type scriptedGit struct {
	t      *testing.T
	script map[string]scriptedResult
	calls  []string
}

type scriptedResult struct {
	out string
	err error
}

func (g *scriptedGit) Run(ctx context.Context, gc GitContext, args ...string) (string, error) {
	key := strings.Join(args, " ")
	g.calls = append(g.calls, key)
	res, ok := g.script[key]
	if !ok {
		g.t.Fatalf("unexpected git call: %q", key)
	}
	return res.out, res.err
}

// gitExitErr returns a real *exec.ExitError with the given exit code, so the
// error carries a genuine ProcessState exactly like RealGit's wrapping does.
// It re-executes the running test binary with a sentinel environment variable
// (guarded at init so only the spawned child calls os.Exit) instead of a shell,
// because the vcs/ guard forbids shell execution of git anywhere in the repo.
func gitExitErr(t *testing.T, code int) error {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	cmd := exec.Command(exe, "-test.run=^TestGitExitChild$")
	cmd.Env = append(os.Environ(), fmt.Sprintf("GIT_EXIT_CHILD=%d", code))
	err = cmd.Run()
	if err == nil {
		t.Fatal("exit subprocess unexpectedly succeeded")
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("exit subprocess error = %T, want *exec.ExitError", err)
	}
	if got := ee.ExitCode(); got != code {
		t.Fatalf("exit subprocess code = %d, want %d", got, code)
	}
	return err
}

// TestGitExitChild is the re-exec entry point for gitExitErr: when invoked as
// a child process (recognized by the sentinel env var), it exits immediately
// with the requested code.
func TestGitExitChild(t *testing.T) {
	raw := os.Getenv("GIT_EXIT_CHILD")
	if raw == "" {
		t.Skip("not a gitExitErr child invocation")
	}
	code, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("child exit code %q is not an integer", raw)
	}
	os.Exit(code)
}

// TestMergeBaseIsAncestorClassifiesExitCodes pins F-4's core classification:
// exit 0 means "is an ancestor", exit 1 means "genuinely not an ancestor",
// and any other failure (exit 128, a missing or corrupt object, or a plain
// non-git error from a fake runner) is a recoverable git failure that must
// surface as an error, never as a silently false ancestry verdict.
func TestMergeBaseIsAncestorClassifiesExitCodes(t *testing.T) {
	ctx := context.Background()
	gc := GitContext{Dir: "/repo", GitDir: "/repo/.git"}

	t.Run("exit 0 admits ancestry", func(t *testing.T) {
		git := &scriptedGit{t: t, script: map[string]scriptedResult{
			"merge-base --is-ancestor a b": {out: "", err: nil},
		}}
		ok, err := mergeBaseIsAncestor(ctx, git, gc, "a", "b")
		if err != nil {
			t.Fatalf("mergeBaseIsAncestor = %v, want nil for exit 0", err)
		}
		if !ok {
			t.Fatal("mergeBaseIsAncestor = false, want true for exit 0")
		}
	})

	t.Run("exit 1 is a genuine non-ancestor", func(t *testing.T) {
		git := &scriptedGit{t: t, script: map[string]scriptedResult{
			"merge-base --is-ancestor a b": {out: "", err: gitExitErr(t, 1)},
		}}
		ok, err := mergeBaseIsAncestor(ctx, git, gc, "a", "b")
		if err != nil {
			t.Fatalf("mergeBaseIsAncestor = %v, want nil for exit 1", err)
		}
		if ok {
			t.Fatal("mergeBaseIsAncestor = true, want false for exit 1")
		}
	})

	t.Run("exit 128 is a recoverable git failure", func(t *testing.T) {
		git := &scriptedGit{t: t, script: map[string]scriptedResult{
			"merge-base --is-ancestor a b": {out: "", err: gitExitErr(t, 128)},
		}}
		ok, err := mergeBaseIsAncestor(ctx, git, gc, "a", "b")
		if ok {
			t.Fatal("mergeBaseIsAncestor = true, want false for exit 128")
		}
		if err == nil {
			t.Fatal("mergeBaseIsAncestor = nil error, want the git failure surfaced")
		}
	})

	t.Run("non-exit error is a recoverable git failure", func(t *testing.T) {
		git := &scriptedGit{t: t, script: map[string]scriptedResult{
			"merge-base --is-ancestor a b": {out: "", err: errors.New("boom")},
		}}
		ok, err := mergeBaseIsAncestor(ctx, git, gc, "a", "b")
		if ok {
			t.Fatal("mergeBaseIsAncestor = true, want false for a plain error")
		}
		if err == nil {
			t.Fatal("mergeBaseIsAncestor = nil error, want the plain error surfaced")
		}
	})
}

// TestVerifyRemoteBaseAncestryClassifiesMergeBaseExitCodes pins F-4's
// delivery-time boundary: only a genuine exit-1 non-ancestor verdict is a
// permanent rewrite refusal; an exit-128 git failure (a missing or corrupt
// object) is a recoverable plain error so the run stays delivery_pending.
func TestVerifyRemoteBaseAncestryClassifiesMergeBaseExitCodes(t *testing.T) {
	ctx := context.Background()
	req := Request{
		Policy:    Policy{Base: "main"},
		OriginURL: "https://example.com/origin.git",
		GitCtx:    GitContext{Dir: "/repo", GitDir: "/repo/.git"},
	}
	fetch := "fetch --no-tags https://example.com/origin.git +refs/heads/main:refs/remotes/origin/main"
	revParse := "rev-parse --verify --end-of-options refs/remotes/origin/main^{commit}"

	t.Run("exit 1 is a permanent refusal", func(t *testing.T) {
		git := &scriptedGit{t: t, script: map[string]scriptedResult{
			fetch:    {out: "", err: nil},
			revParse: {out: "fetched123\n", err: nil},
			"merge-base --is-ancestor origin123 fetched123": {out: "", err: gitExitErr(t, 1)},
		}}
		err := verifyRemoteBaseAncestry(ctx, git, req, "origin123")
		if err == nil {
			t.Fatal("verifyRemoteBaseAncestry = nil, want RefusalError for exit 1")
		}
		if !IsRefusal(err) {
			t.Fatalf("verifyRemoteBaseAncestry err = %v, want RefusalError for a rewritten base", err)
		}
	})

	t.Run("exit 128 is a recoverable ancestry-unverifiable error", func(t *testing.T) {
		git := &scriptedGit{t: t, script: map[string]scriptedResult{
			fetch:    {out: "", err: nil},
			revParse: {out: "fetched123\n", err: nil},
			"merge-base --is-ancestor origin123 fetched123": {out: "", err: gitExitErr(t, 128)},
		}}
		err := verifyRemoteBaseAncestry(ctx, git, req, "origin123")
		if err == nil {
			t.Fatal("verifyRemoteBaseAncestry = nil, want a recoverable error for exit 128")
		}
		if IsRefusal(err) {
			t.Fatalf("verifyRemoteBaseAncestry err = %v, want a plain recoverable error, not RefusalError", err)
		}
		if !IsAncestryUnverifiable(err) {
			t.Fatalf("verifyRemoteBaseAncestry err = %v, want AncestryUnverifiableError so RepairTarget yields no repair route", err)
		}
	})
}

// TestIsAncestryUnverifiable pins the classifier that keeps a delivery-time
// git object failure (exit 128) off the repair loop: true for an
// AncestryUnverifiableError (wrapped or not), false for every other failure
// class and for a plain error.
func TestIsAncestryUnverifiable(t *testing.T) {
	base := &AncestryUnverifiableError{Reason: "cannot verify remote delivery base ancestry"}
	positives := []struct {
		name string
		err  error
	}{
		{"bare", base},
		{"wrapped", fmt.Errorf("deliver: %w", base)},
		{"double wrapped", fmt.Errorf("attempt: %w", fmt.Errorf("deliver: %w", base))},
	}
	for _, tc := range positives {
		t.Run(tc.name, func(t *testing.T) {
			if !IsAncestryUnverifiable(tc.err) {
				t.Fatalf("IsAncestryUnverifiable(%v) = false, want true", tc.err)
			}
		})
	}
	negatives := []struct {
		name string
		err  error
	}{
		{"nil", nil},
		{"plain error", errors.New("boom")},
		{"refusal", &RefusalError{Reason: "refused"}},
		{"wrapped refusal", fmt.Errorf("wrap: %w", &RefusalError{Reason: "refused"})},
		{"diff size", &DiffSizeError{Reason: "too big"}},
		{"pr metadata", &PRMetadataError{Reason: "bad title"}},
	}
	for _, tc := range negatives {
		t.Run(tc.name, func(t *testing.T) {
			if IsAncestryUnverifiable(tc.err) {
				t.Fatalf("IsAncestryUnverifiable(%v) = true, want false", tc.err)
			}
		})
	}
}

// TestAncestryUnverifiablePreservesCauseChain pins the Unwrap contract: the
// typed error wraps the underlying git failure, so errors.As walks through it
// to the original *exec.ExitError - a chain the fallback that only stores a
// formatted string would break. A connect fault buried inside the git error
// must still classify as transient on the delivery settle paths.
func TestAncestryUnverifiablePreservesCauseChain(t *testing.T) {
	underlying := gitExitErr(t, 128)
	err := &AncestryUnverifiableError{
		Reason: fmt.Sprintf("cannot verify remote delivery base %q ancestry (fetch and retry): %v", "main", underlying),
		cause:  underlying,
	}
	if got := errors.Unwrap(err); got != underlying {
		t.Fatalf("errors.Unwrap(err) = %v, want the underlying git failure", got)
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("errors.As(err, *exec.ExitError) = false, want true: the cause chain through AncestryUnverifiableError is broken")
	}
}

// TestAdmitDeliveryTargetMergeBaseExit128FailsClosed pins F-4's admission
// boundary: a first-check merge-base failure with a real git exit code (128,
// a missing or corrupt object) must fail closed immediately. It must NOT
// silently fall into the local-ref fallback, which exists only for the
// ordinary "operator committed locally, not yet pushed" state and would
// otherwise admit a run against a base whose ancestry the delivery engine
// could not verify.
func TestAdmitDeliveryTargetMergeBaseExit128FailsClosed(t *testing.T) {
	ctx := context.Background()
	worktreeBase := "deadbeef"
	git := &scriptedGit{t: t, script: map[string]scriptedResult{
		"remote get-url origin": {out: "https://example.com/origin.git\n", err: nil},
		"fetch --no-tags https://example.com/origin.git +refs/heads/main:refs/remotes/origin/main": {out: "", err: nil},
		"rev-parse --verify --end-of-options refs/remotes/origin/main^{commit}":                    {out: "abc123\n", err: nil},
		"merge-base --is-ancestor deadbeef abc123":                                                 {out: "", err: gitExitErr(t, 128)},
		// The local-ref fallback must never run after a first-check git
		// failure; these entries prove the fallback would have admitted if it
		// had been reached (local main strictly ahead of origin and containing
		// the worktree base).
		"rev-parse --verify --end-of-options refs/heads/main^{commit}": {out: "local123\n", err: nil},
		"merge-base --is-ancestor abc123 local123":                     {out: "", err: nil},
		"merge-base --is-ancestor deadbeef local123":                   {out: "", err: nil},
	}}
	_, _, err := AdmitDeliveryTarget(ctx, git, GitContext{Dir: "/repo", GitDir: "/repo/.git"}, "main", worktreeBase)
	if err == nil {
		t.Fatal("AdmitDeliveryTarget = nil, want an error: an exit-128 first merge-base must fail closed, not admit via the local-ref fallback")
	}
	for _, call := range git.calls {
		// The admission fetch legitimately names refs/heads/main; only the
		// fallback's LOCAL ref resolution must never run.
		if strings.Contains(call, "refs/heads/main^{commit}") {
			t.Fatalf("AdmitDeliveryTarget reached the local-ref fallback (%q) after a first-check git failure; must fail closed", call)
		}
	}
}
