# Implement Feature

Implement the bounded feature `{{ inputs.task }}`.

Use this approved delivery plan:

{{ evidence.plan }}

Use this approved test plan when it is available:

{{ evidence.test_plan }}

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

Return only the declared structured output. In `summary`, state changed behavior, tests added or
updated, security checks, fuzz decision, requested host gates, and known gaps. Do not claim a
host check passed unless its result is present in the workflow context. List every changed file in
`files_changed`.
