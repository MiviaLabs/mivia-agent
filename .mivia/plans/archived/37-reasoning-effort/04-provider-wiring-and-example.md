# Phase 04 - Propagation to every request path

The complete inventory of non-test `provider.Request` constructors:

| # | Site | Carrier |
|---|---|---|
| 1 | `internal/chat/context_integration.go` `sendPlainLegacy` | `snapshot.binding.Profile` |
| 2 | `internal/chat/context_integration.go` `sendPlainContext` | `snapshot.binding.Profile` |
| 3 | `internal/agent/loop.go` `runStep` | `agent.Options` |
| 4 | `internal/subagents/oneshot.go` | handler fields |
| 5 | `internal/provider/openai_compat.go` `readStream` fallback | covered in phase 02 |

Files:

- `internal/chat/context_integration.go` - set both fields on sites 1 and 2.
- `internal/agent/loop.go` - `Options.ReasoningLevel` / `Options.ReasoningDialect`, copied into the request in `runStep`.
- `internal/chat/session.go` - `sendAgent` fills those options from `snapshot.binding.Profile`.
- `internal/subagents/oneshot.go`, `internal/subagents/multi_step.go` - handler fields, set on the request / on `loopOptions`.
- `internal/cli/dispatcher.go`, `internal/cli/agent_binding.go`, `internal/cli/agent_task_handler.go` - carry the resolved profile's pair into every nested handler. The profile is already resolved at each site through `selectableModel`.
- `internal/cli/model_binding.go` - the legacy no-runtime switch path rewrites `Profile.Name` in place; it must clear the reasoning pair too, or a switch there inherits the previous model's dial.

To keep the already-long `register*` signatures from growing by two, pass one
`reasoning.Setting{Level, Dialect}` value.

## Tests first (integration)

- A session bound to a model with `reasoning = "high"` sends the dialect's
  fields on a **plain** turn; a model without it sends none. Both request
  bodies are captured from a stub transport.
- The same, for an **agent** turn, and the two bodies agree for one binding.
- Switching from a reasoning model to a non-reasoning model clears the fields.
- A nested `multi_step` subagent turn carries the pair.
- A one-shot subagent turn carries the pair.
- A routed agent pinned to a different model carries **that** model's pair, not
  the session's.

Gate: `go test ./internal/chat/... ./internal/agent/... ./internal/subagents/... ./internal/cli/...`
plus `go test -race` on the same set.
