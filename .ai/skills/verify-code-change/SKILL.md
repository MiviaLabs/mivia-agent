---
name: verify-code-change
description: Verify an implemented code or configuration change with evidence appropriate to risk and blast radius. Use after an executable artifact changes, before claiming completion, merging, or review. Do not use for pure research or writing.
---

# Verify Code Change

## Purpose

Collect enough evidence to determine whether the requested behavior appears satisfied within the executed scope, identify material regressions visible in that scope, and expose what remains uncertain.

## Inputs

- requested behavior or definition of done;
- changed files or current diff;
- available test, lint, type-check, build, and runtime commands;
- known risks, constraints, and skipped checks.

## Procedure

1. Inspect the diff and identify the behavior that changed.
2. Separate the intended behavior from the current implementation evidence.
3. Map each material requirement to a verification target.
4. Classify the blast radius:
   - local: isolated implementation with no public contract change;
   - moderate: shared package, API behavior, persistence, or cross-module effect;
   - high: security, privacy, migrations, concurrency, infrastructure, destructive behavior, or broad compatibility impact.
5. Run the smallest relevant check first.
6. If the focused check passes, expand verification according to the blast radius.
7. Review the diff for unrelated changes, debug artifacts, missing error handling, unsafe assumptions, and unnecessary complexity.
8. For meaningful bug fixes, confirm regression coverage when feasible.
9. Never claim a check passed unless it was executed successfully.
10. Report exactly what was verified, what failed, what was skipped, and what risk remains.

## Verification ladder

Use only the levels justified by the change:

1. focused reproduction or unit test;
2. relevant lint, type-check, or static analysis;
3. package or service test suite;
4. build or integration test;
5. broader validation for high-risk changes;
6. independent review when available and justified.

The ladder is not automatically linear. Choose checks that cover the actual failure modes of the change.

## Result semantics

- `PASS`: the checks required at the change's blast radius were executed and passed, and no material issue was found within that scope. Do not downgrade a verified change to `PARTIAL` by citing higher-tier checks that were not required for its scope.
- `PARTIAL`: a check that is required to verify the change at its blast radius is unavailable, incomplete, or blocked, or the diff review surfaced a material concern the executed checks did not resolve.
- `FAIL`: a required check failed, the implementation does not satisfy the requirement, or a material regression was found.

`PASS` does not prove that no defect exists; it describes the executed scope only. When the diff review finds a material defect in the change itself (for example a silently swallowed error or an untested broadened behavior), the result is `PARTIAL` or `FAIL`, not a clean `PASS`.

## Failure handling

- If a check fails, report the command or method, decisive failure, and practical consequence.
- If the failure is caused by the change, return the task to implementation.
- If the failure is unrelated or environmental, provide the evidence and continue with all remaining safe checks.
- If required verification cannot run, do not declare the task complete.
- For high-risk work, identify the exact approval or validation still required before release.

## Report shape

### Verification result

`PASS`, `PARTIAL`, or `FAIL`

### Scope

- behavior and files covered;
- blast-radius classification.

### Checks executed

- command or method;
- summarized result;
- requirement or risk covered;
- list a check as not run only if it was required at the change's blast radius and could not be executed; do not record higher-tier checks that were not required for the change as not run, since that invents a gap and muddies the result.

### Diff review

- material findings or `No material issues found within the reviewed diff`.

### Remaining risk

- skipped checks, unresolved uncertainty, or `None identified within the executed scope`.

Keep the report concise. Do not paste complete successful logs.
