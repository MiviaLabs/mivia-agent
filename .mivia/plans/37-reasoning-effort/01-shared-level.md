# Phase 01 - Shared reasoning level

Files:

- Create `internal/reasoning/reasoning.go`.
- Create `internal/reasoning/reasoning_test.go`.

Implement the finite provider-neutral level type and validation. Empty means
unset; `off`, `minimal`, `low`, `medium`, `high`, `xhigh`, and `max` are the
accepted values. Keep this package independent of `config` and `provider` so it
cannot introduce an import cycle.

Tests first:

- Every accepted value validates.
- Empty is accepted as unset.
- An arbitrary string is rejected with a stable error category.

Gate: `go test ./internal/reasoning/...`.
