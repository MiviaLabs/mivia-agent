# Phase 02 - Provider dialects and request shaping

Files:

- Create `internal/provider/reasoning.go` and its tests.
- Modify `internal/provider/provider.go`.
- Modify `internal/provider/openai_compat.go` and its focused tests.

Add the dialect interface and built-ins for top-level OpenAI-compatible
`reasoning_effort` and `thinking.type` shapes. Add the level to
`provider.Request`, add the dialect to `CompatOptions`, merge dialect fields
without mutating caller input, and conditionally remove temperature/top_p from
the serialized body when sampling suppression is active.

Tests first and required cases:

- openai, thinking, effort-plus-thinking, off, empty, and nil-dialect mappings;
- active reasoning removes sampling from the wire body but not from the input
  request;
- unset reasoning preserves the existing body shape byte-for-byte;
- extra-body merging cannot override reserved request fields unexpectedly.

Gate: `go test ./internal/provider/...` and `go test -race ./internal/provider/...`.
