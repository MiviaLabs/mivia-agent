---
id: non_stream_header_wait_is_generation_time
title: A non-stream LLM request's header wait is the model thinking, not a stall
content: ResponseHeaderTimeout on a non-stream completion is a ceiling on thinking time; only streaming requests may carry a header bound, and every Completer must honor req.StreamTransport.
importance: high
tags: [[provider, timeouts, streaming, subagents, anthropic, transport]]
---

# A non-stream header wait is generation time

## The trap

A response-header bound catches a peer that accepts a request and then goes
silent. That is only what it catches for a request whose headers are supposed
to arrive immediately.

- **Streaming request**: the provider opens the stream, headers return at
  once, and the model's work arrives afterwards as body bytes, where the
  stream watchdogs measure it. A header bound is a correct stall detector.
- **Non-stream completion**: the provider sends *no byte at all* until the
  whole answer exists. Its wait for headers **is** the generation. A header
  bound there is a ceiling on how long a model may think, enforced below the
  layer that knows the caller's budget.

On 2026-08-28 a 120-second `DefaultResponseHeaderTimeout` was applied to every
request of both kinds. Combined with the native Anthropic client ignoring
`req.StreamTransport` (so every nested subagent turn went out non-stream),
this capped all subagent thinking at two minutes regardless of the configured
600s/1200s budgets - and then reported it as a timeout, via
[[transport-stage-timeout-is-not-a-deadline]].

## What to do

1. `net/http` scopes `ResponseHeaderTimeout` to the Transport, so a client
   carries **one transport per header phase** and selects per request through
   a context marker set where the request is built
   (internal/provider/header_bound.go). Streaming keeps the bound; the
   generation phase carries none and is bounded by the caller's request
   timeout and the client-wide backstop.
2. Any new Completer must mark the phase in its request builder. There are
   exactly two such builders: `openai_compat_request.go newRequest` (keys on
   `req.Stream`) and `anthropic.go newHTTPRequest` (keys on the body's stream
   flag).
3. Any new Completer must honor `req.StreamTransport` - stream on the wire,
   non-stream contract on the return path. A Completer that ignores it
   silently reintroduces the ceiling for every nested turn.
4. When splitting a transport, apply the loopback dial pin to **every**
   resulting transport. It is the keyless-ollama security gate, and a pin on
   one pool only leaves the other dialling whatever a resolver answered.
