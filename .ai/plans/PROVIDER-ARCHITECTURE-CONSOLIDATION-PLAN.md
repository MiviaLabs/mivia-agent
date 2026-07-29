# Plan: Provider Architecture Consolidation (Phase 1)

**Status:** Draft v2 — hostile challenged; implementation blocked pending topology decisions
**Scope:** Refactor the provider adapter architecture BEFORE adding any new providers (ZAI).
**Goal:** Eliminate provider dispatch switches, add narrowly-scoped OpenAI-compatible hooks, fix error handling, and establish adapter coverage before adding ZAI.

> **v2 correction:** The former “verified, consolidated” status was not justified. Three independent hostile reviews plus local inspection found a compile-blocking import cycle and incomplete API, streaming, migration, and ADLC contracts. The v2 decisions below supersede conflicting v1 text; no implementation is authorized until the topology and task validators pass.

### v2 binding decisions

1. **Dependency boundary:** Do not make `config` import `provider`. `provider` currently imports `config` through `provider.New(*config.Resolved)`, so `config → provider → config` is invalid. Create `internal/providerregistry` with only neutral metadata (`Descriptor{Name, DefaultModel, DefaultURL, DefaultAPIKeyEnv}`, `Lookup`, and sorted `Names`). `config.resolveProvider` consumes that package. `provider` owns a private factory table and explicit built-in registration keyed by the same names; it must not expose a mutable map.
2. **Registration lifecycle:** Remove all `init()` registration examples. Use controlled explicit registration guarded by `sync.Once`; duplicate names return errors, names are deterministic/sorted, and tests cannot mutate global production state.
3. **Compatibility:** Add the `CompatOptions` constructor while retaining deprecated wrappers for the existing exported five-argument constructors. Migrate every repository caller found by `rg`; removal is a separate breaking-change plan, not Wave 4.
4. **Config scope:** This phase keeps the existing common TOML fields and explicitly defers arbitrary provider-specific TOML. `ConfigFields` and `Options.Extra` are removed from this phase unless a separate typed TOML representation, validation, and conversion path is designed.
5. **Hook contract:** `ExtraHeaders` cannot override `Authorization`, `Content-Type`, `Accept`, or `Idempotency-Key`; `ExtraBody` cannot override `model`, `messages`, or `stream`. Clone maps, reject collisions, and test both success and failure. `ErrorParser` receives bounded raw bodies and applies to non-2xx, HTTP-200 error envelopes, and both SSE paths.
6. **Streaming contract:** Add reasoning/search fields to streaming deltas and define accumulation/return behavior for `readStream` and `readTurnStream`, including mixed content/tool-call/reasoning chunks. This phase transports fields only; ZAI rendering/citations and URL auto-detection remain in the ZAI plan.

---

## §1 Diagnosis

### What's Good (keep)

| Component | Why |
|-----------|-----|
| `Completer` interface (Name, ChatStream, Chat, ChatTurn) | Clean 4-method contract |
| `Request` / `Response` / `Message` / `ToolCall` types | Stable, OpenAI-compatible |
| `OpenAICompat` core (HTTP, auth, retry, streaming, idempotency) | Battle-tested, good test coverage |
| `retryRoundTripper` | Works, tested |
| `context.go` (token estimation, pruning) | Unrelated to dispatch, fine |
| TOML config shape (`[providers.xxx]` blocks) | Clean user-facing API |
| Env file resolution | Standard pattern |

### What's Broken (fix now)

| # | Problem | Files | Impact |
|---|---------|-------|--------|
| P1 | **Hardcoded switch in `provider.New()`** | `provider/provider.go:100-112` | Every new provider edits this file |
| P2 | **Hardcoded switch in `resolveProvider()`** | `config/load.go:130-148` | Duplicate dispatch, must stay in sync |
| P3 | **`NewOpenAICompat` 5 positional string params** | `provider/openai_compat.go:22` | Adding 6th param breaks all callers |
| P4 | **Error format hardcoded to OpenAI `{error:{}}` shape** | `provider/openai_compat.go:90-94` | ZAI errors silently produce "empty choices" |
| P5 | **No per-provider hooks (headers, request mods)** | `provider/openai_compat.go` | ZAI needs Accept-Language, error intercept |
| P6 | **`Options` struct has OpenRouter-specific fields** | `provider/provider.go:80-88` | HTTPReferer, XTitle pollute shared type |
| P7 | **`ProviderConfig` is flat; provider-specific TOML fields pollute** | `config/types.go:45-52` | No clean place for ZAI-specific config |
| P8 | **`KnownProviders` dead code** | `config/defaults.go:75` | Only used in error messages, not extensible |
| P9 | **Provider metadata scattered across 3+ files** | `config/defaults.go`, `provider/deepseek.go`, `provider/openrouter.go` | Per-provider constants in config/, factories in provider/ |
| P10 | **Zero adapter-level tests** | `provider/` tests | No safety net for refactoring |

---

## §2 Design Decisions (challenged)

### D1: Provider Registry — neutral metadata plus explicit factory registry

**Rejected:** Full plugin system and package-level `init()` registration (invisible side-effects, hard to test).

**Adopted in v2:** Metadata is owned by the dependency-neutral `internal/providerregistry` package. Provider factories are private provider-package state populated by explicit controlled registration. `config` must never import `provider`.

```go
// internal/providerregistry/registry.go
type Descriptor struct {
    Name             string
    DefaultModel     string
    DefaultURL       string
    DefaultAPIKeyEnv string
}
```

Each adapter contributes a factory through explicit registration:
```go
// provider/provider.go — called through controlled bootstrap
func registerBuiltins() error {
    return registerProvider("deepseek", func(opts Options) (Completer, error) {
        return NewOpenAICompat(CompatOptions{
            Name: "deepseek", BaseURL: opts.BaseURL, APIKey: opts.APIKey,
        }), nil
    })
}
```

**Result:** `provider.New()` performs private factory lookup; `resolveProvider()` performs neutral metadata lookup. Adding a provider requires one metadata entry and one explicit factory registration, with no dispatch switches or package cycle.

### D2: CompatOptions struct — NOT functional options

**Rejected:** Functional options pattern (`type Option func(*OpenAICompat)`) — harder to introspect, serialize, grep.

**Adopted:** Single `CompatOptions` struct is added; old exported constructors remain deprecated wrappers during this phase. Includes hooks:

```go
type CompatOptions struct {
    Name        string
    BaseURL     string
    APIKey      string
    HTTPReferer string   // zero-value = not set (unlike OpenRouter's current "" = "use default")
    XTitle      string   // same

    // Extensibility hooks (nil = default behavior)
    ExtraHeaders  map[string]string  // protected merge into every request
    ExtraBody     map[string]any      // protected top-level JSON merge
    ErrorParser   func(statusCode int, body []byte) error // nil = default parser
}
```

### D3: ErrorParser hook — NOT per-provider HTTP clients

The shared request pipeline (retry, streaming, tool-call assembly) is identical for all OpenAI-compatible providers. Only error format differs. A single function hook is minimal.

- **ZAI** provides: `ErrorParser: zaiErrorParser` that detects flat `{"code":N,"message":"..."}` format
- **DeepSeek/OpenRouter** leave nil (use default OpenAI `{error:{message,type,code}}` parsing)

### D4: ExtraHeaders — NOT transport wrapper

**Rejected:** Custom `http.RoundTripper` per provider (opaque, hard to debug).
**Adopted:** `ExtraHeaders map[string]string` merged into every request by `newRequest()`.

- ZAI registers: `ExtraHeaders: {"Accept-Language": "en-US,en"}`
- Others leave nil

### D5: Config consolidation — provider metadata in one place

**Rejected:** Constants scattered in `config/defaults.go` AND string literals in adapter files.
**Adopted:** Provider metadata lives in the `ProviderDescriptor` in each adapter file. `config/defaults.go` keeps only:
- `DefaultProvider` ("deepseek")
- `DeepSeekProModel` (a model-level concept, not provider-level)

The `resolveProvider()` switch is replaced by a lookup into `providerregistry.Lookup(name)`.

### D6: ZAI-specific shared type hooks (first-class, not deferred)

The ZAI adapter requires four shared-type extensions that must be part of the consolidation, not patched in later. These are **required by the ZAI provider spec** (see ZAI-GLM-PROVIDER-ADAPTER-PLAN.md §6).

| Hook | Shared type affected | Why |
|------|---------------------|-----|
| `ExtraBody map[string]any` | `CompatOptions` in `openai_compat.go` | ZAI `thinking` request param |
| `ReasoningContent string` | `Response` + `chatResponseBody.Message` in `provider.go` + `openai_compat.go` | ZAI `reasoning_content` response field |
| `WebSearch []WebSearchResult` | `Response` + `chatResponseBody.Message` in `provider.go` + `openai_compat.go` | ZAI `web_search` response array |
| `WebSearchResult` struct | `provider.go` (new type) | ZAI web search result shape |

```go
// provider/provider.go — additions to Response
type WebSearchResult struct {
    Title       string `json:"title"`
    Content     string `json:"content"`
    Link        string `json:"link"`
    Media       string `json:"media"`
    Icon        string `json:"icon"`
    Refer       string `json:"refer"`
    PublishDate string `json:"publish_date"`
}

type Response struct {
    Content          string
    ReasoningContent string          // ZAI thinking trace
    ToolCalls        []ToolCall
    FinishReason     string
    WebSearch        []WebSearchResult // ZAI web search results
}
```

```go
// provider/openai_compat.go — additions to CompatOptions
type CompatOptions struct {
    Name        string
    BaseURL     string
    APIKey      string
    HTTPReferer string
    XTitle      string
    ExtraHeaders map[string]string    // per-request header injection
    ExtraBody    map[string]any       // top-level JSON body injection
    ErrorParser  func(int, []byte) error
}
```

```go
// provider/openai_compat.go — additions to chatResponseBody.Message
type chatResponseBody struct {
    Choices []struct {
        Message struct {
            Content          string            `json:"content"`
            ReasoningContent string            `json:"reasoning_content"` // ZAI thinking trace
            ToolCalls        []ToolCall        `json:"tool_calls"`
            WebSearch        []WebSearchResult `json:"web_search"`        // ZAI search results
        } `json:"message"`
        // ... rest unchanged
    } `json:"choices"`
    Error *struct {
        Message string `json:"message"`
        Type    string `json:"type"`
        Code    any    `json:"code"`
    } `json:"error"`
}
```

**Rationale for making these first-class:** These fields are zero-value-safe (empty string, nil slice) for providers that don't use them. DeepSeek and OpenRouter are unaffected — their responses simply have `ReasoningContent=""` and `WebSearch=nil`. Adding them to the shared types now avoids forking the shared client later.

---

## §3 Implementation Plan (ADLC micro-task waves)

### Wave 0: Topology and contract lock (no implementation)

- Create the exact dependency-neutral `internal/providerregistry` API described above.
- Enumerate all constructor and constant callers with `rg`; include production and integration tests.
- Split the work into one-file tasks, with a RED test task before every production task and a reviewer after each 2–3 production tasks.
- Run ADLC Step 0 hostile challenge (2–4 agents), Step 2 one validator per wave, and Step 3 finalization. Any rejected validator returns the task list to Step 0/1.

### Wave 1: CompatOptions + hooks + shared type extensions

**Files to modify:**
- `internal/provider/provider.go` — add `WebSearchResult` struct, add `ReasoningContent string` and `WebSearch []WebSearchResult` to `Response` struct
- `internal/provider/openai_compat.go` — add `CompatOptions` struct with `ExtraHeaders`, `ExtraBody`, `ErrorParser` fields; update `chatResponseBody.Message` to include `ReasoningContent` and `WebSearch` fields; update `NewOpenAICompat` to accept `CompatOptions` instead of 5 strings; update `newRequest()` to merge `ExtraHeaders` and `ExtraBody`; update `doJSON()` and `ChatTurn()` to copy `ReasoningContent` and `WebSearch` into `Response`; update `httpError()` to call `ErrorParser` hook
- `internal/provider/openai_compat_stream.go` — update `applyStreamChunk` to use `ErrorParser` hook for stream error parsing; update `readTurnStream` to propagate `ReasoningContent` and `WebSearch` through stream assembly
- `internal/provider/openai_compat_test.go` — update all callers of `NewOpenAICompat` to use `CompatOptions{}`; add tests for `ExtraHeaders` merge, `ExtraBody` merge, `ErrorParser` hook, `ReasoningContent` propagation, `WebSearch` propagation

### Wave 2: Neutral metadata and factory registry (eliminate switches)

**Files to create:**
- `internal/providerregistry/registry.go` — neutral descriptor table, `Lookup`, sorted `Names`; no imports of `config` or `provider`.

**Files to modify:**
- `internal/provider/provider.go` — add a private factory registry and explicit built-in registration; replace `New()` switch without introducing a config/provider import cycle.
- `internal/provider/deepseek.go` — construct with `CompatOptions` and neutral descriptor values.
- `internal/provider/openrouter.go` — preserve configured `HTTPReferer`/`XTitle` while mapping them into `CompatOptions`.
- `internal/config/defaults.go` — remove per-provider constants only after all production/test callers migrate; keep justified aliases if compatibility requires them.
- `internal/config/load.go` — use `providerregistry.Lookup` for defaults and `Names` for deterministic supported-provider errors.

**Test:** RED then GREEN tests for neutral metadata lookup, duplicate registration errors, deterministic names, provider factory dispatch, and config wiring. No test may mutate shared production registries.

### Wave 3: Adapter-level tests

**Files to create:**
- `internal/provider/deepseek_test.go` — test `NewDeepSeek` with empty/explicit BaseURL, test dispatch via `Providers`
- `internal/provider/openrouter_test.go` — test `NewOpenRouter` with empty/explicit BaseURL, test HTTPReferer/XTitle defaults

**Files to modify:**
- `internal/provider/pipeline_integration_test.go` — add integration test that wires `config.Load` (with test TOML) → `provider.New` → `ChatTurn` against httptest server
- `internal/config/provider_integration_test.go` — add test that `resolveProvider` resolves defaults correctly via registry

### Wave 4: Options cleanup (deferred breaking removal)

**Files to modify:**
- `internal/provider/provider.go` — remove only fields proven redundant after the OpenRouter mapping tests; do not add an untyped `Extra` configuration escape hatch.
- `internal/provider/deepseek.go` — use `CompatOptions` directly; remove dependency on `config.DeepSeek*` constants
- `internal/provider/openrouter.go` — use `CompatOptions` directly; remove dependency on `config.OpenRouter*` constants
- `internal/provider/openai_compat.go` — ensure `NewOpenAICompatWithRetry` also uses `CompatOptions`
- `internal/provider/retry_test.go` — update callers of `NewOpenAICompatWithRetry`

**Do not remove:** exported constructor compatibility wrappers. Removing them requires a separate approved breaking-change plan.

### Wave 5: Verification gates

All must pass:
```
go build ./...
go test ./internal/provider/... -race -count=1
go test ./internal/config/... -race -count=1
go test ./internal/agent/... -race -count=1
go vet ./...
make verify
make race
```

---

## §4 Files Changed (summary)

| File | Action | Wave |
|------|--------|------|
| `internal/provider/openai_compat.go` | Add `CompatOptions`, `ExtraHeaders`, `ErrorParser` | W1 |
| `internal/provider/openai_compat_stream.go` | Use `ErrorParser` hook | W1 |
| `internal/provider/openai_compat_test.go` | Update callers, add tests | W1 |
| `internal/provider/provider.go` | Add `ProviderDescriptor`, `Providers` map, update `New()` | W2 |
| `internal/provider/deepseek.go` | Add registration; remove config dependency | W2+W4 |
| `internal/provider/openrouter.go` | Add registration; remove config dependency | W2+W4 |
| `internal/config/defaults.go` | Remove per-provider constants, `KnownProviders` | W2 |
| `internal/config/load.go` | Replace switch with registry lookup | W2 |
| `internal/provider/deepseek_test.go` | New file | W3 |
| `internal/provider/openrouter_test.go` | New file | W3 |
| `internal/provider/pipeline_integration_test.go` | Add full-stack integration test | W3 |
| `internal/config/provider_integration_test.go` | Add registry-backed resolve test | W3 |
| `internal/provider/retry_test.go` | Update callers | W4 |

**Total: ~13 files modified/created, ~400-500 LOC added, ~50 LOC removed**

**Inventory correction:** the v1 count is incomplete. Constructor migration also covers `internal/agent/loop_integration_test.go`, `internal/subagents/subagent_integration_test.go`, `internal/storage/store_agent_integration_test.go`, and `internal/chat/session_agent_integration_test.go`. Constant migration also covers `internal/cli/doctor.go`. Re-run `rg` immediately before implementation and treat its output as authoritative.

---

## §5 Hostile Challenge Dispositions

The architecture findings were challenged by 2 independent hostile agents. Below are consolidated dispositions:

| Finding | Challenge | Disposition |
|---------|-----------|-------------|
| P1-P2: Switches are bad | "2 providers is fine; 3+ is when it hurts. YAGNI?" | **UPHELD.** 3rd provider (ZAI) is imminent. Fix before it arrives. Cost of registry is ~30 LOC. |
| P3: 5 positional params | "Go-idiomatic. Just add more params." | **UPHELD (modified).** Replace with struct. 5 params is already unwieldy; adding 6th is the last straw. |
| P4: Error format | "ZAI might actually match OpenAI. Verify." | **UPHELD.** ZAI docs confirm `{code, message}` format, NOT `{error: {...}}`. Verified by fetching docs.z.ai. |
| P5: No hooks | "Middleware is over-engineering. Just add fields." | **UPHELD (modified).** Use explicit fields on `CompatOptions`, not middleware chain. |
| P6-P7: OpenRouter fields in shared types | "HTTPReferer and XTitle are generic HTTP concepts." | **UPHELD (modified).** They stay as zero-value-safe fields on `CompatOptions`, not on `Options`. |
| P8: KnownProviders dead code | "Remove it? Then how does user see supported providers?" | **UPHELD (revised).** Replace it with sorted names from `providerregistry.Names()`. |
| P9: Scattered metadata | "Constants file is fine — Go idiom." | **UPHELD (modified).** Keep only truly cross-cutting constants in config/; move per-provider defaults to the adapter file's descriptor. |
| P10: No tests | "8-line factories don't need tests." | **PARTIALLY UPHELD.** Factory constructors need tests only to prove they build the right `CompatOptions` — ~5 LOC each. Integration tests are more valuable. |
| Import cycle | Three independent reviews confirmed `config → provider → config` under the v1 registry. | **CONFIRMED BLOCKER.** Use `internal/providerregistry` for metadata; keep factories provider-owned. |
| Registration | v1 rejects `init()` but its example and Wave 2 require it. | **CONFIRMED.** Use explicit controlled registration, private state, duplicate errors, sorted names. |
| Hooks/streaming | `readStream`, SSE flat errors, delta metadata, and collision rules were omitted. | **CONFIRMED.** Expand Wave 1 tests and contracts before implementation. |
| ADLC readiness | Broad waves lacked RED sequencing and real Step 5 evidence. | **CONFIRMED.** This plan remains blocked until Steps 0–3 are completed for the implementation run. |

---

## §6 Open Questions (must resolve before Step 3 lock)

1. **Multimodal content** (`Message.Content` as string → array of parts). Cross-cutting change across all providers. Separate plan.

2. **Provider auto-discovery / dynamic registration.** Not needed. Explicit built-in registration is sufficient for foreseeable future.

3. **Provider-specific TOML.** Keep flat common TOML and defer arbitrary provider-specific fields, or define a typed representation and validation before adding them. `ConfigFields` alone is not a solution.
4. **Removing `config.DeepSeekName` etc.** Migrate all `rg` results, including `internal/cli/doctor.go`, or keep aliases with explicit compatibility rationale.

## §7 ZAI-specific hooks (transport scope only)

The following were originally marked "deferred" in ZAI-GLM-PROVIDER-ADAPTER-PLAN.md but are now first-class in the consolidation:

| Feature | Status | Hook provided by |
|---------|--------|-----------------|
| `thinking` request param | ✅ `ExtraBody map[string]any` on `CompatOptions` | Wave 1 |
| `reasoning_content` response | ✅ `ReasoningContent string` on `Response` + wire struct | Wave 1 |
| `web_search` response | ✅ `WebSearch []WebSearchResult` on `Response` + wire struct | Wave 1 |
| Base URL auto-detection | ⏳ Deferred | Separate ZAI adapter plan with explicit fallback ownership/tests |
| `Accept-Language` header | ✅ `ExtraHeaders map[string]string` on `CompatOptions` | Wave 1 |
| Error format intercept | ✅ `ErrorParser` func hook on `CompatOptions` | Wave 1 |

---

## §8 Post-Consolidation: Adding ZAI

Once Phase 1 is complete, adding ZAI is:

1. Create `internal/provider/zai.go` — adapter file + `Providers["zai"]` registration with `ExtraHeaders: {"Accept-Language": "en-US,en"}`, `ExtraBody: {"thinking": {"type": "enabled"}}`, and `ErrorParser: zaiErrorParser`
2. Create `internal/provider/zai_test.go` — constructor, config wiring, error intercept E2E, `ReasoningContent`/`WebSearch` propagation tests, dual URL auto-detection tests
3. Add `[providers.zai]` section to `mivia.toml.example`

No registry/config/openai client changes should be required for ZAI after this phase; rendering/citations and URL auto-detection remain ZAI-plan work.

## §9 ADLC verification contract

- **Baseline:** run `GOCACHE=/tmp/mivia-go-cache go test ./internal/provider ./internal/config` and `go vet ./...`; report exact results.
- **Each wave:** RED assertion failure, GREEN focused pass, `go build ./...`, and affected-package `go test -race` before advancing.
- **Step 5:** dispatch 3–4 hostile bug auditors with partial results; disposition every finding, add targeted tests for rejected/uncertain findings, and repeat until zero confirmed bugs.
- **Final:** run `go build ./... && go vet ./... && go test -race ./...`, then `make verify`, `make race`, and `make secret-scan` where permitted. Never claim an unexecuted or environment-blocked check passed.
