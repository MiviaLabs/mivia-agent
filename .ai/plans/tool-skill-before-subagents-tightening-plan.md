# Tool, Skill, and Subagent Boundary Tightening

## Confirmed current risks

- All model-directed execution must be admitted by `internal/runtime.Dispatcher`; direct registry execution is forbidden outside the adapter.
- Permission grants are explicit and fail closed. Registration creates a named grant; a caller cannot execute an ungranted kind/name.
- Scope admission must use cancellation-aware bounded queues. A blocked invocation must not create a goroutine per waiter.
- Terminal outcomes must be idempotent by request fingerprint, including failure, cancellation, timeout, and duplicate identity reuse.
- Budgets must be cumulative across a run and child invocations; retry attempts consume the same budget.
- Skill schemas must validate nested types, required properties, enums, arrays, and output contracts before/after execution.
- Subagent scheduling must use a bounded worker pool, reject idempotency-key collisions, preserve dependency provenance, and apply explicit partial-result policy.

## Implementation order

1. Finish dispatcher lifecycle events, cumulative budget accounting, request-fingerprint checks, terminal-outcome caching, and text/JSON redaction.
2. Complete skill discovery/selection metadata: version identity, declared scope, tool capability allowlists, schema validation, permission grants, and bounded execution policy.
3. Complete subagent worker scheduling: bounded worker goroutines (not goroutine-per-task), dependency causal status, ownership/provenance, idempotency, conflict resources, timeout/cost/depth/fan-out limits, and cancellation.
4. Add Semgrep rules and executable adversarial fixtures for direct execution bypasses, unbounded fan-out, and raw sensitive metadata/persistence.
5. Run focused tests, full tests, race, vet, build, secret scan, Semgrep, docs, and repository gates; independently review findings before commit/push.

## Static enforcement

Semgrep must reject production calls to `Registry.Execute` outside `internal/runtime`, and the rule contract must be tested against adversarial fixtures. Runtime tests remain necessary for cancellation, budgets, conflicts, retries, redaction, and deterministic aggregation; static checks are not substitutes for behavior proof.
