---
id: sibling_implementations_drift
title: A contract enforced in one implementation is absent from its sibling
content: When an interface has more than one implementation, a flag or bound honored by one is routinely dropped by the other; prove coverage with a shared conformance suite, never with a prose Sweep line.
importance: high
tags: [[review, invariants, interfaces, provider, testing, sweep]]
---

# Sibling implementations drift, and prose sweeps do not catch it

## The pattern

This repo's most expensive recurring defect is not a wrong line of code. It is
a contract that **exists in one implementation of an interface and is silently
absent from another**. The deficient sibling is usually the newer or less
travelled one, so it fails only on the paths nobody watches.

On 2026-08-29, seven bugs of this one shape were found in `provider.Completer`,
which has two real implementations (`OpenAICompat`, `AnthropicCompleter`):

| Contract | OpenAICompat | AnthropicCompleter (before) |
|---|---|---|
| honors `req.StreamTransport` | yes | **no** |
| honors `req.DisableProviderReplay` | yes | **no** |
| body reads wrapped by the idle watchdog | 4 of 4 sites | **0 of 2** |
| torn tool-call stream rejected | yes | **no** |

## Why the existing gates missed it

- `make verify`, semgrep, and the structure gate check *shape*, not
  cross-implementation *behaviour*. None of them can see that a field is read
  in one file and not another.
- The `Sweep:` commit trailer is validated for **presence, not content**. The
  commit that introduced the 120s header ceiling carried a Sweep line that was
  literally true ("every http.Client construction routes through
  compatBaseRoundTripper") and completely beside the point: it swept
  construction sites when the invariant at risk was per-request-mode
  semantics. A prose sweep is self-attested and cannot fail.
- Provider tests leaned on hand-written fake Completers that bypass the HTTP
  path entirely, so no test exercised the real transport, the real timers, or
  a peer that stops responding.

## What to do instead

1. **Write conformance suites, not per-implementation tests.** A table that
   runs EVERY implementation of an interface through the same behavioural
   assertions turns "did we update the sibling?" from a memory exercise into a
   compile-and-run failure. A new implementation joins the table or the
   omission itself is visible.
2. **Test against a real server, not a fake.** `httptest` plus a real
   `http.Transport` found every one of these; the fakes had been green for
   months. Fakes cannot fail on a timer or a half-sent body.
3. **When writing a `Sweep:` trailer, sweep by the MECHANISM, not the
   symptom.** Name the invariant, then grep for every site that could violate
   it, then state pass/fail per site. If the sweep only lists sites that
   construct a thing, it has not checked what the thing does.
