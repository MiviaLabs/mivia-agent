package durablefence

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// A harness whose failure branches never run is a harness that can report a
// false pass. Every branch below is driven by a scenario broken in exactly one
// way, and each case asserts both that the check fails and which failure it
// reported. Compare with durablefence_test.go, which proves the checks accept a
// correct surface.

var errOther = errors.New("some unrelated failure")

// capture records the first failure message a check produced.
type capture struct {
	testing.TB
	failed bool
	msg    string
}

func (c *capture) Helper() {}

func (c *capture) Errorf(format string, args ...any) {
	c.record(format, args...)
}

func (c *capture) Fatalf(format string, args ...any) {
	c.record(format, args...)
	panic(sentinelStop{})
}

func (c *capture) record(format string, args ...any) {
	if !c.failed {
		c.failed = true
		c.msg = fmt.Sprintf(format, args...)
	}
}

func captureCheck(t *testing.T, check func(testing.TB, Scenario), s Scenario) *capture {
	t.Helper()
	got := &capture{TB: t}
	func() {
		defer func() {
			r := recover()
			if r == nil {
				return
			}
			if _, ok := r.(sentinelStop); !ok {
				panic(r)
			}
		}()
		check(got, s)
	}()
	return got
}

type brokenCase struct {
	name  string
	check func(testing.TB, Scenario)
	// broken returns a scenario with exactly one defect.
	broken func(t *testing.T) Scenario
	// want is a substring of the failure the check must report.
	want string
}

func alwaysFail(context.Context, string) error { return errOther }

func TestChecksRejectEveryDefect(t *testing.T) {
	for _, tc := range brokenCases() {
		t.Run(tc.name, func(t *testing.T) {
			got := captureCheck(t, tc.check, tc.broken(t))
			if !got.failed {
				t.Fatalf("check passed a defective surface (%s)", tc.name)
			}
			if !strings.Contains(got.msg, tc.want) {
				t.Fatalf("failure = %q, want it to mention %q", got.msg, tc.want)
			}
		})
	}
}

func brokenCases() []brokenCase {
	cases := exclusiveCases()
	cases = append(cases, takeoverCases()...)
	cases = append(cases, concurrentCases()...)
	return append(cases, releaseCases()...)
}

func base(t *testing.T) (*fakeOwner, Scenario) {
	t.Helper()
	owner := newFakeOwner()
	return owner, owner.scenario()
}

func exclusiveCases() []brokenCase {
	check := CheckClaimIsExclusive
	return []brokenCase{
		{"exclusive/first claim fails", check, func(t *testing.T) Scenario {
			_, s := base(t)
			s.Claim = alwaysFail
			return s
		}, "first claim"},
		{"exclusive/refresh by the same holder is refused", check, func(t *testing.T) Scenario {
			owner, s := base(t)
			calls := 0
			s.Claim = func(ctx context.Context, holder string) error {
				calls++
				if calls == 2 {
					return errHeld
				}
				return owner.claim(ctx, holder)
			}
			return s
		}, "same-holder refresh"},
		{"exclusive/a second holder may claim", check, func(t *testing.T) Scenario {
			owner, s := base(t)
			s.Claim = func(ctx context.Context, holder string) error {
				owner.mu.Lock()
				owner.owner = holder
				owner.fence++
				owner.seen[holder] = owner.fence
				owner.mu.Unlock()
				return nil
			}
			return s
		}, "second holder claimed"},
		{"exclusive/refusal uses an unrecognized error", check, func(t *testing.T) Scenario {
			owner, s := base(t)
			s.Claim = func(ctx context.Context, holder string) error {
				if err := owner.claim(ctx, holder); err != nil {
					return errOther
				}
				return nil
			}
			return s
		}, "want a held error"},
		{"exclusive/the holder cannot release", check, func(t *testing.T) Scenario {
			_, s := base(t)
			s.Release = alwaysFail
			return s
		}, "release by holder"},
		{"exclusive/a released record stays unclaimable", check, func(t *testing.T) Scenario {
			owner, s := base(t)
			s.Release = func(ctx context.Context, holder string) error {
				// Release succeeds but leaves the owner in place.
				_ = owner
				return nil
			}
			return s
		}, "claim after release"},
	}
}

func takeoverCases() []brokenCase {
	check := CheckTakeoverFencesPreviousOwner
	return []brokenCase{
		{"takeover/the first claim fails", check, func(t *testing.T) Scenario {
			_, s := base(t)
			s.Claim = alwaysFail
			return s
		}, "claim by owner-a"},
		{"takeover/the owner cannot write while it owns the record", check, func(t *testing.T) Scenario {
			_, s := base(t)
			s.Mutate = alwaysFail
			return s
		}, "must write while it owns"},
		{"takeover/the takeover fails", check, func(t *testing.T) Scenario {
			_, s := base(t)
			s.Takeover = alwaysFail
			return s
		}, "takeover by owner-b"},
		{"takeover/the stale write lands", check, func(t *testing.T) Scenario {
			owner, _ := base(t)
			owner.unfenced = true
			return owner.scenario()
		}, "stale owner-a write landed"},
		{"takeover/the stale write fails for the wrong reason", check, func(t *testing.T) Scenario {
			owner, s := base(t)
			s.Mutate = func(ctx context.Context, holder string) error {
				if err := owner.mutate(ctx, holder); err != nil {
					return errOther
				}
				return nil
			}
			return s
		}, "want a fenced error"},
		{"takeover/the new owner cannot write", check, func(t *testing.T) Scenario {
			owner, s := base(t)
			s.Mutate = func(ctx context.Context, holder string) error {
				if holder == "owner-b" {
					return errOther
				}
				return owner.mutate(ctx, holder)
			}
			return s
		}, "must write after takeover"},
	}
}

func concurrentCases() []brokenCase {
	check := CheckConcurrentClaimAdmitsOne
	return []brokenCase{
		{"concurrent/an unrecognized error surfaces", check, func(t *testing.T) Scenario {
			_, s := base(t)
			s.Claim = alwaysFail
			return s
		}, "unrecognized error"},
		{"concurrent/every claimer wins", check, func(t *testing.T) Scenario {
			owner, s := base(t)
			owner.checkThenAct = true
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
			return s
		}, "won the claim"},
	}
}

func releaseCases() []brokenCase {
	check := CheckReleaseIsHolderOnly
	return []brokenCase{
		{"release/the claim fails", check, func(t *testing.T) Scenario {
			_, s := base(t)
			s.Claim = alwaysFail
			return s
		}, "claim by owner-a"},
		{"release/any caller may release", check, func(t *testing.T) Scenario {
			owner, s := base(t)
			s.Release = func(ctx context.Context, _ string) error {
				owner.mu.Lock()
				defer owner.mu.Unlock()
				owner.owner = ""
				return nil
			}
			return s
		}, "released a claim it does not hold"},
		{"release/the owner loses the record to a foreign release", check, func(t *testing.T) Scenario {
			owner, s := base(t)
			s.Release = func(ctx context.Context, holder string) error {
				if holder == "owner-b" {
					// Reports a refusal, but drops the claim anyway.
					owner.mu.Lock()
					owner.owner = ""
					owner.mu.Unlock()
					return errHeld
				}
				return owner.release(ctx, holder)
			}
			return s
		}, "still owns the record"},
		{"release/the holder cannot release at the end", check, func(t *testing.T) Scenario {
			owner, s := base(t)
			s.Release = func(ctx context.Context, holder string) error {
				if holder == "owner-a" {
					return errOther
				}
				return owner.release(ctx, holder)
			}
			return s
		}, "release by holder"},
	}
}
