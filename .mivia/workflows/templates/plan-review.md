# Review Delivery Plan

Independently review this delivery plan for `{{ inputs.task }}`:

{{ evidence.plan }}

Read the relevant source and tests. Do not edit files. Do not run commands, commit, push,
publish, or read secret-like files.

Check the scope, acceptance criteria, affected interfaces, compatibility risks, security and
hook boundaries, negative paths, structured-input cases, fuzz decision, and requested host
evidence gates. Independently verify each claim the plan makes by reading the cited source
paths. Request changes for each missing requirement or unsupported claim.

Return only the declared structured output. List every workspace path you independently
inspected in `inspected`. Do not make a finding about source you did not read. Use `approved`
only when no finding remains. Otherwise use `changes_requested` and list each finding with
severity and a concrete reason that cites the evidence.

## Output contract

Reply with only a JSON object that satisfies the output schema appended to this task. Do not
use a skill report format, markdown, or extra fields. The schema declares the only valid keys.
An invalid shape is rejected and you will be asked again with the schema.
