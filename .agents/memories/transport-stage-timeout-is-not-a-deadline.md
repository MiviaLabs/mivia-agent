---
id: transport_stage_timeout_is_not_a_deadline
title: net/http stage timeouts claim equality with context.DeadlineExceeded
content: A response-header or client-backstop timeout satisfies errors.Is(err, context.DeadlineExceeded) with no context expired; use provider.IsTransportStageTimeout to tell a transport fault from a spent budget.
importance: high
tags: [[provider, timeouts, errors, classification, transport, http]]
---

# A transport stage timeout is not the caller's deadline

## The trap

`net/http` defines its timeout error as:

```go
func (e *timeoutError) Is(err error) bool { return err == context.DeadlineExceeded }
```

So `errors.Is(err, context.DeadlineExceeded)` reports **true** for a
`Transport.ResponseHeaderTimeout` firing and for the `http.Client.Timeout`
backstop, while every context in the chain is still live. Any code that
branches on that test alone cannot tell "our own transport gave up on a
working call" from "the budget this call was given ran out".

On 2026-08-29 this produced three bugs at once from one commit: the retry
round tripper refused to retry a header timeout, `IsTransient` classified it
permanent, and the subagent envelope reported `"timed_out"` while the
configured request (600s) and total (1200s) budgets had minutes to spare.
Operators were sent tuning timeout knobs that were never the cause.

## What distinguishes them

**Identity, not equality.** A real context deadline carries the
`context.DeadlineExceeded` *value* in its error chain. A transport stage
timeout only claims equality with it and carries nothing. Verified against
the live standard library (Go 1.26):

| timer | `errors.Is(…, DeadlineExceeded)` | carries the value |
|---|---|---|
| `ResponseHeaderTimeout` | true | **no** |
| request context deadline | true | **yes** |
| `http.Client.Timeout` | true | **no** |

## What to do

Use `provider.IsTransportStageTimeout(err)` (internal/provider/stage_timeout.go).
It walks the chain by identity and ignores any `Is` method that merely claims
equality. `TestStdlibTimerErrorIdentities` pins the table above against the
live standard library, so a future Go release that changes which timer carries
the sentinel fails there rather than silently re-arming the confusion.

Never write a bare `errors.Is(err, context.DeadlineExceeded)` branch to mean
"the caller's budget ran out" on any path that can reach an HTTP transport.

## Related

A second, separate trap in the same area: a header bound is a **stall
detector only for a request whose headers arrive immediately**. A non-stream
LLM completion sends nothing until the generation is finished, so its header
wait IS the model's thinking time and a header bound there is a generation
ceiling. See [[non-stream-header-wait-is-generation-time]].
