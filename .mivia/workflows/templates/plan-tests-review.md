# Review Test Plan

Independently review the test plan for `{{ inputs.task }}`.

Approved delivery plan:

{{ evidence.plan }}

Test plan:

{{ evidence.test_plan }}

Read the relevant source and tests. Do not edit files. Do not run commands, commit, push,
publish, or read secret-like files.

Check that the tests cover accepted behavior and reachable negative paths. Check empty,
malformed, oversized, and duplicate structured input when it applies. Check security and hook
regressions, the fuzz decision, and requested host evidence gates. Request changes for each
missing requirement or unsupported claim.

Return only the declared structured output. Use `approved` only when no finding remains.
Otherwise use `changes_requested` and list each finding with severity and a concrete reason.
