package localengine

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
)

// TestPanelCancelCoordinatorReusesLiveInstance is a regression test: Engine's
// panelCancelCoordinator used to build a brand-new coordinator.Coordinator
// via e.NewRunner() on every Cancel call, instead of reusing the coordinator
// instance actually executing the run's in-flight Advance() call. A fresh
// coordinator instance has its own empty in-memory handle map and its own
// claim-holder identity (see coordinator.New / newCoordinatorHolderID), so it
// can never find or genuinely cancel a live panel member dispatched by the
// real coordinator: JoinAsRecovered always misses its empty handle map, falls
// through to the ledger, and cancelRecovered then refuses outright because
// the live dispatcher still holds the child's execution claim (D15 broken).
//
// panelCancelCoordinator must instead return the exact coordinator instance
// stored on the active run's controller (active.ctrl.Runner), since that is
// the one and only instance NewLinearController was built with and Advance
// dispatches panel children through for that run's lifetime.
func TestPanelCancelCoordinatorReusesLiveInstance(t *testing.T) {
	live := coordinator.New(nil, nil)
	liveRunner := controller.NewCoordinatorRunner(live)
	fresh := coordinator.New(nil, nil)
	freshRunner := controller.NewCoordinatorRunner(fresh)

	e := &Engine{
		NewRunner: func() controller.AgentStepRunner { return freshRunner },
	}
	active := &activeRun{ctrl: &controller.LinearController{Runner: liveRunner}}

	if got := e.panelCancelCoordinator(active); got != live {
		t.Fatalf("panelCancelCoordinator(active) = %p, want the live run's own dispatching coordinator %p (must reuse it, not build a fresh one via NewRunner)", got, live)
	}

	// With no run active in this process (e.g. a cross-process/recovered
	// cancel), there is no live in-process dispatcher to reuse; the fallback
	// to a fresh coordinator built from NewRunner is preserved.
	if got := e.panelCancelCoordinator(nil); got != fresh {
		t.Fatalf("panelCancelCoordinator(nil) = %p, want fallback fresh coordinator %p", got, fresh)
	}
}
