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
