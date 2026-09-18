package chat

// The merge oracle: MergeChecker and its production implementation,
// gitMergeChecker. Split out of stack_reconcile.go to keep that file under
// the repo's per-file line ceiling (.mivia/policy/go-structure.json).
//
// The verdict logic lives in delivery.MergeProbe, the one implementation both
// stack drivers (this package and internal/workflows/localengine) share. This
// file keeps the CLI-facing interface, the sentinel alias, and the adapter.

import (
	"context"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
)

// errMergeProbeUnavailable aliases the shared probe sentinel so existing
// errors.Is call sites in this package keep matching.
var errMergeProbeUnavailable = delivery.ErrMergeProbeUnavailable

// MergeChecker reports whether a chunk's PR is merged from durable git state
// and, when necessary, the remote PR state. Tests inject a fake.
//
// wasPushed is the driver's durable pushed evidence for the run (a delivery
// record that reached pushed/succeeded with a commit SHA). Without it a
// missing remote ref only means "never pushed", not "merged".
type MergeChecker interface {
	Merged(ctx context.Context, headBranch, baseBranch, headCommit, repoSlug string, wasPushed bool) (bool, error)
}

// gitMergeChecker is the MergeChecker over the repository's origin remote.
// It delegates to the shared delivery.MergeProbe so the CLI driver and the
// engine driver cannot drift on merge-verdict semantics.
type gitMergeChecker struct {
	git delivery.GitRunner
	pr  delivery.PRClient
	gc  delivery.GitContext
}

// Merged reports whether the chunk's PR has landed (see delivery.MergeProbe
// for the probe order and the fail-closed unknown verdict).
func (g gitMergeChecker) Merged(ctx context.Context, headBranch, baseBranch, headCommit, repoSlug string, wasPushed bool) (bool, error) {
	return delivery.MergeProbe{Git: g.git, PR: g.pr, GC: g.gc}.Merged(ctx, headBranch, baseBranch, headCommit, repoSlug, wasPushed)
}
