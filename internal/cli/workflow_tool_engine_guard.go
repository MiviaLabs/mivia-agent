package cli

import (
	"context"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// sessionActiveRun tracks one in-process session-driven workflow run so
// Cancel and the run's own completion goroutine can coordinate.
type sessionActiveRun struct {
	cancel  context.CancelFunc
	done    chan struct{}
	closeFn func()
	// runner is the exact coordinator runner this run dispatches panel
	// children with, when the controller uses one. Cancel reuses it (see
	// cliPanelCancelCoordinator) so a live panel member is genuinely
	// canceled instead of only having its claim refused (D15).
	runner *controller.CoordinatorRunner
	// resourceGuard serializes runner-backed coordinator reuse against
	// closeFn's teardown: the run's own completion goroutine closes runner's
	// backing store via closeFn, and without this guard Cancel could reuse
	// runner after that store already closed. Every use of runner must hold
	// resourceGuard.RLock() for its full duration; closeGuarded holds
	// resourceGuard.Lock() around closeFn and the closed flag it sets.
	resourceGuard sync.RWMutex
	closed        bool
}

// useLiveCoordinator runs use with the run's live coordinator while holding
// resourceGuard for use's full duration, so closeGuarded cannot close the
// backing store mid-use. It reports used=false (without calling use) when
// there is no runner or the run's resources already closed - the caller must
// fall back to a freshly built coordinator in that case, never assume runner
// is still usable from a bare nil check.
func (a *sessionActiveRun) useLiveCoordinator(use func(coordinator.Coordinator) error) (used bool, err error) {
	if a == nil || a.runner == nil {
		return false, nil
	}
	a.resourceGuard.RLock()
	defer a.resourceGuard.RUnlock()
	if a.closed {
		return false, nil
	}
	return true, use(a.runner.Coordinator)
}

// closeGuarded runs closeFn under resourceGuard's write lock and marks the
// run's resources closed, so a concurrent useLiveCoordinator call either
// completes first (and closeGuarded waits its turn) or observes closed=true
// and falls back instead of touching a closing/closed store.
func (a *sessionActiveRun) closeGuarded() {
	if a == nil {
		return
	}
	a.resourceGuard.Lock()
	defer a.resourceGuard.Unlock()
	if a.closeFn != nil {
		a.closeFn()
	}
	a.closed = true
}

// cancelRunWithGuardedCoordinator runs CancelRunWithAttemptsWithClaim,
// reusing active's live coordinator only while resourceGuard proves its
// backing store has not closed: Cancel's earlier stopActive wait only
// confirms the run loop exited, not that its resources are still open, so a
// bare active.runner read could hand CancelRunWithAttemptsWithClaim a
// coordinator whose store already closed. useLiveCoordinator holds the
// guard for this call's full duration, which also makes closeGuarded wait
// its turn instead of closing mid-cancel. It falls back to a fresh
// store-backed coordinator when active has no usable live runner.
func cancelRunWithGuardedCoordinator(ctx context.Context, active *sessionActiveRun, repo workflowledger.Repository, store *storage.SQLite, runID, holder string) ([]workflowledger.StepAttempt, error) {
	var attempts []workflowledger.StepAttempt
	var cancelErr error
	usedLive, _ := active.useLiveCoordinator(func(coord coordinator.Coordinator) error {
		attempts, cancelErr = controller.CancelRunWithAttemptsWithClaim(ctx, repo, coord, runID, holder)
		return cancelErr
	})
	if !usedLive {
		attempts, cancelErr = controller.CancelRunWithAttemptsWithClaim(ctx, repo, cliPanelCancelCoordinator(nil, store), runID, holder)
	}
	return attempts, cancelErr
}
