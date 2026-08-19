---
name: test-review
description: Audit the tests of a Go package for truth and coverage quality. Trigger for test review, coverage checks, mocks, fakes, edge cases, vacuous assertions, or cross-package integration test gaps.
tools:
  - read_file
  - list_dir
  - grep
  - glob
  - find_references
---

# Test review

Every package ships tests. Passing tests are not proof. A test that
passes on a broken implementation is worse than no test. It hides the
bug and games the coverage floor. Your job is to find tests that do not
test what they claim, and to fix them.

## Trigger

Read tests before code. A code review and a test review are different
passes. This skill audits the test files of one package.

## The adversarial stance

For every test function, answer one question: can this test pass while
the code under test is broken? If yes, the test is wrong. Build the
"wrong but passing" implementation in your head for each test. The test
that cannot catch it is the one to rewrite.

Confirmed findings only. Each needs a reproduction, a severity, a
file:line, and a minimal fix. Report first. Fix the tests after you
report, unless the user asked only for a review.

## Six coverage categories

Check each category. A package is not done until every category has a
real case and no category relies on a fake.

### 1. Integration tests

An integration test exercises the real path across a boundary. It
calls other packages. It uses the real wire form or the real transport.
It never substitutes a stand-in for the trust boundary.

Find the boundary in the code under test. For the message plane, the
full ladder is sign, encode, transport, decode, verify, admit, ack,
thread. For a state machine, the full path is init, validate, fire,
encode, decode. For a graph, build a real graph and read the real
result.

A test that stays inside one function and never crosses a boundary is a
unit test, not an integration test. Flag the package if it has no
integration test that crosses a boundary.

#### Cross-package integration tests

A cross-package integration test proves two packages compose through
their public API. It is the highest-value integration test, because it
exercises the real edges the architecture declares in
`policy/layers.json`. A single-package integration test cannot prove an
edge between packages.

Check for cross-package coverage explicitly. For each allowed import
edge in `policy/layers.json`, ask: is there a test that sends a real
value from the importer across the edge to the imported package and
reads the real result? In this repo the edges are:

- `a2a` imports `envelope`: `ToPart` maps an `envelope.Message` into an
  `a2a.Mapped` value.
- `a2aclient` imports `a2a` and `envelope`: it sends an `a2a.Mapped`
  task and re-verifies the returned `envelope.Message` signature.
- `agent` imports `identity`, `discovery`, `flow`, `envelope`, `events`,
  `machine`, `heartbeat`, and `contextbudget`: `Run` builds signed
  `envelope.Message`s through a `flow.Definition` over a
  `machine.Definition`, emits `events.Event`s, and optionally beats a
  `heartbeat.Monitor`.
- `flow` imports `events` and `machine`: `Run` emits
  `StepCompletedEvent` and calls `machine.Definition.Fire`.
- `heartbeat` imports `events`: `Monitor.Beat` emits a `BeatEvent`.
- `identity` imports `envelope`: `Sign` wraps `envelope.Sign`.
- `ledger` imports `machine` and `events`: `Claim` persists
  `machine.Status` transitions and emits ledger events.
- `machine` imports `events`: `MoveEvent` is an `events.Name` a caller
  emits after a successful `Fire`.
- `memory` imports `envelope`: `Store.Put` and `Store.Get` use
  `envelope.ContextRef` keys.
- `mcp` imports `tools`: `ListTools` maps remote tools into
  `tools.Tool` values and `RegisterAll` adds them to a
  `tools.Registry`.
- `room` imports `envelope` and `heartbeat`: `Accepts` verifies an
  `envelope.Message` signature; `StaleMembers` reads a
  `heartbeat.Monitor`.
- `scheduler` imports `events`: `Scheduler.Run` emits `JobFailedEvent`
  on the supplied bus.
- The remaining packages (`channel`, `contextbudget`, `discovery`,
  `durablefence`, `envelope`, `events`, `provider`, `tools`, `trigger`)
  declare no internal import edges.

If an allowed edge has no cross-package test, that is a gap. Flag it.
A fake or a re-bound registry at the edge is not a cross-package test;
the real types must flow through the boundary.

To find the gaps, list every edge in `policy/layers.json`, then grep
the test packages for the two package names imported together. An edge
with no test importing both members is uncovered.

### 2. No fake-interface coverage

A fake is any stand-in for a real dependency. A test that passes a fake
interface, a stubbed function, or a re-bound registry proves the fake,
not the integration. The fake is the wrong thing to test.

Watch for these variants, all of which fake the path:

- A mock interface passed where production code uses the real type.
- A re-bound registry or handler used in place of the real one.
- A test that mutates a slice header instead of a slice element.
  Header reassignment does not exercise backing-array sharing. To test
  a deep copy you must mutate an element and re-read.
- A test that observes a precomputed snapshot. If the assertion reads a
  cached value, a mutation of the input can pass unnoticed. Read the
  real state, not the derived state.
- A test that asserts the returned object is non-nil and nothing else.
  Non-nil proves construction, not behavior.

The most dangerous fake is a test that only reads a derived value. For
example, a test that mutates an input and then checks a cached root
list can pass even when the input leak is real. To catch it, re-derive
or read the internal copy directly.

### 3. All gaps

Line coverage is the floor, not the goal. 85 is green; it is not done.
Every branch and every error path needs a test that asserts the right
outcome.

Find the gaps. Run a coverage report. For each uncovered line, decide
whether it is reachable. A reachable but untested line is a gap. A dead
branch you did not intend is either dead code or an untested decision;
test it or delete it.

Assertion-free tests game the floor. Count how many assertions each
test makes. A test that runs code but asserts nothing proves nothing.

### 4. Edge cases

Edge cases are the boundaries of the input space. For each type in the
surface, cover at least one valid and one invalid case per dimension.

- Empty inputs: an empty list, an empty string, an empty map.
- Invalid inputs: a missing reference, a duplicate ID, an unknown name.
- Boundaries: a zero-length result, a single element, a self reference.
- Structural edges: a self loop, a two-element cycle, a lone root.

Prefer table-driven tests when the case set grows. Name each case with
the behavior it pins, not the code path. A table makes a new edge case a
one-line addition.

For a rule that unions or branches, pin each side with its own missing
case. A fixture that lacks only one side lets a reduced rule pass.

### 5. Fuzz tests

A fuzz test feeds random input to a decoder or a validator. It proves
the code does not crash or violate an invariant on unknown input, not
that it handles a fixed set.

Write a Go `Fuzz` target for any function that accepts bytes, a string,
or a wire form. Seed it with the valid cases. Run a short pass with a
time limit. Add an invariant check inside the target, not just a
no-crash check.

Fuzz is not a replacement for edge cases. It samples the space; it does
not pin the boundaries. Use both.

### 6. Perf tests

A perf test states the time target and the allocation budget before it
runs. It documents the measured baseline in a comment. It asserts the
budget, not just the time.

Use a benchmark for the time target. Assert the allocation count with
`testing.AllocsPerRun` and a budget.

The budget is calibrated to the measured baseline, not a round number.
If the baseline is 213 allocs, a budget of 300 is too loose; a constant
allocation regression of a few allocs slips past. Set the budget with a
small margin above the measured value and state why the margin is enough
to catch a real regression.

## Repo-specific warnings

The rules below hold in this repo. Check them.

- The coverage floor: total and every package reach 85. Verify the
  floor with `make verify`. A test that deletes assertions to keep the
  floor is a review finding.
- The Makefile `bench` target runs benchmarks. Use it for the perf
  baseline.
- Conformance vectors live in `testdata/vectors/` where the package has
  them. A vector is the pinned wire form. A round-trip test that
  re-encodes and compares is weaker than a vector that matches the wire
  contract.
- Internal state needs an internal test package. An external test
  package cannot read unexported fields. If the claimed invariant is
  only visible through unexported state, add a `package foo` test file
  next to the code, not in the external test package. This repo keeps
  most tests in `foo/foo_test/`; an internal test is the exception for
  invariants that the external package cannot observe.
- The error-substring assertion must pin the failure. If two error
  paths share a substring, the assertion can pass for the wrong reason.
  Choose a substring unique to one path.
- Assert the returned object is non-nil on success, not only that the
  error is nil.
- Tests may construct invalid values on purpose. That is allowed. Tests
  may not fake the boundary.
- Run the gates after any test change: `make verify`. The semgrep, prose
  and label rules scan test files too.

## Output format

Report three sections.

1. A per-test verdict list. For each test: tests-what-it-claims, weak,
   vacuous, or untested. One line each with a file:line.
2. A prioritized gap list. Each gap states a reproduction, a severity,
   and a concrete test that would catch it.
3. A coverage summary. State the line coverage, the floor, and every
   reachable uncovered line.

After the report, fix the confirmed gaps. Then run `make verify` and
`go test -race ./...` and report the result.
