package durablefence

import (
	"context"
	"errors"
	"sync"
	"testing"
)

var errHeld = errors.New("claim held by another holder")

// fakeOwner is a correct in-memory ownership surface: a fence token makes a
// stale owner's write fail after a takeover.
type fakeOwner struct {
	mu    sync.Mutex
	owner string
	fence uint64
	// held records the fence token each holder saw when it acquired the record.
	seen map[string]uint64
	// unfenced trusts the holder's own belief that it owns the record and
	// never re-reads the current owner. This is the DC-2 defect: a boolean
	// claim flag plus a handle the stale owner acquired before the takeover.
	unfenced bool
	// checkThenAct claims without atomicity, which is the DC-3 defect. The
	// gate widens the window between the check and the act deterministically;
	// without it the window is nanoseconds and the defect hides.
	checkThenAct bool
	gate         func()
	writes       int
}

func newFakeOwner() *fakeOwner {
	return &fakeOwner{seen: map[string]uint64{}}
}

func (f *fakeOwner) claim(_ context.Context, holder string) error {
	if f.checkThenAct {
		f.mu.Lock()
		free := f.owner == "" || f.owner == holder
		f.mu.Unlock()
		if f.gate != nil {
			f.gate()
		}
		if !free {
			return errHeld
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.owner = holder
		f.fence++
		f.seen[holder] = f.fence
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.owner != "" && f.owner != holder {
		return errHeld
	}
	f.owner = holder
	f.fence++
	f.seen[holder] = f.fence
	return nil
}

func (f *fakeOwner) takeover(_ context.Context, holder string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.owner = holder
	f.fence++
	f.seen[holder] = f.fence
	return nil
}

func (f *fakeOwner) mutate(_ context.Context, holder string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unfenced {
		// The defect: the write is authorized by the holder's belief that it
		// claimed the record once, never by the record's current owner.
		if f.seen[holder] == 0 {
			return errHeld
		}
		f.writes++
		return nil
	}
	if f.owner != holder || f.seen[holder] != f.fence {
		return errHeld
	}
	f.writes++
	return nil
}

func (f *fakeOwner) release(_ context.Context, holder string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.owner != holder {
		return errHeld
	}
	f.owner = ""
	return nil
}

func (f *fakeOwner) scenario() Scenario {
	return Scenario{
		Name:     "fake owner",
		Claim:    f.claim,
		Takeover: f.takeover,
		Mutate:   f.mutate,
		Release:  f.release,
		IsHeld:   ErrIs(errHeld),
	}
}

func TestDurableFenceAcceptsAFencedSurface(t *testing.T) {
	Run(t, "fenced", func(testing.TB) Scenario { return newFakeOwner().scenario() })
}

// recorder captures whether a check failed, so a test can assert the harness
// rejects a defective surface instead of passing it.
type recorder struct {
	testing.TB
	failed bool
}

func (r *recorder) Helper()                           {}
func (r *recorder) Errorf(string, ...any)             { r.failed = true }
func (r *recorder) Fatalf(format string, args ...any) { r.failed = true; panic(sentinelStop{}) }

type sentinelStop struct{}

func runCheck(t *testing.T, check func(testing.TB, Scenario), s Scenario) bool {
	t.Helper()
	rec := &recorder{TB: t}
	func() {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(sentinelStop); !ok {
					panic(r)
				}
			}
		}()
		check(rec, s)
	}()
	return rec.failed
}

func TestTakeoverCheckRejectsAnUnfencedSurface(t *testing.T) {
	owner := newFakeOwner()
	owner.unfenced = true
	if !runCheck(t, CheckTakeoverFencesPreviousOwner, owner.scenario()) {
		t.Fatal("CheckTakeoverFencesPreviousOwner passed a surface with no fence")
	}
	// The exclusivity check alone does not catch it, which is why the takeover
	// check exists as a separate gate.
	unfenced := newFakeOwner()
	unfenced.unfenced = true
	if runCheck(t, CheckClaimIsExclusive, unfenced.scenario()) {
		t.Fatal("CheckClaimIsExclusive should still pass an unfenced surface")
	}
}

func TestConcurrentCheckRejectsCheckThenActClaim(t *testing.T) {
	owner := newFakeOwner()
	owner.checkThenAct = true
	// Every claimer must finish its check before any of them acts. Repeating
	// the check without this barrier proves nothing: the natural window
	// between the two halves is nanoseconds and one claimer wins every time.
	var barrier sync.WaitGroup
	barrier.Add(ConcurrentHolders)
	released := make(chan struct{})
	owner.gate = func() {
		barrier.Done()
		<-released
	}
	go func() {
		barrier.Wait()
		close(released)
	}()
	if !runCheck(t, CheckConcurrentClaimAdmitsOne, owner.scenario()) {
		t.Fatal("CheckConcurrentClaimAdmitsOne passed a check-then-act claim")
	}
}

func TestReleaseCheckRejectsAnyCallerRelease(t *testing.T) {
	owner := newFakeOwner()
	scenario := owner.scenario()
	scenario.Release = func(ctx context.Context, _ string) error {
		return owner.release(ctx, owner.owner)
	}
	if !runCheck(t, CheckReleaseIsHolderOnly, scenario) {
		t.Fatal("CheckReleaseIsHolderOnly passed a release any caller may perform")
	}
}

func TestScenarioReportsMissingFunctions(t *testing.T) {
	rec := &recorder{TB: t}
	func() {
		defer func() { _ = recover() }()
		Scenario{Name: "empty"}.require(rec)
	}()
	if !rec.failed {
		t.Fatal("an incomplete scenario must fail loudly, not run partial checks")
	}
}

func TestFencedFallsBackToIsHeld(t *testing.T) {
	s := Scenario{IsHeld: ErrIs(errHeld)}
	if !s.fenced(errHeld) {
		t.Fatal("fenced must fall back to IsHeld when IsFenced is nil")
	}
	other := errors.New("other")
	s.IsFenced = ErrIs(other)
	if s.fenced(errHeld) || !s.fenced(other) {
		t.Fatal("fenced must prefer IsFenced when it is set")
	}
}
