package delivery

import (
	"context"
	"errors"
	"os/exec"
)

// mergeBaseIsAncestor reports whether ancestor is an ancestor of descendant,
// using `git merge-base --is-ancestor` under the pinned context. It separates
// the three outcomes the exit code encodes: exit 0 means yes, exit 1 means no
// (a genuine non-ancestor relation), and any other failure - a missing or
// corrupt object (exit 128), or a non-git error from a fake runner - is a
// recoverable git failure surfaced as an error. Callers must fail closed on
// that error instead of reading it as "not an ancestor": a permanently deleted
// base would otherwise be treated as a recoverable condition and retried
// forever.
//
// baseStillContains deliberately does NOT use this helper: its merge-base
// errors are already plain and recoverable, and it distinguishes a missing
// object from a rewrite by fetching and retesting once.
func mergeBaseIsAncestor(ctx context.Context, git GitRunner, gc GitContext, ancestor, descendant string) (bool, error) {
	_, err := git.Run(ctx, gc, "merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		// Not a git exit failure at all (a fake runner, a plain error): treat
		// it as a recoverable failure rather than guessing an ancestry verdict.
		return false, err
	}
	if exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// AncestryUnverifiableError marks a delivery-time base-ancestry check that
// git itself could not complete (exit other than 0/1: a missing or corrupt
// object). It is a recoverable condition, not a rewrite verdict and not a
// repairable change defect: RepairTarget yields no step for it, so the run
// stays delivery_pending with a recorded cause and a later attempt retries.
// The struct wraps the underlying git failure (Unwrap) so errors.Is and
// errors.As keep traversing the cause chain - a wrapped connect fault, for
// example, still classifies as transient on the delivery settle paths.
type AncestryUnverifiableError struct {
	Reason string
	cause  error
}

// Error implements error.
func (e *AncestryUnverifiableError) Error() string { return e.Reason }

// Unwrap returns the underlying git failure so the cause chain is preserved.
func (e *AncestryUnverifiableError) Unwrap() error { return e.cause }

// IsAncestryUnverifiable reports whether err is an AncestryUnverifiableError
// (possibly wrapped).
func IsAncestryUnverifiable(err error) bool {
	var ae *AncestryUnverifiableError
	return errors.As(err, &ae)
}
