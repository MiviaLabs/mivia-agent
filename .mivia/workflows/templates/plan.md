# Delivery Plan

Plan one bounded feature slice for `{{ inputs.task }}`.

Read the workspace instructions and the relevant source, interfaces, tests, configuration,
and security boundaries. Do not edit files in this step. Do not run commands, commit, push,
publish, or read secret-like files.

Lock the scope. Identify:

- the requested behavior and acceptance criteria;
- the production and test files that need changes;
- the affected interfaces and compatibility risks;
- security, privacy, hook, and path-safety boundaries;
- the host evidence gates needed after implementation.

Make a small ordered plan. Include the test-first order. Include negative paths and structured
input cases when they apply: empty, malformed, oversized, and duplicate input. State whether a
deterministic fuzz target is practical. If it is practical, request a bounded host fuzz gate.
Otherwise state why it is not practical.

Return only the declared structured output. Put the locked scope, test plan outline, security
checks, fuzz decision, and requested host gates in `summary`. Put ordered actions in `steps`.
