# Provider Adapter Refactor Plan

**Status:** Final (validated by sub-agent challenge)
**Author:** mivia (root agent)
**Last Updated:** 2025
**Stakeholders:** `internal/provider/`, `internal/agent/loop.go`, `internal/config/`, tests

---

## Table of Contents

1. [Executive Summary](#1-executive-summary)
2. [Current Architecture](#2-current-architecture)
3. [Problem Inventory](#3-problem-inventory)
4. [Proposed Changes](#4-proposed-changes)
   - 4.1 Provider Registry Pattern (with Config Pluggability)
   - 4.2 Unify SSE Streaming
   - 4.3 Fix Idempotency Key for Retries
   - 4.4 Remove Recursive Empty-Stream Fallback
   - 4.5 Consolidate Thin Wrappers → Config Helpers
   - 4.6 Token Estimation (Research Decision)
   - 4.7 Retry Options in TOML Config
   - 4.8 Circuit Breaker (Deferred)
   - 4.9 Completer Interface Extension (Design Note)
5. [Phase Plan & Dependencies](#5-phase-plan--dependencies)
6. [Risk Matrix](#6-risk-matrix)
7. [Testing Strategy](#7-testing-strategy)
8. [Files Changed Summary](#8-files-changed-summary)
9. [Open Questions](#9-open-questions)
10. [Appendix: Sub-Agent Validation Results](#10-appendix-sub-agent-validation-results)

---

## 1. Executive Summary

The `internal/provider/` package is sound in its core (`Completer` interface,
`OpenAICompat` base, retry transport) but has accumulated several anti-patterns:

| # | Issue | Severity | Phase |
|---|-------|----------|-------|
| 1 | Hardcoded switch in `New()` factory + parallel switch in `config/load.go` | 🔴 High | 1 |
| 2 | Duplicate SSE parsing paths (`readStream` vs `readTurnStream`) | 🟡 Medium | 1 |
| 3 | Idempotency key includes sequence number, defeating retry dedup | 🟡 Medium | 1 |
| 4 | Silent recursive empty-stream fallback (potential infinite loop) | 🟡 Medium | 2 |
| 5 | Thin wrapper files are identical config data, not behavior | 🟢 Low | 2 |
| 6 | Coarse token estimation (`len/4`) causes pruning inaccuracy | 🟡 Medium | 3 |
| 7 | Retry config exists in code but is not user-configurable | 🟢 Low | 2 |
| 8 | No circuit breaker for cascading provider failures | 🟢 Low | Deferred |
| 9 | Completer interface not designed for non-OpenAI APIs | 🟡 Medium | Design note |

**3-phase approach:** Phase 1 (structural, high-confidence), Phase 2 (cleanup/config),
Phase 3 (quality/deferred).

---

## 2. Current Architecture

```
Completer interface             (provider/provider.go)
  ├── Name() string
  ├── Chat(ctx, req) -> string, err
  ├── ChatStream(ctx, req, w) -> string, err
  └── ChatTurn(ctx, req) -> *Response, err

OpenAICompat struct             (provider/openai_compat.go)
  ├── Chat()          → non-stream text-only via ChatTurn(stream=false)
  ├── ChatStream()    → SSE via readStream() (no-tools) or chatTurnStream() (tools)
  ├── ChatTurn()      → SSE via chatTurnStream() or non-stream via doJSON()
  ├── doJSON()        → non-stream HTTP POST + JSON decode
  ├── newRequest()    → HTTP request construction + idempotency key
  ├── httpError()     → error formatting
  └── sanitizeErr()   → error truncation

chatTurnStream()                (provider/openai_compat_stream.go)
  ├── SSE: applyStreamChunk() + mergeToolCallDeltas() + orderedToolCalls()
  └── Fallback: empty-stream → recursive ChatTurn(ctx, req)

readStream()                    (provider/openai_compat.go)
  ├── SSE: text-only delta accumulation
  └── Fallback: empty-stream → recursive Chat(ctx, req)

NewDeepSeek(opts)               (provider/deepseek.go)
  └── NewOpenAICompat(name="deepseek", base=..., key, "", "")

NewOpenRouter(opts)             (provider/openrouter.go)
  └── NewOpenAICompat(name="openrouter", base=..., key, referer, title)

provider.New(res.config)        (provider/provider.go)
  └── switch DeepSeekName → NewDeepSeek
      switch OpenRouterName → NewOpenRouter

config.resolveProvider()        (config/load.go)  ← PARALLEL SWITCH
  └── switch DeepSeekName → set defaults
      switch OpenRouterName → set defaults
```

---

## 3. Problem Inventory

### 3.1 Hardcoded Switches (🔴 High)

Two parallel hardcoded switches exist — one in `provider/provider.go:48-53` and
one in `config/load.go:107-127`. Adding a new provider today requires editing
**4 files**:

1. `config/defaults.go` — add consts (name, default model, URL, API key env)
2. `config/load.go` — add case in `resolveProvider()` switch
3. `provider/deepseek.go` (or new file) — thin constructor wrapper
4. `provider/provider.go` — add case in `New()` switch

The duplicate switch in config was **missed** by the initial analysis — the
registry in `provider/` alone solves only half the problem.

### 3.2 Duplicate SSE Paths (🟡 Medium)

`readStream()` (openai_compat.go:~190) and `readTurnStream()` (openai_compat_stream.go)
are separate functions with nearly identical SSE parsing logic:

- Both scan with `bufio.Scanner` (same buffer size: 64KB initial, 1MB max)
- Both check `[DONE]`, parse `data:` lines
- Both handle `chatResponseBody` unmarshalling
- Both check for `chunk.Error`
- Both accumulate content strings
- Both have empty-stream fallback

The no-tools path is a strict subset of the tools-aware path. The tools path
adds tool-call delta merging but is otherwise identical.

**Risk of maintaining both:** Drift — fixes to one path may not be applied to
the other.

### 3.3 Broken Idempotency for Retries (🟡 Medium)

Current implementation (`openai_compat.go:~290`):

```go
key := sha256.Sum256(raw)
httpReq.Header.Set("Idempotency-Key", fmt.Sprintf("mivia-%d-%x",
    c.requestSeq.Add(1), key[:]))
```

The `requestSeq` prefix makes every request unique — even retries of the same
payload. If a provider accepts the request, responds with 500, and the retry
transport retries, the retry has a different idempotency key, so the provider
cannot deduplicate. This can cause duplicate tool executions on the provider
side (though the tool call ID prevents duplicate execution on the agent side).

**Provider behavior varies:**
- OpenAI: `Idempotency-Key` is advisory; duplicate detection is best-effort
- DeepSeek: Does not document idempotency support
- OpenRouter: Passes through to upstream provider

### 3.4 Recursive Empty-Stream Fallback (🟡 Medium)

Two locations where empty streams trigger silent non-stream retry:

1. `chatTurnStream()` lines 64-68: `content=="" && len(toolCalls)==0` → recurse
2. `readStream()` lines 213-220: `full.Len()==0` → recurse

If the fallback also returns empty, this is an infinite recursion bounded only
by context timeout (`~180s`). Valid cases for empty-stream retry exist
(some providers send empty initial chunks), but the adapter should handle this
with a **limited retry count** or return an error to the caller.

### 3.5 Thin Wrappers as Duplicate Code (🟢 Low)

`deepseek.go` (20 lines) and `openrouter.go` (26 lines) are structurally
identical — just light config differences (BaseURL, HTTP-Referer, X-Title).
No behavioral divergence exists. This is not a bug but adds unnecessary
ceremony per provider.

### 3.6 Coarse Token Estimation (🟡 Medium)

```go
func estimateTokens(s string) int {
    n := len(s) / 4
    ...
}
```

The `len/4` heuristic:
- **English prose:** ~70-80% accuracy (typically underestimates by 20-30%)
- **Code:** ~40-50% accuracy (code tokens are denser)
- **CJK:** ~10-20% accuracy (each CJK character is 1-2 tokens, not 0.25)
- **Mixed content:** Unpredictable

Impact on `PruneMessagesKeepTurns()`: underestimation → context overflow → API
rejection; overestimation → premature pruning → lost working memory.

### 3.7 Retry Config Not Exposed (🟢 Low)

`NewOpenAICompatWithRetry()` is exported, tested, but never called in production.
Retry options are hardcoded:

```go
func defaultRetryOptions() retryOptions {
    return retryOptions{MaxRetries: 3, BaseDelay: 200ms, MaxDelay: 5s}
}
```

Users who hit aggressive rate limits have no way to tune this without forking.

### 3.8 No Circuit Breaker (🟢 Low, Deferred)

The `retryRoundTripper` will retry 3 times on every request to a saturated
provider, amplifying load. For a CLI tool where sessions are minutes long,
this is acceptable but not ideal for long-running agent sessions.

### 3.9 Completer Interface Not Future-Proof (🟡 Medium)

`Completer` assumes an OpenAI-compatible chat completions API. Adding a
provider with a fundamentally different API (Anthropic Messages API, Google
Gemini SDK, Ollama custom protocol) would require:

1. A new struct that satisfies `Completer`
2. Possibly new methods on `Completer` for non-chat APIs
3. Different error handling, streaming, token counting

The interface is not wrong — OpenAI-compatible is the most common API surface.
But the plan should acknowledge this assumption and document the extension path.

---

## 4. Proposed Changes

### 4.1 Provider Registry Pattern (with Config Pluggability)

**Objective:** Replace both hardcoded switches with a registry that carries
constructor + defaults. Adding a provider becomes one file + one registration
call — not 4 files.

**Design:**

```go
// provider/provider.go

// ProviderInfo holds defaults for both the provider adapter and config resolution.
type ProviderInfo struct {
    Name         string
    DefaultModel string
    DefaultURL   string
    DefaultEnv   string // API key env var name
    Constructor  func(opts Options) (Completer, error)
    // ExtraHeaders is provider-specific HTTP headers (e.g. OpenRouter's Referer).
    ExtraHeaders map[string]string
}

var registry = map[string]ProviderInfo{}

func Register(info ProviderInfo) {
    name := strings.ToLower(info.Name)
    if _, ok := registry[name]; ok {
        panic(fmt.Sprintf("provider %q already registered", name))
    }
    if info.Constructor == nil {
        panic(fmt.Sprintf("provider %q: nil constructor", name))
    }
    registry[name] = info
}

func New(res *config.Resolved) (Completer, error) {
    info, ok := registry[res.ProviderName]
    if !ok {
        return nil, fmt.Errorf("unknown provider %q (registered: %s)",
            res.ProviderName, strings.Join(KnownProviders(), ", "))
    }
    opts := Options{
        Name:        res.ProviderName,
        BaseURL:     res.BaseURL,
        APIKey:      res.APIKey,
        Model:       res.Model,
        HTTPReferer: res.HTTPReferer,
        XTitle:      res.XTitle,
    }
    return info.Constructor(opts)
}

// KnownProviders returns sorted registered provider names (for help text).
func KnownProviders() []string {
    names := make([]string, 0, len(registry))
    for n := range registry {
        names = append(names, n)
    }
    sort.Strings(names)
    return names
}
```

**Config integration** (`config/load.go`):

```go
// resolveProviderDefaults queries the provider registry for default values
// when not explicitly set in config. No more switch per-provider.
func resolveProviderDefaults(name string, pc ProviderConfig) ProviderConfig {
    info, ok := provider.LookupInfo(name) // new exported function
    if !ok {
        return pc
    }
    if pc.Model == "" && info.DefaultModel != "" {
        pc.Model = info.DefaultModel
    }
    if pc.BaseURL == "" && info.DefaultURL != "" {
        pc.BaseURL = info.DefaultURL
    }
    if pc.APIKeyEnv == "" && info.DefaultEnv != "" {
        pc.APIKeyEnv = info.DefaultEnv
    }
    return pc
}
```

**Registration (in deepseek.go, openrouter.go, or a new `registration.go`):**

```go
// Explicit wiring — safer than init().
// Called from cmd/mivia/main.go or from an init() chain.
func init() {
    Register(ProviderInfo{
        Name:         "deepseek",
        DefaultModel: "deepseek-v4-flash",
        DefaultURL:   "https://api.deepseek.com/v1",
        DefaultEnv:   "DEEPSEEK_API_KEY",
        Constructor: func(opts Options) (Completer, error) {
            base := opts.BaseURL
            if base == "" {
                base = "https://api.deepseek.com/v1"
            }
            return NewOpenAICompat("deepseek", base, opts.APIKey, "", ""), nil
        },
    })
    Register(ProviderInfo{
        Name:         "openrouter",
        DefaultModel: "openai/gpt-4o-mini",
        DefaultURL:   "https://openrouter.ai/api/v1",
        DefaultEnv:   "OPENROUTER_API_KEY",
        ExtraHeaders: map[string]string{
            "HTTP-Referer": "https://github.com/MiviaLabs/mivia-agent",
            "X-Title":      "mivia",
        },
        Constructor: func(opts Options) (Completer, error) {
            base := opts.BaseURL
            if base == "" {
                base = "https://openrouter.ai/api/v1"
            }
            return NewOpenAICompat("openrouter", base, opts.APIKey,
                orDefault(opts.HTTPReferer, "https://github.com/MiviaLabs/mivia-agent"),
                orDefault(opts.XTitle, "mivia")), nil
        },
    })
}
```

**Alternative to `init()`:** Explicit wiring in `cmd/mivia/main.go`:

```go
import (
    _ "github.com/MiviaLabs/mivia-agent/internal/provider" // init() calls
)
```

This is the Go standard library pattern (see `database/sql` + `_ "github.com/lib/pq"`).
The registry package exports `Register()` and `init()` in each provider file
calls it. Tests can register mock providers without affecting production.

**Sub-agent challenge addressed:** The `ProviderInfo` struct carries defaults
for config resolution, eliminating the parallel switch in `config/load.go`.

### 4.2 Unify SSE Streaming

**Objective:** Eliminate duplicate `readStream()` by routing all streaming through
`chatTurnStream()`/`readTurnStream()`.

**Design:**

```go
// ChatStream always routes through streaming ChatTurn.
// The dedicated no-tools readStream() path is deleted.
func (c *OpenAICompat) ChatStream(ctx context.Context, req Request, w io.Writer) (string, error) {
    req.Stream = true
    req.StreamWriter = w
    resp, err := c.ChatTurn(ctx, req)
    if err != nil {
        return "", err
    }
    return resp.Content, nil
}
```

**No behavioral change:** `chatTurnStream()` already handles plain text deltas
correctly — `applyStreamChunk()` writes content to `content` buffer and to `w`
(via `liveWrite` flag). The tool-call delta merging adds zero overhead when no
tool deltas arrive.

**Delete:** `readStream()` method entirely (~50 lines).
**Keep:** All helper functions (`applyStreamChunk`, `mergeToolCallDeltas`,
`orderedToolCalls`).

**Edge cases verified:**
- Text-only streaming: `liveWrite=true`, all content goes to writer + buffer
- Tool-call streaming: `liveWrite=false` after first tool delta, content still
  accumulated in buffer
- Empty stream: handled by fallback removal (4.4)
- Error in stream: propagated via `chunk.Error`

### 4.3 Fix Idempotency Key for Retries

**Objective:** Make idempotency key stable across retries of the same payload.

**Design:**

```go
func (c *OpenAICompat) newRequest(ctx context.Context, req Request) (*http.Request, error) {
    // ... marshal payload ...
    key := sha256.Sum256(raw)
    httpReq.Header.Set("Idempotency-Key", fmt.Sprintf("mivia-%x", key[:]))
    // Remove: c.requestSeq.Add(1) from the key
    // ...
}
```

**Remove:** `requestSeq atomic.Uint64` field from `OpenAICompat` struct
(only used for idempotency key).

**Tradeoff:** Some providers may intentionally want unique keys per request.
For those, idempotency headers are advisory anyway — a stable content-addressed
key is safe because retries should produce the same result. If a provider
rejects duplicate keys, the error is non-retryable (4xx) and will surface
cleanly.

**Test update:** `TestChatTurnIdempotencyKeyIsStablePerRequestAndUniqueAcrossRequests`
must be renamed and updated:
- Same request body → same idempotency key
- Different request body → different idempotency key
- Two identical requests sent separately → same key (not unique)

### 4.4 Remove Recursive Empty-Stream Fallback

**Objective:** Return an error from the adapter on empty stream instead of
silently retrying with a non-stream call. The agent loop should decide retry
policy.

**Design:**

```go
// In chatTurnStream, replace:
if content == "" && len(toolCalls) == 0 {
    req.Stream = false
    req.StreamWriter = nil
    return c.ChatTurn(ctx, req) // recursive
}
// With:
if content == "" && len(toolCalls) == 0 {
    return nil, fmt.Errorf("%s: stream returned zero content", c.name)
}
```

**Same change in readStream** (though this function is deleted in 4.2).

**Agent loop impact:** `loop.go:runStep()` receives an error from
`l.Completer.ChatTurn()`. The loop currently returns errors to the user
immediately. If empty-stream retry is desired, it should be explicit:

```go
// loop.go — optional retry with context deadline
const maxEmptyRetries = 1
for retry := 0; retry <= maxEmptyRetries; retry++ {
    resp, err := l.Completer.ChatTurn(heartbeat, req)
    if err != nil {
        if strings.Contains(err.Error(), "stream returned zero content") && retry < maxEmptyRetries {
            continue // retry once
        }
        return "", false, err
    }
    // ... process resp ...
}
```

This makes the retry policy **explicit and bounded**, eliminating the
silent infinite-recursion risk.

### 4.5 Consolidate Thin Wrappers → Config Helpers

**Objective:** Eliminate `deepseek.go` and `openrouter.go` as separate files
by folding their logic into the registration calls (4.1). If a provider
needs behavioral divergence later, it gets its own file then.

**Design:**
- Delete `deepseek.go` (or reduce to registration + empty constructor)
- Delete `openrouter.go` (or reduce to registration + empty constructor)
- Registration lives in a new `registration.go` or is inline in `provider.go`

**Helper for OpenAI-compatible providers:**

```go
func NewOpenAICompatProvider(info ProviderInfo) Constructor {
    return func(opts Options) (Completer, error) {
        base := opts.BaseURL
        if base == "" {
            base = info.DefaultURL
        }
        referer := opts.HTTPReferer
        if referer == "" {
            referer = info.ExtraHeaders["HTTP-Referer"]
        }
        title := opts.XTitle
        if title == "" {
            title = info.ExtraHeaders["X-Title"]
        }
        return NewOpenAICompat(info.Name, base, opts.APIKey, referer, title), nil
    }
}
```

### 4.6 Token Estimation (Research Decision — Phase 3)

**Recommendation:** **Option C (pragmatic) for now, with Option A as planned
improvement.**

| Option | Approach | Effort | Accuracy | Dependency |
|--------|----------|--------|----------|------------|
| A | tiktoken-go per model family | 2-3 days | ~95% | +1 dep (~500KB) |
| B | Improved heuristic (char-class) | 1 day | ~60-75% | None |
| **C** | **Current heuristic + padding config** | **0.5 day** | **~50%** | **None** |
| D | Model-aware via provider registry | 3-5 days | ~95% | +1 dep per family |

**Phase 3 deliverable:**
1. Accept current heuristic in the short term
2. Add `token_budget_padding` to config (default 1.3 = 30% safety margin)
3. Plan tiktoken-go integration as a follow-up PR

**Chosen approach rationale (from sub-agent research):**
- `tiktoken-go` is mature but adds ~500KB to binary for token lookup tables
- Pruning is approximate anyway (message boundaries are discrete)
- A padding factor compensates for the heuristic's systematic underestimation
- True accuracy matters more for context overflow prevention than for memory
  retention — and padding handles overflow

### 4.7 Retry Options in TOML Config

**Objective:** Expose retry configuration to users via TOML. Thread through
config → Options → OpenAICompat construction.

**TOML Schema:**

```toml
[provider.retry]
max_retries = 5           # default 3
base_delay_ms = 500       # default 200
max_delay_ms = 10000      # default 5000
```

**Config changes:**

```go
// config/types.go — add to File or ProviderConfig
type RetryConfig struct {
    MaxRetries   *int `toml:"max_retries"`
    BaseDelayMs *int `toml:"base_delay_ms"`
    MaxDelayMs  *int `toml:"max_delay_ms"`
}

// Option A: Nest under ProviderSection
type ProviderSection struct {
    Name    string       `toml:"name"`
    EnvFile string       `toml:"env_file"`
    Retry   RetryConfig  `toml:"retry"`
}

// Or Option B: Top-level [retry] section
type File struct {
    Provider  ProviderSection           `toml:"provider"`
    Retry     RetryConfig               `toml:"retry"`
    // ...
}
```

**Recommendation:** **Option A** (scoped under `[provider]`), because retries
are provider-specific behavior. If multi-provider support is added later,
each provider could have its own retry config.

**Threading through:**

```go
// config.Resolved gains:
type Resolved struct {
    // ...
    RetryOptions *RetryConfig
}

// provider.Options gains:
type Options struct {
    // ...
    Retry *retryOptions
}

// provider.New uses:
opts.Retry = res.RetryOptions // if set
return info.Constructor(opts)
```

**Backward compatibility:** Nil/zero = use defaults (no behavior change).

### 4.8 Circuit Breaker (Deferred)

**Decision:** Deferred to Phase 3 or later. Rationale:

- CLI tool sessions are typically minutes, not hours
- `retryRoundTripper` already handles transient failures (429, 503, 5xx)
- Users can Ctrl-C and retry manually
- Circuit breaker adds non-trivial complexity (half-open state, cooldown timer,
  metrics) for marginal gain in a CLI context

**If implemented:** Use a token-bucket or sliding-window circuit breaker
around the HTTP transport. Consider `github.com/sony/gobreaker`.

### 4.9 Completer Interface Extension (Design Note)

**Assumption:** All current and near-future providers use an OpenAI-compatible
chat completions endpoint (POST `/v1/chat/completions` with `messages` array).

**When this breaks:**
- Anthropic Messages API (different endpoint, different message format)
- Google Gemini API (streaming via gRPC or different SSE format)
- Ollama (custom JSON protocol)
- AWS Bedrock (signed requests, different API shape)

**Extension path:**
1. New struct implementing `Completer` (e.g., `AnthropicCompleter`)
2. No changes to `Completer` interface needed — the interface is generic enough
3. New constructor in registry: `Register(ProviderInfo{Name: "anthropic", Constructor: NewAnthropic})`
4. Provider-specific config resolution in `config/load.go` via the new
   `resolveProviderDefaults()` from 4.1

**Potential interface evolution:**
```go
type Completer interface {
    Name() string
    Chat(ctx context.Context, req Request) (string, error)
    ChatStream(ctx context.Context, req Request, w io.Writer) (string, error)
    ChatTurn(ctx context.Context, req Request) (*Response, error)
}
```
The `Request` struct would need to become more flexible (or accept a
provider-specific payload wrapper). For now, no change needed.

---

## 5. Phase Plan & Dependencies

```
Phase 1 (Structural)
├── 4.1 Provider Registry Pattern
│   ├── Dependency: None (self-contained)
│   ├── Files: provider.go, deepseek.go, openrouter.go, config/load.go
│   └── Notes: Replace both switches. Init() vs explicit wiring decision.
├── 4.2 Unify SSE Streaming
│   ├── Dependency: 4.4 recommended but not required
│   ├── Files: openai_compat.go, openai_compat_stream.go
│   └── Notes: Delete readStream(), route all through chatTurnStream()
└── 4.3 Fix Idempotency Key
    ├── Dependency: None
    ├── Files: openai_compat.go, test files
    └── Notes: Remove requestSeq. Update tests.

Phase 2 (Cleanup/Config)
├── 4.4 Remove Recursive Empty-Stream Fallback
│   ├── Dependency: 4.2 (readStream deletion makes it cleaner)
│   ├── Files: openai_compat_stream.go, openai_compat.go, agent/loop.go
│   └── Notes: Add explicit bounded retry in loop.go
├── 4.5 Consolidate Thin Wrappers
│   ├── Dependency: 4.1 (registry replaces need for separate files)
│   ├── Files: deepseek.go, openrouter.go (delete or reduce)
│   └── Notes: Registration moves to registration.go
└── 4.7 Retry Options in TOML Config
    ├── Dependency: 4.1 (Options struct changes), 4.5 (OpenAICompat construction)
    ├── Files: config/types.go, config/load.go, config/defaults.go,
    │          provider/provider.go, provider/openai_compat.go
    └── Notes: Backward compatible (nil=defaults)

Phase 3 (Quality/Deferred)
├── 4.6 Token Estimation (+ padding config)
│   ├── Dependency: None
│   ├── Files: provider/context.go, config/types.go (padding)
│   └── Notes: Padding first, tiktoken-go follow-up
└── 4.8 Circuit Breaker (deferred)
    ├── Dependency: None
    ├── Files: provider/retry.go (new transport wrapper)
    └── Notes: Only if user demand requires it
```

**Recommended execution order:** Phase 1 items are independent and can be
done in parallel. Within Phase 2, 4.4 depends on 4.2, and 4.5 depends on 4.1.
Phase 3 is independent.

---

## 6. Risk Matrix

| # | Change | Risk | Probability | Impact | Mitigation |
|---|--------|------|-------------|--------|------------|
| R1 | Registry breaks config resolution | Missing defaults for existing providers | Low | High | Unit test all registered providers produce correct defaults |
| R2 | SSE unification has edge case | Different behavior for no-tools streaming | Low | Medium | Existing streaming tests must pass unchanged |
| R3 | Idempotency key change breaks provider | Provider rejects duplicate keys | Very Low | Low | Key is advisory for all major providers; 4xx error surfaces cleanly |
| R4 | Empty-stream removal breaks provider | Legitimate empty-stream then non-stream response lost | Low | Medium | Add 1-retry in agent loop as safety net |
| R5 | Retry config threading misses path | Config option has no effect | Low | Low | Integration test with httptest server |
| R6 | Registry test pollution | `init()` registration conflicts with test mocks | Medium | Medium | Use explicit `Register()` in test setup/teardown; avoid `init()` for mock providers |
| R7 | Breaking existing user config | TOML schema change incompatible | Very Low | Low | Backward-compatible parsing (nil = defaults) |

---

## 7. Testing Strategy

### Must-Pass (zero change to test logic)

All existing tests in:
- `internal/provider/openai_compat_test.go` (13 tests)
- `internal/provider/retry_test.go` (8 tests)
- `internal/provider/context_test.go` (15+ tests)

**Exception:** `TestChatTurnIdempotencyKeyIsStablePerRequestAndUniqueAcrossRequests`
must change assertion semantics (key is now stable for same payload, not unique).

### New Tests Required

| Test | Coverage | Phase |
|------|----------|-------|
| `TestRegistryHappyPath` | Known provider constructs correctly | 1 |
| `TestRegistryUnknownProvider` | Unknown provider returns error | 1 |
| `TestRegistryDuplicatePanics` | Double-register panics | 1 |
| `TestConfigResolvesDefaultsFromRegistry` | Config picks up defaults from provider info | 1 |
| `TestChatStreamRoutesThroughChatTurn` | ChatStream calls same code path as ChatTurn stream | 1 |
| `TestIdempotencyKeyStablePerPayload` | Same body = same key, different body = different key | 1 |
| `TestEmptyStreamReturnsError` | Empty stream is error, not silent retry | 2 |
| `TestLoopRetriesEmptyStreamOnce` | Agent loop retries empty stream once (if implemented) | 2 |
| `TestRetryConfigThreadsThrough` | TOML retry config reaches transport | 2 |
| `TestRetryDefaultsBackwardCompat` | No config = same behavior as before | 2 |

### Integration Tests

```go
// TestNewWithHTTPServer — end-to-end with httptest server per provider flavor
func TestNewDeepSeekWithHTTPServer(t *testing.T) {
    srv := httptest.NewServer(...)
    defer srv.Close()
    // Mock config pointing to srv.URL
    res := &config.Resolved{ProviderName: "deepseek", BaseURL: srv.URL, ...}
    c, err := New(res)
    // ... test ChatTurn ...
}
```

### Registry Test Pattern (avoiding init() conflict)

```go
// In test file, use a fresh registry per test:
func TestRegistryHappyPath(t *testing.T) {
    defer resetRegistry() // saves and restores global registry
    Register(ProviderInfo{Name: "test-provider", Constructor: ...})
    // ...
}

func resetRegistry() func() {
    saved := registry
    registry = make(map[string]ProviderInfo)
    return func() { registry = saved }
}
```

---

## 8. Files Changed Summary

| File | Change | Phase |
|------|--------|-------|
| `internal/provider/provider.go` | Add `ProviderInfo` type, `Register()`, `New()` registry lookup, `KnownProviders()`, `LookupInfo()` | 1 |
| `internal/provider/deepseek.go` | Simplify to registration call or delete | 1, 2 |
| `internal/provider/openrouter.go` | Simplify to registration call or delete | 1, 2 |
| `internal/provider/registration.go` | **NEW** — all provider registrations | 1 |
| `internal/provider/openai_compat.go` | Delete `readStream()`, fix idempotency key, route `ChatStream()` through `ChatTurn()` | 1 |
| `internal/provider/openai_compat_stream.go` | Remove empty-stream fallback, simplify | 2 |
| `internal/provider/context.go` | (Phase 3) Add padding factor or tokenizer | 3 |
| `internal/config/types.go` | Add `RetryConfig` to `ProviderSection`, add `TokenBudgetPadding` | 2, 3 |
| `internal/config/defaults.go` | Remove provider-specific constants (now in registry) | 1 |
| `internal/config/load.go` | Replace `resolveProvider()` switch with registry lookup | 1 |
| `internal/agent/loop.go` | Optional: explicit empty-stream retry | 2 |
| `internal/provider/openai_compat_test.go` | Update idempotency test, add registry tests | 1 |
| `internal/provider/retry_test.go` | Add retry config threading test | 2 |
| `internal/config/load_test.go` | Update for registry-based defaults | 1 |

---

## 9. Open Questions

1. **Init vs explicit wiring?** Go standard library uses `init()` + blank import
   (`database/sql`). This pattern is well-understood but adds implicit coupling.
   Decision: Use `init()` in `registration.go` for production, with
   `resetRegistry()` helper for tests.

2. **Config defaults import cycle?** `config/load.go` would need to call
   `provider.LookupInfo()`. Does this create a cycle (`config` → `provider` → `config`)?
   No — `provider` only references `config` for constants (which move to registry).
   `config` references `provider` for `LookupInfo()`. This is a valid dependency
   direction: config → provider.

3. **DeepSeek Pro model?** `DeepSeekProModel` exists in `defaults.go` but is not
   referenced in the factory. Should it be a separate provider entry or a model
   override? Keep as model override (user sets `model = "deepseek-v4-pro"`).

4. **Multiple provider instances?** Currently one active provider at a time. If
   multi-provider support is needed later, the registry becomes a map of
   `ProviderInfo` + instance state. Not needed now.

5. **Non-OpenAI completion endpoint?** Some providers use `/v1/completions` instead
   of `/v1/chat/completions`. The `OpenAICompat` struct hardcodes the chat path.
   A future non-chat provider would need its own struct.

---

## 10. Appendix: Sub-Agent Validation Results

### Sub-Agent 1: Registry Pattern & Architecture

**Key findings:**
- ✅ Registry pattern is sound (mirrors `http.HandleFunc`, `database/sql`)
- ⚠️ Config package has its own parallel switch — registry must carry defaults to replace it
- ⚠️ `init()` registration can cause test pollution — use `resetRegistry()` pattern
- ⚠️ The `ProviderDefaults` approach may be simpler than a full constructor registry
- ✅ No circular import risk (config → provider is valid direction)

**Challenge incorporated:** Added `ProviderInfo` struct with defaults for config
resolution (§4.1). Added `resetRegistry()` test pattern (§7).

### Sub-Agent 2: SSE Streaming & Idempotency

**Key findings:**
- ✅ SSE paths are functionally identical — unification is safe
- ✅ The `liveWrite` flag in `readTurnStream` correctly gates tool delta visibility
- ⚠️ `readStream()` uses slightly different error handling (returns `full.String()` on error vs not)
- ✅ Idempotency change is safe — all major providers treat it as advisory
- ⚠️ Empty-stream fallback exists for a reason (some providers send empty first chunk) — handle with limited retry

**Challenge incorporated:** Empty-stream removal adds limited retry in agent loop
(§4.4). Unified SSE preserves all error paths correctly.

### Sub-Agent 3: Token Estimation

**Key findings:**
- ✅ `tiktoken-go` is available and mature (`github.com/pkoukk/tiktoken-go`)
- ⚠️ Adds ~500KB to binary size for encoding tables
- ⚠️ Provider-aware tokenization requires parallel registry (which tokenizer for which model)
- ✅ Current heuristic systematically underestimates code tokens (bad for this agent)
- ✅ Best short-term fix: padding config factor
- ⚠️ Pruning is discrete (message boundaries) — exact token count matters less than getting the right messages

**Recommendation accepted:** Phase 3 with padding factor first, tiktoken-go
as separate PR (§4.6).

### Sub-Agent 4: Config & Retry Design

**Key findings:**
- ✅ Thin wrappers are identical — consolidation is safe
- ⚠️ OpenRouter's `HTTP-Referer` and `X-Title` are specific to OpenRouter's tracking — generic solution must preserve them
- ✅ Retry TOML schema is clean and backward compatible
- ⚠️ Circuit breaker not needed for CLI — confirmed
- ✅ Test strategy: 5 tests break with idempotency change, 1 test breaks with SSE unification
- ⚠️ Missing tests: error path for empty stream, registry conflict, config fallthrough

**Challenge incorporated:** OpenRouter headers preserved in `ExtraHeaders` map
(§4.5). Test impact documented (§7).

### Sub-Agent 5: Plan Quality & Completeness

**Key findings:**
- ✅ Overall structure is well-organized for implementation
- ⚠️ Missing: dependencies between changes (added §5)
- ⚠️ Missing: rollback strategy (added each item has backward-compatible path)
- ⚠️ Missing: documentation updates needed (docs/product/agent.md for new providers)
- ⚠️ Risk matrix needed (added §6)
- ⚠️ Completer interface extension path should be documented (added §4.9)

**Improvements incorporated:**
- Added dependency graph (§5)
- Added risk matrix (§6)
- Added Completer interface extension design note (§4.9)
- Added files changed summary (§8)
- Added open questions (§9)
