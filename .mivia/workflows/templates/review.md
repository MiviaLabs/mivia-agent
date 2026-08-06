# Review Delivery Step

Independently review the implemented delivery for `{{ inputs.task }}`.

Approved plan:

{{ evidence.plan }}

Test plan:

{{ evidence.test_plan }}

Implementation summary:

{{ evidence.implementation }}

Read the relevant source and tests. Do not edit files. Do not run commands, commit, push,
publish, or read secret-like files.

Check that the work stays within scope. Check that tests cover success and reachable negative
paths. When untrusted structured input applies, check empty, malformed, oversized, and duplicate
input coverage. Check security, privacy, safe paths, fail-closed behavior, and hook policy.
Check that the fuzz decision is explicit. Check that the report does not claim host evidence that
the workflow context does not provide.

Request changes for any missing requirement, unsafe behavior, missing host gate, or unsupported
claim. Do not approve a step only because it has a low-severity finding. Return only the declared
structured output. Use `approved` only when no finding remains. Otherwise use
`changes_requested` and list each finding with severity and a concrete reason.
