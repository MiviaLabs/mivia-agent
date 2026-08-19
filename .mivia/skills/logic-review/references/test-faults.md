# Test fault catalog

Fault classes for the test-logic pass. A test can pass on broken
code. Each class below names the fault, the signal, and the fix.

## Tautology faults

The test derives the expected value from the code under test. It can
never fail.

- Expected recomputed: `want := f(in)` and then `got := f(in)`.
  Derive the want by hand or pin it as a constant.
- Round trip without a pin: decode what you encoded proves only
  self-consistency. Pin the wire bytes, as the conformance vectors
  in `envelope/testdata/vectors/` do.
- Assert the helper, not the unit: the expected value comes from a
  helper that shares the logic under test. The shared bug cancels
  out.

## Wrong-target faults

The assertion reads something other than the behavior under test.

- Wrong field: the code changed field X and the test asserts field
  Y. Both stay green.
- Copy not original: the assertion reads a copy made before the
  mutation. Mutate, then read the original.
- Cached snapshot: the assertion reads a precomputed value. An input
  mutation then passes unnoticed. Re-derive or read the live state.
- Length only: len equality without content equality. Any
  permutation passes.
- Non-nil only: construction proved, behavior unproved.

## Swallowed-failure faults

- Logged, not failed: `t.Log(err)` continues the test. Fail the
  test on the error.
- Error branch skipped: the table case wants an error, the runner
  never checks it. Each case states its expected outcome, error or
  value.
- Soft assert on the happy path only: error paths get no assertion.
  Every path asserts its outcome.

## Test control-flow faults

- Skipped table case: an early continue or a wrong loop bound drops
  a case. The case never runs and never fails. Count the subtests
  the run reports.
- Loop-variable capture: in parallel subtests on Go before 1.22, the
  closure captures the loop variable. Every case asserts the last
  entry. Re-bind the variable inside the loop.
- Missing subtest wrap: a loop that asserts without t.Run stops at
  the first failure only when it calls t.Fatalf. With t.Errorf it
  continues, which is fine. Pick the one that fits, and say why.
- Shared fixture without reset: state leaks from one case into the
  next. Reset per case or build per case.

## Assertion-weakness faults

- Shared error substring: two error paths emit the same substring.
  The assertion passes for the wrong reason. Pin a substring unique
  to one path.
- Partial compare: only part of the struct checked. Fields left
  unchecked drift. Compare the whole observable result.
- DeepEqual on time-bearing structs: wall-clock fields differ. Trim
  them or compare fields.
- Expected overspecified: the test pins internal steps, not the
  observable outcome. A valid refactor breaks it. Assert the
  contract.

## Concurrency faults in tests

- testing.T off the test goroutine: t.Errorf from another goroutine
  is undefined. Channel the result back, assert on the test
  goroutine.
- Missing synchronization: the test reads shared state the code
  writes. Add a channel or a WaitGroup, or run with the race
  detector and watch it fire.
- Sleep as synchronization: a fixed sleep replaces a signal. It
  stays flaky and stays slow. Synchronize on the condition.

## The mutation check

For each path, mutate the code in your head: flip the branch, drop
the guard, break the return. Name the test that fails. A path with
no failing test is a gap.

Confirm a suspected gap with a scratch test under `/tmp`. Never edit
repo code to prove it. Some mutations do not change behavior: an
equivalent mutant is not a gap. Skip those.

## Sources

- Mutation testing practice: a good test fails when behavior
  changes; equivalent mutants are a known false positive.
  See pitest.org and the CircleCI mutation testing guide.
- Go wiki, TableDrivenTests: t.Run subtests, got and want in
  failure messages, t.Errorf versus t.Fatalf, parallel subtests and
  the loop-variable note.
- Dave Cheney, prefer table-driven tests: one careful runner
  amortized over all cases.
- Michaela Greiler checklist: ask which extra inputs and edge cases
  need a test.
