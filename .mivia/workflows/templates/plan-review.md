# Review Delivery Plan

Independently review this delivery plan for `{{ inputs.task }}`:

{{ evidence.plan }}

Read the relevant source and tests. Do not edit files. Do not run commands, commit, push,
publish, or read secret-like files.

Check the scope, acceptance criteria, affected interfaces, compatibility risks, security and
hook boundaries, negative paths, structured-input cases, fuzz decision, and requested host
evidence gates. Request changes for each missing requirement or unsupported claim.

Return only the declared structured output. Use `approved` only when no finding remains.
Otherwise use `changes_requested` and list each finding with severity and a concrete reason.
