# Phase 03 - Model config and request propagation

Files:

- Modify `internal/config/types.go` and its tests.
- Modify `internal/chat/binding.go`.
- Modify `internal/chat/session.go` and focused integration tests.
- Modify `internal/agent/loop.go` and focused tests.

Add `reasoning` to the closed model object using the shared lower-level type.
Reject invalid values and unknown keys. Carry the active profile through both
request paths:

- direct plain chat requests in `internal/chat/session.go`;
- agent turns through `agent.Options` and `internal/agent/loop.go`.

Tests must prove model switching activates the selected level and switching back
to a model without the field clears it. Capture requests in both paths and prove
the value reaches the provider request.

Gate: `go test ./internal/config/... ./internal/chat/... ./internal/agent/...`.
