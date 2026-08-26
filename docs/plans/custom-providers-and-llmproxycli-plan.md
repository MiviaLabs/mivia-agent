# Custom Providers and LLM Proxy CLI Support Plan

**Status:** Implemented
**Author:** mivia / MiviaLabs
**Date:** March 2025
**Topic:** LLM Provider Architecture, Custom OpenAI-Compatible Endpoints, Local CLI Proxy Support (`llmproxycli`), and `llmgateway` Configuration Clean-up.

---

## 1. Executive Summary

### 1.1 Context
In the current implementation of `mivia`, provider registration is strictly gated by a hard-coded compile-time allowlist in `internal/providerregistry/registry.go`. Any provider table declared in `.mivia/mivia.toml` (e.g. `[providers.<name>]`) that is not present in `providerregistry.Lookup(name)` fails closed during configuration loading (`internal/config/load.go`) with an error:
```
unknown provider "<name>" (supported: deepseek, llmgateway, minimax, ollama, openrouter, zai)
```

As a result, workspace configurations requiring a local proxy (such as `llmproxycli`, LiteLLM, or custom OpenAI-compatible endpoints) were forced to hijack an existing registered provider stanza—specifically `[providers.llmgateway]`—and repoint its `base_url` to `http://127.0.0.1:8317/v1` with `api_key_env = 'CLIPROXY_API_KEY'`.

### 1.2 Identified Issues with Hijacking `llmgateway`
1. **Upstream Gateway Inaccessibility:** Users cannot access the real online LLM Gateway service (`https://api.llmgateway.io/v1`) concurrently with local proxies.
2. **Proprietary Upstream Header Leakage:** `internal/provider/llmgateway.go` injects `X-No-Fallback: true` for thinking-mode models (`thinking_effort` dialect). This is specific to LLM Gateway's upstream provider failover logic and can cause unexpected errors or 400 bad requests when sent to local proxies.
3. **Anthropic Cache Marker Suppression:** `llmgateway.go` sets `CacheMarkersEnabled: false` because LLM Gateway automatically injects Anthropic cache markers server-side. Local proxies expecting client-side cache markers do not receive them.
4. **Hashed Session Key Emission:** `llmgateway.go` emits hashed session IDs in the OpenAI `user` field (`SendSessionUserKey: true`) for online server-side cache affinity.

---

## 2. Architectural Solution

We propose a **Hybrid Architectural Design**:
1. **Dedicated First-Class `llmproxycli` Provider:** Provides an out-of-the-box, zero-friction preset for local CLI proxies with default loopback settings (`http://127.0.0.1:8317/v1`, `CLIPROXY_API_KEY`).
2. **Generic Custom OpenAI-Compatible Provider Extensibility:** Relaxes the hard-closed provider registry check in configuration loading to allow arbitrary `[providers.<custom_name>]` tables when an explicit `base_url` is provided.
3. **Clean Workspace Configuration Migration:** Restores `[providers.llmgateway]` to official upstream defaults and configures `[providers.llmproxycli]` with the local proxy models.

---

## 3. Detailed Technical Design

### 3.1 Security & Networking Guarantees
* **Loopback Dial Pinning:** In `internal/provider/provider.go`, `newLoopbackDialContext` independently re-verifies and pins DNS resolution to `127.0.0.1` / `::1` for loopback endpoints, protecting against DNS rebinding attacks.
* **HTTP on Loopback:** In `internal/config/validate.go`, `validateBaseURL` permits unencrypted `http://` for verified loopback endpoints without requiring `MIVIA_ALLOW_INSECURE_HTTP=1`.
* **API Key Validation:** `provider.NewForProvider` enforces that `runtime.APIKeySet` is true and non-empty for all keyed providers (including `CLIPROXY_API_KEY`), ensuring fail-closed credential protection.
* **Reasoning Dialect & Thinking Replay:** Custom and local proxy providers default to `DialectOpenAI` in `internal/reasoning/reasoning.go`. Model specifications under `[[providers.llmproxycli.models]]` can override `reasoning_dialect` per model (e.g. `thinking_effort`).

---

## 4. Implementation Steps

### Phase 1: Codebase Updates

#### 1. Provider Registry (`internal/providerregistry/registry.go`)
Register `llmproxycli` in the built-in descriptor map:
```go
"llmproxycli": {
    Name:             "llmproxycli",
    DefaultModel:     "claude-sonnet-5",
    DefaultURL:       "http://127.0.0.1:8317/v1",
    DefaultAPIKeyEnv: "CLIPROXY_API_KEY",
},
```

#### 2. Provider Factory (`internal/provider/llmproxycli.go`)
Create a dedicated provider factory using standard OpenAI-compatible mechanics:
```go
package provider

import (
    "github.com/MiviaLabs/mivia-agent/internal/providerregistry"
    "github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

func NewLLMProxyCLI(opts Options) (Completer, error) {
    base := opts.BaseURL
    if base == "" {
        descriptor, _ := providerregistry.Lookup("llmproxycli")
        base = descriptor.DefaultURL
    }
    dialect := opts.ReasoningDialect
    if dialect == "" {
        dialect = defaultReasoningDialect("llmproxycli")
    }
    return NewOpenAICompatWithOptions(CompatOptions{
        Name:                    "llmproxycli",
        BaseURL:                 base,
        APIKey:                  opts.APIKey,
        DialContext:             opts.DialContext,
        CacheUsageEnabled:       opts.CacheUsageEnabled,
        CacheMarkersEnabled:     opts.CacheMarkersEnabled,
        Reasoning:               dialect,
        RequiresReasoningReplay: true,
    }), nil
}
```

#### 3. Factory Registration (`internal/provider/provider.go`)
Add `{"llmproxycli", NewLLMProxyCLI}` to the `builtins` slice in `internal/provider/provider.go`.

#### 4. Reasoning Dialect (`internal/reasoning/reasoning.go`)
Add `"llmproxycli": DialectOpenAI` to `defaultDialects`.

#### 5. Documentation & Doc Checker Scripts
* Add `"llmproxycli"` / `"LLM Proxy CLI"` mappings to `scripts/check_provider_docs.py`.
* Update `README.md`, `docs/product/config.md`, `docs/architecture/overview.md`, and `.mivia/mivia.toml.example`.

---

### Phase 2: Workspace Configuration (`.mivia/mivia.toml`)

#### 1. Restore Official `[providers.llmgateway]`
```toml
[providers.llmgateway]
api_key_env = 'LLMGATEWAY_API_KEY'
base_url = 'https://api.llmgateway.io/v1'
default_model = 'deepseek-v4-pro'

[[providers.llmgateway.models]]
context_window_tokens = 1100000
name = 'deepseek-v4-pro'
reasoning = 'high'
reasoning_efforts = ['low', 'medium', 'high', 'max']

[[providers.llmgateway.models]]
context_window_tokens = 1000000
name = 'muse-spark-1.2'

[[providers.llmgateway.models]]
context_window_tokens = 1000000
name = 'glm-5.2'
```

#### 2. Configure Dedicated `[providers.llmproxycli]`
```toml
[provider]
default_model = 'gemini-3.7-flash-high'
name = 'llmproxycli'

[providers.llmproxycli]
api_key_env = 'CLIPROXY_API_KEY'
base_url = 'http://127.0.0.1:8317/v1'
default_model = 'gemini-3.7-flash-high'

[[providers.llmproxycli.models]]
context_window_tokens = 200000
max_output_tokens = 128000
name = 'claude-sonnet-5'
reasoning = 'high'
reasoning_efforts = ['low', 'medium', 'high', 'max']

[[providers.llmproxycli.models]]
context_window_tokens = 200000
max_output_tokens = 128000
name = 'claude-sonnet-4-7'
reasoning = 'high'
reasoning_efforts = ['low', 'medium', 'high', 'max']

[[providers.llmproxycli.models]]
context_window_tokens = 200000
max_output_tokens = 128000
name = 'claude-sonnet-4-6'
reasoning = 'high'
reasoning_efforts = ['low', 'medium', 'high', 'max']

[[providers.llmproxycli.models]]
context_window_tokens = 200000
max_output_tokens = 128000
name = 'claude-opus-5'
reasoning = 'high'
reasoning_efforts = ['low', 'medium', 'high', 'max']

[[providers.llmproxycli.models]]
context_window_tokens = 200000
max_output_tokens = 128000
name = 'claude-opus-4-7'
reasoning = 'high'
reasoning_efforts = ['low', 'medium', 'high', 'max']

[[providers.llmproxycli.models]]
context_window_tokens = 200000
max_output_tokens = 128000
name = 'claude-fable-5'
reasoning = 'high'
reasoning_efforts = ['low', 'medium', 'high', 'max']

[[providers.llmproxycli.models]]
context_window_tokens = 1000000
max_output_tokens = 65536
name = 'gemini-3.1-pro-low'
reasoning = 'high'
reasoning_efforts = ['low', 'medium', 'high']

[[providers.llmproxycli.models]]
context_window_tokens = 1000000
max_output_tokens = 65536
name = 'gemini-pro-agent'
reasoning = 'high'
reasoning_efforts = ['low', 'medium', 'high']

[[providers.llmproxycli.models]]
context_window_tokens = 1000000
max_output_tokens = 65536
name = 'gemini-3.7-flash-high'
reasoning = 'high'
reasoning_efforts = ['low', 'medium', 'high']

[[providers.llmproxycli.models]]
context_window_tokens = 1000000
max_output_tokens = 65536
name = 'gemini-3.6-flash-high'
reasoning = 'high'
reasoning_efforts = ['low', 'medium', 'high']

[[providers.llmproxycli.models]]
context_window_tokens = 1000000
max_output_tokens = 65536
name = 'gemini-3-flash'
reasoning = 'high'
reasoning_efforts = ['low', 'medium', 'high']

[[providers.llmproxycli.models]]
context_window_tokens = 1000000
max_output_tokens = 384000
name = 'runware/deepseek-v4-flash'
reasoning = 'high'
reasoning_dialect = 'thinking_effort'
reasoning_efforts = ['high', 'max']

[[providers.llmproxycli.models]]
context_window_tokens = 131072
max_output_tokens = 32768
name = 'runware/gpt-oss-120b'
reasoning = 'high'
reasoning_efforts = ['low', 'medium', 'high']
```

#### 3. Update Environment Variable Allowlist (`.mivia/mivia.toml`)
Ensure `[tools].env_allowlist` includes both:
```toml
[tools]
env_allowlist = [
  'DEEPSEEK_API_KEY',
  'OPENROUTER_API_KEY',
  'ZAI_API_KEY',
  'OLLAMA_API_KEY',
  'LLMGATEWAY_API_KEY',
  'CLIPROXY_API_KEY',
  'MINIMAX_API_KEY',
  'TAVILY_API_KEY',
]
```

---

## 5. Verification Matrix

| Area | Test Target | Verification Command |
| :--- | :--- | :--- |
| **Provider Registry** | Verify lookup, sorting, and defaults for `llmproxycli` | `go test ./internal/providerregistry/...` |
| **Provider Factory** | Verify client creation, loopback dial pinning, options | `go test ./internal/provider/...` |
| **Config Loading** | Verify `.mivia/mivia.toml` parses both `llmgateway` and `llmproxycli` | `go test ./internal/config/...` |
| **Reasoning Dialects**| Verify dialect resolution and model-level overrides | `go test ./internal/reasoning/...` |
| **Documentation Gate**| Verify docs consistency script passes | `python3 scripts/check_provider_docs.py` |
| **Full Repo Contract**| Run full gate verification | `make verify` |
