# Review Test Plan

Independently review the test plan for `{{ inputs.task }}`.

Approved delivery plan:

{{ evidence.plan }}

Test plan:

{{ evidence.test_plan }}

Every prior-step output is stored in the workflow ledger.
Its ref, step, and attempt are listed in the 'Evidence refs' section of the prompt.
The evidence value is the full artifact when it fits the binding cap.
Otherwise, it is a reference envelope with a preview.
When the preview is truncated or more context is needed, read the full artifact with workflow_inspect(run_id, step, attempt).
For very large artifacts, use the offset and limit parameters to page through the output.

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
