# Review Test Plan

Independently review the test plan for `{{ inputs.task }}`.

Approved delivery plan:

{{ evidence.plan }}

Test plan:

{{ evidence.test_plan }}

Evidence below may be a reference envelope containing a preview and a ledger ref. When it is, read the FULL artifact with workflow_inspect(run_id, step, attempt) instead of relying on the preview.

Read the relevant source and tests. Do not edit files. Do not run commands, commit, push,
publish, or read secret-like files.

Check that the tests cover accepted behavior and reachable negative paths. Check empty,
malformed, oversized, and duplicate structured input when it applies. Check security and hook
regressions, the fuzz decision, and requested host evidence gates. Independently verify each
claim by reading the cited source paths. Request changes for each missing requirement or
unsupported claim. Do not request changes based on source you did not inspect.

Return only the declared structured output. List every workspace path you independently
inspected in `inspected`. Do not make a finding about source you did not read. Use `approved`
only when no finding remains. Otherwise use `changes_requested` and list each finding with
severity and a concrete reason that cites the evidence.

## Output contract

Reply with only a JSON object that satisfies the output schema appended to this task. Do not
use a skill report format, markdown, or extra fields. The schema declares the only valid keys.
An invalid shape is rejected and you will be asked again with the schema.
