package localengine

// Fresh-start admission identity: pinNewRunIdentity resolves the run's real
// base ref/commit, and the helpers behind its two sources of truth (a git
// worktree, or the local workspace fallback). Kept separate from engine.go so
// the fresh-start admission rules stay in one place with their cleanup.

import (
	"context"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowspace "github.com/MiviaLabs/mivia-agent/internal/workflows/workspace"
)

// workflowBranchPrefix is the branch prefix for a run's git worktree branch.
// The workspace and cli packages each declare an unexported copy; this package
// keeps its own because it must pass a prefix to vcs.RemoveWithPrefix during
// admission cleanup. It must match workflowspace's prefix exactly, since
// creation and cleanup must agree on the branch namespace.
const workflowBranchPrefix = "wf/"

// pinNewRunIdentity resolves the fresh run's real base ref/commit and pins it
// on admission, preferring a git worktree and falling back to the local
// workspace's checked-out identity. It fails closed (returns an error)
// instead of leaving newRunController's placeholder Admission (BaseRef
// "main", BaseCommit "test-base") baked into the durable admission record
// when neither source can resolve a real base.
//
// When the worktree path creates a real run worktree, pinNewRunIdentity
// returns a non-nil cleanup closure. startNew must call it if SetAdmission or
// StartNew fails AFTER admission succeeded, so the pre-created worktree is not
// leaked. The cleanup closure is nil on the local-identity fallback, which
// fabricates no on-disk worktree.
func (e *Engine) pinNewRunIdentity(ctx context.Context, ctrl *controller.LinearController, compiled *compiler.CompiledWorkflow, admission *controller.Admission, runID string, inputSnapshot map[string]string) (cleanup func(), err error) {
	// Create (or validate) the run's git worktree and record the identity on
	// the engine so workflow_deliver resolves the run's real git directory.
	if identity, ok := e.ensureRunWorktree(ctx, runID, nil); ok {
		cleanup = func() { e.removeFreshWorktree(identity) }
		admission.BaseRef, admission.BaseCommit, admission.OriginBaseCommit, admission.WorktreeName = identity.BaseRef, identity.BaseCommit, identity.OriginBaseCommit, identity.WorktreeName
		if compiled.Delivery != nil && compiled.DeliveryActive() {
			url, originBaseCommit, uerr := resolveOriginURL(ctx, e.admissionFetchTimeout(), identity, delivery.EffectiveBase(compiled, inputSnapshot))
			if uerr != nil {
				e.removeFreshWorktree(identity)
				return nil, fmt.Errorf("resolve delivery origin: %w", uerr)
			}
			// Pin the TARGET's fetched origin tip (rewrite detection compares
			// against the target's history), not identity's source-derived one.
			admission.RemoteURL = url
			admission.OriginBaseCommit = originBaseCommit
		}
		// Pin the run's git context for the fail-fast diff-size gate. This is
		// the FRESH-start path: stacking chunk runs are fresh engine starts,
		// so without it the gate would never fire for them.
		if serr := ctrl.WireGitContext(identity.MainRoot, identity.WorktreeName, identity.Root); serr != nil {
			e.removeFreshWorktree(identity)
			return nil, serr
		}
		return cleanup, nil
	}
	if base, commit, wt, rerr := resolveLocalIdentity(e.WorkspaceRoot, runID); rerr == nil {
		// Delivery unconditionally requires a real worktree: the local
		// fallback fabricates WorktreeName "workflow-<runID>" with nothing on
		// disk, so a delivery-active run admitted here would burn its whole
		// body and then permanently refuse at delivery. Fail fast instead.
		if compiled.Delivery != nil && compiled.DeliveryActive() {
			return nil, fmt.Errorf("delivery-active workflow cannot admit without a run worktree")
		}
		admission.BaseRef, admission.BaseCommit, admission.WorktreeName = base, commit, wt
		return nil, nil
	} else {
		// Neither the worktree path nor the local-identity fallback could
		// resolve a real base ref/commit: admitting the run now would bake
		// the newRunController placeholder into the durable admission
		// record. Fail closed instead of starting a run pinned to a
		// fabricated base.
		return nil, fmt.Errorf("resolve workflow base identity: %w", rerr)
	}
}

// admissionFetchTimeout returns the bound for the fresh-admission origin
// fetch. A zero or negative DeliveryTimeout uses 2 minutes, mirroring the
// deliver path (engine_deliver.go). The bound makes run creation fail closed
// - and the pre-created worktree is removed - instead of blocking forever on
// an offline or hung origin.
func (e *Engine) admissionFetchTimeout() time.Duration {
	timeout := e.DeliveryTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	return timeout
}

// removeFreshWorktree removes the worktree a fresh admission created before a
// later admission step failed, so a failed Start does not leak the run
// worktree. It preserves the wf/ branch, matching the CLI's cleanup behavior.
// The cleanup deliberately uses context.Background: it is best-effort and
// must still run when the admission context was already canceled. pinNewRunIdentity
// is only ever reached from the fresh-start path, so removing here can never
// touch a resumed run's worktree.
func (e *Engine) removeFreshWorktree(identity workflowspace.Identity) {
	_ = vcs.RemoveWithPrefix(context.Background(), identity.MainRoot, identity.WorktreeName, workflowBranchPrefix)
}
