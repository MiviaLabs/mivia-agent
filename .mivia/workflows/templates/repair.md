# Repair Feature

Repair the bounded feature `{{ inputs.task }}` after this host evidence failure:

{{ evidence.failed_evidence }}

Use this approved delivery plan:

{{ evidence.plan }}

Use this approved test plan when it is available:

{{ evidence.test_plan }}

Read the relevant source and tests. Edit only files that repair the reported failure and stay
within the approved scope. Preserve test coverage for accepted behavior, negative paths, and
structured input. Recheck security, safe paths, external input, privileges, fail-closed guards,
and hook policy.

Do not run commands, commit, push, publish, bypass hooks, or read secret-like files. Do not
claim a host check passed unless the workflow context gives its result.

Return only the declared structured output. List every workspace path you inspected in
`inspected`. Do not make a claim about source you did not read. In `summary`, state the repair,
tests changed, security checks, fuzz decision, requested host gates, and known gaps. List every
changed file in `files_changed`.

## Output contract

Reply with only a JSON object that satisfies the output schema appended to this task. Do not
use a skill report format, markdown, or extra fields. The schema declares the only valid keys.
An invalid shape is rejected and you will be asked again with the schema.
