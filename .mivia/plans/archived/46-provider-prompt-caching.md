# 46 - Provider prompt caching

**Status:** ✅ Implemented (2026-08-02) - v1 slice shipped; see Step 0 correction below for what was cut and why.
**Date:** 2026-08-02
**Depends on:** nothing hard; interacts with `41`/`43` compaction plans (cache-aware planning).
**Blocks:** nothing, but plan `47` (deferred tool loading, if written) compounds its savings.
**Blast radius:** MEDIUM - provider request assembly, cost accounting, compaction interaction. No tool-surface or persistence changes.

## Step 0 correction (2026-08-02)

Before implementation, `internal/provider` was read in full: all three built-in
providers (`deepseek`, `zai`, `openrouter`) share one `OpenAICompat` client
that speaks a plain OpenAI-compatible `/chat/completions` wire format
(`Message.Content` is a bare string, not a content-block array). **None of
them speak Anthropic-style `cache_control` content-block markers.** Verified
against provider docs: DeepSeek's context caching is fully automatic
server-side, reporting `usage.prompt_cache_hit_tokens`/`prompt_cache_miss_tokens`
with no request change; OpenRouter's own docs list DeepSeek/OpenAI-family/Z.AI
upstreams as automatic-caching (no markers), while marker-style caching there
applies only to Anthropic/Gemini(non-2.5)/Qwen models, none of which this
repo's `mivia.toml` configures by default.

**Cut from this increment** (§3.2 breakpoint planner, §3.3 marker emission,
§3.6 strip-and-retry fallback/disable latch): no provider configured in this
codebase takes the marker-style wire path today, so building it now would be
unreachable, untestable code - the exact anti-pattern the ADLC Step 0 gate
exists to catch ("an abstraction nothing will reach and nothing has
contracted"). **Sequencing constraint:** this ships when an explicit-marker
provider (native Anthropic, or OpenRouter routed to an Anthropic/Gemini/Qwen
model) is actually configured and testable here - write a short follow-up
plan at that point rather than reviving this section from memory.

**What shipped instead:** a provider-agnostic `CacheStyle`/`CacheUsage` type
(`internal/provider/cache.go`), tolerant usage-field parsing across both the
DeepSeek and OpenAI/OpenRouter usage conventions (wired into both the
non-stream and streaming response paths - a hostile Step 0 review caught a
real bug here: the trailing usage-only SSE chunk carries an empty `choices`
array and was being silently dropped by the existing choices-length guard
before usage extraction ever ran), a `[provider] prompt_cache = "auto"|"off"`
config toggle (default `auto`) that gates only this host's capture/publication
of usage accounting - it cannot and does not disable a provider's own
automatic caching - an `events.CacheUsageEvent`/`KindCacheUsage` observability
event mirroring `CompactionEvent`'s sealed, content-free pattern, and a
byte-stability regression test locking in the existing request serialization
as cache-friendly. **v1 does zero outgoing request mutation** (no
`stream_options.include_usage` injection): a hostile review flagged that as
an unconfirmed-compatibility risk to the default `zai` provider for a feature
whose entire premise was supposed to carry no such risk, so streaming capture
is opportunistic-only (captures usage if a provider happens to include it,
never asks for it) until per-provider support is confirmed.

**Known, deliberate coverage gap (not fixed, tested as a boundary):** plain
`--no-tools` chat sessions call `Completer.ChatStream` directly
(`internal/chat/context_integration.go`'s `sendPlain*`), bypassing
`agent.Loop`/`EmitCacheUsage` entirely - `ChatStream` returns `(string,
error)` with no structured usage to carry back, and widening that public
interface signature was judged disproportionate scope for this slice.
`--no-tools` is an explicit opt-out from the default (`useTools :=
!invocation.noTools`), so the default interactive path and all subagent
turns are covered. See `TestChatStreamDoesNotPublishCacheUsage`.

## 1. Goal

Eliminate re-paying the static request prefix (system prompt + tool schema block
+ stable history head) on every provider turn by emitting provider-side prompt
cache markers. Today `internal/provider` contains no `cache_control` (or
equivalent) anywhere; every request re-bills the full prefix. This is the
single largest token-cost gap in the harness.

## 2. Current state

- Request assembly serializes system prompt, full tool schemas, and the entire
  message history on every call (`internal/provider`, cost model in
  `internal/provider/context.go`).
- Tool schemas are registered eagerly (`internal/tools/default_registry.go`)
  and identical across turns within a binding - ideal cache material.
- Compaction (`internal/contextmgr/planner.go`) rewrites the message prefix,
  which would invalidate any prefix cache; it is currently cache-oblivious.
- Token estimation is the `len/4` heuristic with no reconciliation against
  provider-reported usage, so cache savings would be invisible to our own
  accounting.

## 3. Design

### 3.1 Provider capability seam

Add an optional capability interface on the provider contract, e.g.
`PromptCacheSupport` reporting: supported (bool), marker style
(anthropic `cache_control: ephemeral` blocks vs OpenAI-style implicit prefix
caching vs none), max breakpoints, and minimum cacheable prefix size.
Providers that do not implement it behave exactly as today.

### 3.2 Breakpoint placement policy (host-owned, deterministic)

For marker-style providers, place breakpoints at, in order of stability:

1. End of the tool-schema/system block (changes only on binding or agent
   surface change - both already tracked by `BindingFence` fields
   `ModelGeneration`/`AgentSurfaceGeneration` in `internal/chat/fencing.go`).
2. End of the last compaction boundary (the retained prefix is immutable until
   the next compaction).
3. Optionally a rolling breakpoint before the most recent N messages.

Placement must be a pure function of the request (deterministic, testable),
mirroring how the compaction planner is pure.

For implicit-prefix providers, the work is instead to keep the serialized
prefix byte-stable: stable tool ordering, stable JSON key ordering, no
timestamps or nonces in the system prompt.

### 3.3 Compaction interaction

- Compaction invalidates the history-prefix cache by construction; that is
  acceptable (it happens at most every ~30% of budget growth). The
  system+tools breakpoint survives compaction and must be placed so that it
  does.
- The planner/trigger math should account for the fact that a compaction has a
  one-turn cache-refill cost; do NOT let this delay a needed compaction
  (safety authority stays with the host, per plan `42`), but record the cost in
  observability.

### 3.4 Accounting and observability

- Capture provider-reported usage fields (cache read/write/input tokens) from
  responses and publish them on the event bus alongside the existing estimate,
  so operators can see hit rates and so the `len/4` estimator can be
  calibrated later (separate plan).
- Emit a bounded event when a cache-invalidating change occurs (binding
  switch, compaction, tool-set change) naming the cause category only.

## 4. Invariants

- Cache markers are metadata only: identical model-visible content with and
  without caching. A golden-request test must prove byte-equality of the
  content portions.
- No secrets or redacted material change exposure: caching stores what was
  already being sent. Redaction (`internal/redact`) runs before assembly,
  unchanged.
- Marker placement never splits an assistant/tool-result unit.
- A provider rejecting cache markers must degrade to a plain request
  transparently (strip-and-retry once, then disable for the session binding).

## 5. Implementation steps

1. Add `PromptCacheSupport` capability + no-op default; wire provider metadata.
2. Implement deterministic breakpoint planner as a pure function with unit
   tests (binding change, compaction boundary, small-prefix skip).
3. Anthropic-style marker emission in request assembly behind the capability.
4. Byte-stability audit of the serialized prefix (tool ordering, JSON
   canonicalization) for implicit-prefix providers.
5. Usage capture: parse cache-related usage fields into a typed struct,
   publish via `internal/events`.
6. Strip-and-retry fallback + per-binding disable latch.
7. Config: `[provider] prompt_cache = "auto" | "off"` (default auto).

## 6. Testing

- Pure-planner unit tests for breakpoint placement across compaction and
  binding-switch sequences.
- Golden request fixtures per provider: with/without capability, asserting
  content equality and marker positions.
- Regression: compaction immediately followed by a turn still yields a valid
  request with the surviving system+tools breakpoint.
- Fallback path: simulated provider 400 on markers -> retry without -> latch.

## 7. Failure analysis

- Marker placed on a mutable prefix (e.g. dynamic status line in system
  prompt) silently yields 0% hit rate: covered by byte-stability audit + hit
  rate observability.
- Provider billing surprises (cache writes cost more than reads on some
  providers): placement policy must require a minimum expected reuse (>=1
  subsequent turn), which the agent loop guarantees except on the final turn -
  acceptable.
