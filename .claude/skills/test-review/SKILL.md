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

Find the boundary in the code under test. For the agent loop, the
full path is dispatch, execute, observe, summarize, checkpoint. For a
workflow, the full path is parse, compile, schedule, execute, synthesize.

A test that stays inside one function and never crosses a boundary is a
unit test, not an integration test. Flag the package if it has no
integration test that crosses a boundary.

#### Cross-package integration tests

A cross-package integration test proves two packages compose through
their public API. It exercises the real architecture boundaries declared in
`.mivia/policy/go-structure.json` and `docs/design/ui-isolation.md`.

Check for cross-package coverage explicitly. In this repository, key boundaries include:

- `cmd/mivia` connects CLI entrypoints to `internal/agent`, `internal/cli*`, and `internal/workflows`.
- `cmd/mivia-ui` connects to `internal/uiadapter` and `internal/uikit` without importing CLI or coordinator packages.
- `internal/workflows` connects `controller`, `compiler`, `ledger`, and `storage`.
- `internal/hooks` executes lifecycle gates independently and never imports `internal/runtime` or `internal/tools`.
- `internal/ui` and `internal/uikit` connect through `internal/uikit/ports` and `internal/uikit/uievent`.

If a critical package boundary has no cross-package test, that is a gap. Flag it.

### 2. Mock and fake audit

Look at every test helper and fake. Ask two questions:

1. Does the fake return what the real dependency returns, or what the
   author wished it returned?
2. Does the fake hide a failure mode the real dependency exhibits?

A fake that returns `nil, nil` for an error that the real system returns
is a lie. A fake that always succeeds hides timeouts, network drops, and
malformed payloads. Replace it with a test double that exercises the real
failure paths.

### 3. Assertion quality

Look at every assertion in the test:

1. Does the assertion check the payload, or only the error code?
2. Is the assertion vacuous (for example, comparing an object to itself or asserting true is true)?
3. Does the test check that expected state was mutated, or only that the method returned?

A test with zero assertions or tautological checks fails the AST test quality gate (`scripts/check_test_quality.py`).

### 4. Edge cases and boundary values

Check for boundary cases:

- Empty inputs: an empty slice, an empty string, an empty map.
- Invalid inputs: a missing reference, a duplicate ID, an unknown name.
- Boundaries: a zero-length result, a single element, a self reference.
- Structural edges: a self loop, a two-element cycle, a lone root.

Prefer table-driven tests when the case set grows. Name each case with
the behavior it pins, not the code path.

### 5. Fuzz tests

A fuzz test feeds random input to a decoder or a validator. It proves
the code does not crash or violate an invariant on unknown input.

Write a Go `Fuzz` target for any function that accepts bytes, a string,
or a wire form. Seed it with the valid cases. Add an invariant check inside the target, not just a no-crash check.

### 6. Perf tests and mutation testing

A perf test documents the measured baseline in a comment and asserts the
allocation budget with `testing.AllocsPerRun`.

Run mutation tests via `scripts/check_mutation.py --pkg <pkg>` or `scripts/check_mutation.py --diff` to prove assertions catch deliberate code mutations.

## Repo-specific requirements

The rules below hold in this repository:

- **Per-line diff coverage**: Every added or modified non-test Go statement line must be executed by a test (`scripts/diff_coverage.py`).
- **Invariant verification**: Every invariant listed in `.mivia/invariants.md` must have a corresponding, non-skipping test verified via `make invariants` and `scripts/validate_invariants.py`.
- **AST test quality**: Empty test bodies, zero-assertion tests, tautological assertions, and unreviewed `t.Skip` additions are blocked by `scripts/check_test_quality.py`.
- **Structure and file limits**: Packages follow `.mivia/policy/go-structure.json` and pass `scripts/check_go_structure.py`.
- **Run the offline gates**: `make verify` must pass cleanly before any code is committed.

## Output format

Report three sections:

1. **A per-test verdict list**: For each test: `tests-what-it-claims`, `weak`, `vacuous`, or `untested` with `file:line`.
2. **A prioritized gap list**: Each gap states a reproduction, a severity, and a concrete test that would catch it.
3. **A coverage summary**: State the line coverage and any reachable uncovered lines.
