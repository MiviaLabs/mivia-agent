# Implement Feature

Implement the bounded feature `{{ inputs.task }}`.

Use this approved delivery plan:

{{ evidence.plan }}

Use this approved test plan when it is available:

{{ evidence.test_plan }}

Prior review findings (present on repair iterations only):

{{ evidence.review_findings }}

When review findings are present, address each finding before you resubmit. Do not repeat a
change the reviewer rejected.

Integration review findings (present on integration-repair iterations only):

{{ evidence.integration_findings }}

When integration review findings are present, address each finding before you resubmit. Do not
repeat a change the integration reviewer rejected.

Evidence below may be a reference envelope containing a preview and a ledger ref. When it is, read the FULL artifact with workflow_inspect(run_id, step, attempt) instead of relying on the preview.

Read the relevant source and tests. Edit only files required by the approved scope. Write or
update tests before or with the implementation. Cover success behavior and negative paths. For
parsed or decoded untrusted structured input, cover empty, malformed, oversized, and duplicate
input when applicable.

Check the delivered change for secrets, unsafe path handling, unsafe external input, privilege
expansion, fail-open guards, and hook-policy bypasses. State a fuzz decision. Request a bounded
host fuzz gate when it is practical.

Do not run commands, commit, push, publish, bypass hooks, or read secret-like files. The host
evidence gates run commands. If prior host evidence reports a failure, repair the reported issue
only, preserve approved scope, and request the required evidence again.

Return only the declared structured output. List every workspace path you inspected in `inspected`.
Do not make a claim about source you did not read. In `summary`, state changed behavior, tests added or
updated, security checks, fuzz decision, requested host gates, and known gaps. Do not claim a
host check passed unless its result is present in the workflow context. List every changed file in
`files_changed`.

## Output contract

Reply with only a JSON object that satisfies the output schema appended to this task. Do not
use a skill report format, markdown, or extra fields. The schema declares the only valid keys.
An invalid shape is rejected and you will be asked again with the schema.
