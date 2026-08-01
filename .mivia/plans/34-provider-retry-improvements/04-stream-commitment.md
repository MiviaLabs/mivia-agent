# Phase 04 - Streaming transport and commitment boundary

Files:

- Modify `internal/provider/stream_defects_test.go` or the existing provider
  stream test file that owns the scenario.
- Modify `internal/provider/openai_compat_test.go` only if existing SSE parser
  tests are the correct home for the in-band error case.

Tests first (RED):

- Direct `ChatStream` SSE: one pre-commit HTTP 429 followed by a 200 stream
  succeeds with two upstream calls.
- Tool-capable `ChatTurn`/`chatTurnStream`: the same pre-commit 429 behavior
  succeeds through the shared transport.
- Both paths return an HTTP-200 in-band SSE error after one request, with no
  transport replay and no empty-stream fallback request.
- Existing committed content, tool-call, and stream-writer behavior remains
  unchanged.

Implementation (GREEN):

- No new stream retry loop. Rely on the shared transport for pre-commit HTTP
  responses and leave committed SSE parsing/fallback semantics intact.
- Correct response fixture ordering: set headers before `WriteHeader`.

Gate: focused stream tests, then `go test -race ./internal/provider`.
