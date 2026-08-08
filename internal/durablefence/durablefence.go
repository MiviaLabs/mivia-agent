// Package durablefence provides a reusable harness for the durable-ownership
// checks that every claim, lease, or fence surface must pass.
//
// Concurrency through a shared store is not the same problem as concurrency
// through memory: the interleaving happens between transactions, and the
// writers may be separate processes or hosts. The race detector cannot see it.
// Each site in this repository re-derived the same scenarios by hand, which is
// why the class produced a long chain of repeat fixes. This package states the
// scenarios once so a new ownership surface inherits them.
//
// The harness is storage-agnostic. A caller describes its own ownership model
// through Scenario and the checks drive the interleavings.
//
// See `.mivia/quality/defect-taxonomy.md` classes DC-2, DC-3, and DC-4, and
// invariant INV-DUR-2.
package durablefence

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// Scenario adapts one durable-ownership surface to the harness.
//
// Claim, Takeover, Mutate, and Release act on a single logical record that the
// caller creates before it runs a check. Every function must be safe to call
// from more than one goroutine, because the checks drive concurrent claims.
type Scenario struct {
	// Name labels the surface in failure messages, for example "workflow run".
	Name string

	// Claim acquires exclusive ownership for holder. It returns an error that
	// IsHeld recognizes when a different holder already owns the record.
	// A repeat claim by the current holder must succeed as a refresh.
	Claim func(ctx context.Context, holder string) error

	// Takeover atomically transfers ownership to holder, whoever owns it now.
	Takeover func(ctx context.Context, holder string) error

	// Mutate performs one durable state mutation as holder. It returns an
	// error that IsFenced recognizes when holder no longer owns the record.
	Mutate func(ctx context.Context, holder string) error

	// Release releases ownership held by holder.
	Release func(ctx context.Context, holder string) error

	// IsHeld reports whether err means "another holder owns this record".
	IsHeld func(error) bool

	// IsFenced reports whether err means "you are no longer the owner".
	// When it is nil the harness uses IsHeld, which is the common case: the
	// same sentinel covers both a refused claim and a fenced write.
	IsFenced func(error) bool
}

func (s Scenario) fenced(err error) bool {
	if s.IsFenced != nil {
		return s.IsFenced(err)
	}
	return s.IsHeld(err)
}

func (s Scenario) require(t testing.TB) {
	t.Helper()
	missing := make([]string, 0, 5)
	for name, fn := range map[string]bool{
		"Claim":    s.Claim == nil,
		"Takeover": s.Takeover == nil,
		"Mutate":   s.Mutate == nil,
		"Release":  s.Release == nil,
		"IsHeld":   s.IsHeld == nil,
	} {
		if fn {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("durablefence: scenario %q is missing %v", s.Name, missing)
	}
}

// Run executes every check against the scenario. Each check runs in its own
// subtest, so a caller supplies a fresh record per check through New.
func Run(t *testing.T, name string, new func(testing.TB) Scenario) {
	t.Helper()
	checks := map[string]func(testing.TB, Scenario){
		"ClaimIsExclusive":            CheckClaimIsExclusive,
		"TakeoverFencesPreviousOwner": CheckTakeoverFencesPreviousOwner,
		"ConcurrentClaimAdmitsOne":    CheckConcurrentClaimAdmitsOne,
		"ReleaseIsHolderOnly":         CheckReleaseIsHolderOnly,
	}
	for _, check := range []string{
		"ClaimIsExclusive",
		"TakeoverFencesPreviousOwner",
		"ConcurrentClaimAdmitsOne",
		"ReleaseIsHolderOnly",
	} {
		t.Run(name+"/"+check, func(t *testing.T) {
			scenario := new(t)
			scenario.require(t)
			checks[check](t, scenario)
		})
	}
}

// CheckClaimIsExclusive proves a second holder cannot claim a held record, and
// that the record becomes claimable again after the holder releases it.
func CheckClaimIsExclusive(t testing.TB, s Scenario) {
	t.Helper()
	ctx := context.Background()
	if err := s.Claim(ctx, "owner-a"); err != nil {
		t.Fatalf("%s: first claim: %v", s.Name, err)
	}
	if err := s.Claim(ctx, "owner-a"); err != nil {
		t.Fatalf("%s: same-holder refresh must succeed, got %v", s.Name, err)
	}
	err := s.Claim(ctx, "owner-b")
	if err == nil {
		t.Fatalf("%s: second holder claimed a held record", s.Name)
	}
	if !s.IsHeld(err) {
		t.Fatalf("%s: second claim error = %v, want a held error", s.Name, err)
	}
	if err := s.Release(ctx, "owner-a"); err != nil {
		t.Fatalf("%s: release by holder: %v", s.Name, err)
	}
	if err := s.Claim(ctx, "owner-b"); err != nil {
		t.Fatalf("%s: claim after release: %v", s.Name, err)
	}
}

// CheckTakeoverFencesPreviousOwner proves the write of a stale owner fails
// after a takeover, instead of winning it.
//
// This is the load-bearing check. A boolean claim flag passes
// CheckClaimIsExclusive and still fails here: the previous owner keeps a
// reference it acquired before the takeover and its next mutation lands.
func CheckTakeoverFencesPreviousOwner(t testing.TB, s Scenario) {
	t.Helper()
	ctx := context.Background()
	if err := s.Claim(ctx, "owner-a"); err != nil {
		t.Fatalf("%s: claim by owner-a: %v", s.Name, err)
	}
	if err := s.Mutate(ctx, "owner-a"); err != nil {
		t.Fatalf("%s: owner-a must write while it owns the record, got %v", s.Name, err)
	}
	if err := s.Takeover(ctx, "owner-b"); err != nil {
		t.Fatalf("%s: takeover by owner-b: %v", s.Name, err)
	}
	err := s.Mutate(ctx, "owner-a")
	if err == nil {
		t.Fatalf("%s: stale owner-a write landed after takeover", s.Name)
	}
	if !s.fenced(err) {
		t.Fatalf("%s: stale write error = %v, want a fenced error", s.Name, err)
	}
	if err := s.Mutate(ctx, "owner-b"); err != nil {
		t.Fatalf("%s: owner-b must write after takeover, got %v", s.Name, err)
	}
}

// ConcurrentHolders is the number of claimers CheckConcurrentClaimAdmitsOne
// starts. A scenario that widens the window with its own barrier must release
// exactly this many.
const ConcurrentHolders = 8

// CheckConcurrentClaimAdmitsOne starts ConcurrentHolders claimers at once and
// requires that exactly one wins.
//
// Read the result honestly. A pass is evidence, not proof: when the claim is
// check-then-act, the window between the check and the act is nanoseconds and
// one claimer usually wins anyway. Repeating the check does not fix that. To
// prove atomicity, the code under test needs a hook that holds every claimer
// after its check until all of them have checked; the scenario then widens the
// window deterministically. Without such a hook, record the residual risk
// rather than reporting the class as proven.
func CheckConcurrentClaimAdmitsOne(t testing.TB, s Scenario) {
	t.Helper()
	const holders = ConcurrentHolders
	ctx := context.Background()
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var won []string
	var unexpected []error
	for i := range holders {
		holder := string(rune('a'+i)) + "-holder"
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := s.Claim(ctx, holder)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				won = append(won, holder)
			case s.IsHeld(err):
			default:
				unexpected = append(unexpected, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	for _, err := range unexpected {
		t.Errorf("%s: concurrent claim returned an unrecognized error: %v", s.Name, err)
	}
	if len(won) != 1 {
		t.Fatalf("%s: %d holders won the claim (%v), want exactly 1", s.Name, len(won), won)
	}
}

// CheckReleaseIsHolderOnly proves a non-holder cannot release the claim.
// A release that any caller may perform is a claim any caller may steal.
func CheckReleaseIsHolderOnly(t testing.TB, s Scenario) {
	t.Helper()
	ctx := context.Background()
	if err := s.Claim(ctx, "owner-a"); err != nil {
		t.Fatalf("%s: claim by owner-a: %v", s.Name, err)
	}
	if err := s.Release(ctx, "owner-b"); err == nil {
		t.Fatalf("%s: owner-b released a claim it does not hold", s.Name)
	}
	if err := s.Mutate(ctx, "owner-a"); err != nil {
		t.Fatalf("%s: owner-a still owns the record, got %v", s.Name, err)
	}
	if err := s.Release(ctx, "owner-a"); err != nil {
		t.Fatalf("%s: release by holder: %v", s.Name, err)
	}
}

// ErrIs returns an IsHeld or IsFenced function for a sentinel error.
func ErrIs(sentinel error) func(error) bool {
	return func(err error) bool { return errors.Is(err, sentinel) }
}
