# Test Plan

Create a test plan for `{{ inputs.task }}` from this approved delivery plan:

{{ evidence.plan }}

Prior review findings (present on repair iterations only):

{{ evidence.review_findings }}

When review findings are present, address each finding before you resubmit.

Read the relevant production code and tests. Do not edit files in this step. Do not run
commands, commit, push, publish, or read secret-like files.

Specify tests before implementation. Cover success behavior and each reachable error or negative
path. For decoded or parsed untrusted structured input, include empty, malformed, oversized, and
duplicate input. Include security and hook-policy regression tests when the scope can affect them.
State the focused and full host evidence gates that must prove the change. State the fuzz decision
and the requested bounded host fuzz gate when practical.

Return only the declared structured output. List every workspace path you inspected in `inspected`.
Do not make a claim about source you did not read. Put the test cases, security cases, fuzz decision,
and required host evidence gates in `summary`. Put the test-first actions in `steps`.

## Output contract

Reply with only a JSON object that satisfies the output schema appended to this task. Do not
use a skill report format, markdown, or extra fields. The schema declares the only valid keys.
An invalid shape is rejected and you will be asked again with the schema.
