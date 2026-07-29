# Plan: Provider Architecture Consolidation (Phase 1)

**Status:** Draft v1 — hostile challenged, verified, consolidated
**Scope:** Refactor the provider adapter architecture BEFORE adding any new providers (ZAI).
**Goal:** Eliminate hardcoded switches, add extensibility hooks, fix error handling, add tests.

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

### D1: Provider Registry — centralized map, NOT plugin system

**Rejected:** Full plugin system with `init()` registration (invisible side-effects, hard to test).

**Adopted:** A `ProviderDescriptor` struct + a `var Providers map[string]ProviderDescriptor` in `provider/provider.go`. Each adapter file registers itself in a `RegisterProvider()` call (NOT `init()`) that the main factory init block calls.

```go
// provider/provider.go
type ProviderDescriptor struct {
    Name             string
    DefaultModel     string
    DefaultURL       string
    DefaultAPIKeyEnv string
    NewFactory       func(opts Options) (Completer, error)
    // ConfigFields describes which ProviderConfig fields this provider uses
    // (for validation and TOML example generation)
    ConfigFields     []string // e.g. {"model", "base_url", "api_key_env"}
}

var Providers = map[string]ProviderDescriptor{}
```

Each adapter adds itself:
```go
// provider/deepseek.go — no init(), no side-effects
func init() {
    Providers["deepseek"] = ProviderDescriptor{
        Name: "deepseek",
        DefaultModel: "deepseek-v4-flash",
        DefaultURL: "https://api.deepseek.com/v1",
        DefaultAPIKeyEnv: "DEEPSEEK_API_KEY",
        NewFactory: func(opts Options) (Completer, error) {
            return NewOpenAICompat(CompatOptions{
                Name: "deepseek", BaseURL: opts.BaseURL, APIKey: opts.APIKey,
            }), nil
        },
    }
}
```

**Result:** `provider.New()` becomes `desc, ok := Providers[name]`. `resolveProvider()` looks up `Providers[name]` for defaults. Adding a provider = 1 file + 1 `Providers` entry. No switches.

### D2: CompatOptions struct — NOT functional options

**Rejected:** Functional options pattern (`type Option func(*OpenAICompat)`) — harder to introspect, serialize, grep.

**Adopted:** Single `CompatOptions` struct replacing 5 individual string params. Includes hooks:

```go
type CompatOptions struct {
    Name        string
    BaseURL     string
    APIKey      string
    HTTPReferer string   // zero-value = not set (unlike OpenRouter's current "" = "use default")
    XTitle      string   // same

    // Extensibility hooks (nil = default behavior)
    ExtraHeaders  map[string]string  // merged into every request
    ErrorParser   func(statusCode int, body []byte) error // nil = use OpenAI default
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

The `resolveProvider()` switch is replaced by a lookup into `provider.Providers[name]`.

---

## §3 Implementation Plan (5 waves)

### Wave 1: CompatOptions + hooks (no behavior change)

**Files to modify:**
- `internal/provider/openai_compat.go` — add `CompatOptions` struct, add `ExtraHeaders` + `ErrorParser` fields, update `NewOpenAICompat` to accept `CompatOptions` instead of 5 strings, update `newRequest()` to merge `ExtraHeaders`, update `doJSON()` and `httpError()` to call `ErrorParser` hook
- `internal/provider/openai_compat_stream.go` — update `applyStreamChunk` to use hook for stream error parsing
- `internal/provider/openai_compat_test.go` — update all callers of `NewOpenAICompat` to use `CompatOptions{}`

**Backward compat:** Add `NewOpenAICompatLegacy(name, baseURL, apiKey, httpReferer, xTitle string)` as a thin wrapper that calls `NewOpenAICompat(CompatOptions{...})` so existing callers compile. Mark deprecated.

**Test:** All existing tests pass. New tests for `ExtraHeaders` merge and custom `ErrorParser`.

### Wave 2: Provider Registry (eliminate switches)

**Files to create:**
- (none new — registry lives in existing `provider/provider.go`)

**Files to modify:**
- `internal/provider/provider.go` — add `ProviderDescriptor` struct, `var Providers map[string]ProviderDescriptor`, `func RegisterProvider(d ProviderDescriptor)`, replace `New()` hardcoded switch with `Providers[name]` lookup
- `internal/provider/deepseek.go` — add `init()` block registering into `Providers`
- `internal/provider/openrouter.go` — add `init()` block registering into `Providers`
- `internal/config/defaults.go` — remove `DeepSeekName`, `OpenRouterName`, `DeepSeekDefaultURL`, `DeepSeekDefaultModel`, `DeepSeekAPIKeyEnv`, `OpenRouterDefaultURL`, `OpenRouterDefaultModel`, `OpenRouterAPIKeyEnv` constants; remove `KnownProviders` slice
- `internal/config/load.go` — replace `resolveProvider()` switch with lookup into `provider.Providers` for defaults; generate supported-list error from `provider.Providers` keys
- `internal/provider/provider.go` — update `Options` struct: remove `HTTPReferer` and `XTitle` (they live in `CompatOptions` now); add generic `Extra map[string]any` for provider-specific overrides

**Test:** New tests: `TestProviderRegistry` (registration works, duplicate panics), `TestNewDispatch` (factory lookup), `TestResolveProviderFromRegistry` (config wiring). All existing tests continue passing.

### Wave 3: Adapter-level tests

**Files to create:**
- `internal/provider/deepseek_test.go` — test `NewDeepSeek` with empty/explicit BaseURL, test dispatch via `Providers`
- `internal/provider/openrouter_test.go` — test `NewOpenRouter` with empty/explicit BaseURL, test HTTPReferer/XTitle defaults

**Files to modify:**
- `internal/provider/pipeline_integration_test.go` — add integration test that wires `config.Load` (with test TOML) → `provider.New` → `ChatTurn` against httptest server
- `internal/config/provider_integration_test.go` — add test that `resolveProvider` resolves defaults correctly via registry

### Wave 4: Options cleanup

**Files to modify:**
- `internal/provider/provider.go` — strip `HTTPReferer` and `XTitle` from `Options` struct; add `Extra map[string]string` for provider-specific overrides
- `internal/provider/deepseek.go` — use `CompatOptions` directly; remove dependency on `config.DeepSeek*` constants
- `internal/provider/openrouter.go` — use `CompatOptions` directly; remove dependency on `config.OpenRouter*` constants
- `internal/provider/openai_compat.go` — ensure `NewOpenAICompatWithRetry` also uses `CompatOptions`
- `internal/provider/retry_test.go` — update callers of `NewOpenAICompatWithRetry`

**Remove:** `NewOpenAICompatLegacy` backward compat shim (all callers migrated).

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
| P8: KnownProviders dead code | "Remove it? Then how does user see supported providers?" | **UPHELD.** Replaced by `provider.Providers` map keys. Runtime error message uses `maps.Keys(Providers)`. |
| P9: Scattered metadata | "Constants file is fine — Go idiom." | **UPHELD (modified).** Keep only truly cross-cutting constants in config/; move per-provider defaults to the adapter file's descriptor. |
| P10: No tests | "8-line factories don't need tests." | **PARTIALLY UPHELD.** Factory constructors need tests only to prove they build the right `CompatOptions` — ~5 LOC each. Integration tests are more valuable. |

---

## §6 Open Questions (deferred)

1. **`ExtraBody` for provider-specific request fields** (ZAI `thinking` param). Deferred until ZAI adapter implementation. Will add `ExtraBody map[string]any` to `CompatOptions` if needed.

2. **Multimodal content** (`Message.Content` as string → array of parts). Cross-cutting change across all providers. Separate plan.

3. **Provider auto-discovery / dynamic registration.** Not needed. Hardcoded map with explicit registration is sufficient for foreseeable future.

4. **Removing `config.DeepSeekName` etc.** Some consumers (tests, `agent/loop_integration_test.go`) reference these constants. Migration must update all references or keep aliases.

---

## §7 Post-Consolidation: Adding ZAI

Once Phase 1 is complete, adding ZAI is:

1. Create `internal/provider/zai.go` — adapter file + `Providers["zai"]` registration with `ExtraHeaders: {"Accept-Language": "en-US,en"}` and `ErrorParser: zaiErrorParser`
2. Create `internal/provider/zai_test.go` — constructor, config wiring, error intercept E2E
3. Add `[providers.zai]` section to `mivia.toml.example`

No changes to `provider.go`, `config/defaults.go`, `config/load.go`, or `openai_compat.go`.
