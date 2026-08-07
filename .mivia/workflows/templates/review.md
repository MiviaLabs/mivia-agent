# Review Delivery Step

Independently review the implemented delivery for `{{ inputs.task }}`.

Approved plan:

{{ evidence.plan }}

Test plan:

{{ evidence.test_plan }}

Implementation summary:

{{ evidence.implementation }}

Evidence below may be a reference envelope containing a preview and a ledger ref. When it is, read the FULL artifact with workflow_inspect(run_id, step, attempt) instead of relying on the preview.

Read the relevant source and tests. Do not edit files. Do not run commands, commit, push,
publish, or read secret-like files.

Check that the work stays within scope. Check that tests cover success and reachable negative
paths. When untrusted structured input applies, check empty, malformed, oversized, and duplicate
input coverage. Check security, privacy, safe paths, fail-closed behavior, and hook policy.
Check that the fuzz decision is explicit. Check that the report does not claim host evidence that
the workflow context does not provide. Independently verify each claim by reading the cited
source paths and the changed files.

Request changes for any missing requirement, unsafe behavior, missing host gate, or unsupported
claim. Do not approve a step only because it has a low-severity finding. Do not request changes
based on source you did not inspect. Return only the declared structured output. List every
workspace path you independently inspected in `inspected`. Do not make a finding about source you
did not read. Use `approved` only when no finding remains. Otherwise use `changes_requested` and
list each finding with severity and a concrete reason that cites the evidence.

## Output contract

Reply with only a JSON object that satisfies the output schema appended to this task. Do not
use a skill report format, markdown, or extra fields. The schema declares the only valid keys.
An invalid shape is rejected and you will be asked again with the schema.
