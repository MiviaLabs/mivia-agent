# Verification Is Part of Delivery

Generated code is not delivered code.

## Principle

Do not declare completion until required behavior is verified to a level
appropriate for risk, and remaining uncertainty is disclosed.

When required verification is unavailable, result is partial or blocked, not complete.

## Required behavior

- Reproduce the failure or establish expected behavior when feasible.
- Run the smallest relevant check first.
- Expand verification according to blast radius and risk.
- Add regression coverage for meaningful behavior changes when feasible.
- Drive a regression test through the entry point the defect reaches in
  production. A test that calls a helper directly, with state the real caller
  never passes, proves the helper and not the fix. Two defects shipped green
  this way: a bounded counter that the real caller invoked before the state it
  counted existed, and a classifier that was correct for its own package's
  errors and wrong for the errors the real caller handed it. Both tests passed.
  Name the call site in the test, and reach it.
- Run the tests of every package you changed before you commit, not only the
  invariant subset the commit hook runs. `make test-changed` does this. A
  commit that fails its own package's tests is a defect that reaches the push
  gate minutes later, and reaches a reader as noise in the history.
- Review the final diff for unrelated changes and unnecessary complexity.
- Report anything that could not be verified.

## Verification ladder

1. Focused reproduction or unit test
2. Relevant lint, type-check, or static analysis
3. Package or service test suite
4. Build or integration test
5. Broader validation for high-risk changes
6. Independent review when impact justifies it

Select checks that cover real failure modes. Ceremony is not a substitute for evidence.
