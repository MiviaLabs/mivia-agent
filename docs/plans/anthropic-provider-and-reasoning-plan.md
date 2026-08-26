# Native Anthropic Provider and Reasoning Effort Architecture Plan

**Status:** Proposed
**Author:** mivia / MiviaLabs
**Date:** March 2025 / 2026 Ready
**Topic:** Native Anthropic Messages API Provider (`anthropic`), Extended Thinking / Reasoning Effort Integration, Claude Model Auto-Resolution, and Proxy Incompatibility Remediation.

---

## 1. Executive Summary

### 1.1 Context & Background
In current workflows, configuring Claude models (e.g. `claude-sonnet-5`) through local OpenAI-compatible proxies (such as `llmproxycli` or LiteLLM at `http://127.0.0.1:8317/v1`) frequently causes runtime failures with:
```
openai: provider error (HTTP 400, type invalid_request_error)
```
While OpenAI-compatible translation shims work well for basic text turns, they break when advanced agentic features are activated—specifically:
- **Chain-of-thought / Extended Thinking** (`reasoning_effort` vs Anthropic's `thinking` token budgets)
- **Multi-step Tool Turns with Thinking Replay** (OpenAI `reasoning_content` vs Anthropic `thinking` blocks with opaque cryptographic signatures)
- **Cache Breakpoints** (`cache_control` ephemeral blocks on system / tools / user messages)
- **Strict Parameter Constraints** (`max_tokens` strictly exceeding `budget_tokens`, temperature locks)

### 1.2 Objectives
1. **First-Class Native Anthropic Provider (`[providers.anthropic]`):** Implement a native `Completer` directly speaking the Anthropic Messages API (`/v1/messages`), supporting streaming SSE, tool calling, prompt caching, and thinking blocks.
2. **2026 Default Model Standards:** Standardize the default Claude flagship model to `claude-sonnet-5` across provider registries and agent definitions, with full support for `claude-opus-5` and `claude-haiku-5`.
3. **Reasoning Effort & Thinking Budget Mapping:** Define a clean mapping between Mivia's provider-neutral `reasoning.Level` (`low`, `medium`, `high`, `xhigh`, `max`) and Anthropic's token budget parameters.
4. **Under-the-Hood Auto-Routing & Compatibility:** Enable seamless routing when Claude models are invoked directly or via proxies, ensuring proper header injection (`anthropic-version`, `anthropic-beta`) and request shaping.

---

## 2. Root Cause Analysis: OpenAI Proxy vs Anthropic Wire Incompatibilities

| Dimension | OpenAI `/v1/chat/completions` (Proxy) | Anthropic `/v1/messages` (Native) | Failure Mode in Proxies |
| :--- | :--- | :--- | :--- |
| **Reasoning Control** | `reasoning_effort: "low"\|"medium"\|"high"` | `thinking: {"type": "enabled", "budget_tokens": N}` | HTTP 400: Unknown field `reasoning_effort`, or budget translation omitted. |
| **Output Token Budget** | `max_tokens` is optional or standalone | `max_tokens` **must be > `budget_tokens`** | HTTP 400: `max_tokens must be greater than thinking.budget_tokens`. |
| **Reasoning Replay** | `reasoning_content: "text"` on assistant message | `{"type": "thinking", "thinking": "...", "signature": "..."}` | HTTP 400: Extra field `reasoning_content` not permitted, or missing signature. |
| **System Prompt** | Message in `messages` with `role: "system"` | Top-level `system` string or array of text blocks | Message order rejection or loss of cache markers. |
| **Tool Calling** | `tools: [{type: "function", function: ...}]` | `tools: [{name, description, input_schema}]` | Schema translation mismatch or invalid tool call IDs. |
| **Prompt Caching** | Varies by proxy (some expect OpenAI, some Anthropic) | `cache_control: {"type": "ephemeral"}` (max 4 breakpoints) | Rejected array-content payloads on OpenAI endpoints. |

---

## 3. Architecture & Technical Specification

```
                          ┌────────────────────────┐
                          │   mivia Agent Engine   │
                          │ (Agent Loop / Context) │
                          └───────────┬────────────┘
                                      │
                                      ▼
                      ┌────────────────────────────────┐
                      │    provider.NewForProvider     │
                      └───────┬────────────────┬───────┘
                              │                │
             Provider: "anthropic"             Provider: "llmproxycli" / "openrouter"
                              │                │
                              ▼                ▼
                ┌───────────────────┐    ┌───────────────────┐
                │ AnthropicCompleter│    │   OpenAICompat    │
                └─────────┬─────────┘    └─────────┬─────────┘
                          │                        │
                          ▼                        ▼
                POST /v1/messages        POST /v1/chat/completions
                (Native Anthropic)       (OpenAI wire format)
```

### 3.1 Provider Registry (`internal/providerregistry/registry.go`)
Register `"anthropic"` as a canonical provider:
```go
"anthropic": {
    Name:             "anthropic",
    DefaultModel:     "claude-sonnet-5",
    DefaultURL:       "https://api.anthropic.com/v1",
    DefaultAPIKeyEnv: "ANTHROPIC_API_KEY",
},
```

### 3.2 Reasoning Effort to Anthropic Thinking Budget Mapping
Mivia's neutral `reasoning.Level` values map to concrete token budgets in `internal/reasoning/`:
* `reasoning.Off`: `thinking: {"type": "disabled"}` (or omitted)
* `reasoning.Minimal`: `budget_tokens: 1024`
* `reasoning.Low`: `budget_tokens: 2048`
* `reasoning.Medium`: `budget_tokens: 8192`
* `reasoning.High`: `budget_tokens: 16384`
* `reasoning.XHigh`: `budget_tokens: 32768`
* `reasoning.Max`: `budget_tokens: 65536`

**Dynamic `max_tokens` Guarantee:**
Whenever thinking is enabled with budget $B$:
$$\text{wire\_max\_tokens} = \max(\text{req.MaxTokens}, B + 4096)$$
This guarantees Anthropic's invariant ($\text{max\_tokens} > \text{budget\_tokens}$) is satisfied without manual user configuration.

### 3.3 Message & Tool Calling Wire Formats

#### System Prompt & Cache Markers
Anthropic expects system prompts at top level:
```json
{
  "system": [
    {
      "type": "text",
      "text": "You are mivia...",
      "cache_control": { "type": "ephemeral" }
    }
  ]
}
```

#### Multi-Turn Tool Exchanges with Thinking Replay
When preserving thinking from prior turns:
```json
{
  "role": "assistant",
  "content": [
    {
      "type": "thinking",
      "thinking": "Need to inspect internal/provider/...",
      "signature": "opaque_signature_token_from_stream"
    },
    {
      "type": "tool_use",
      "id": "toolu_01A",
      "name": "read_file",
      "input": { "path": "go.mod" }
    }
  ]
}
```
Tool responses:
```json
{
  "role": "user",
  "content": [
    {
      "type": "tool_result",
      "tool_use_id": "toolu_01A",
      "content": "module github.com/MiviaLabs/mivia-agent\n..."
    }
  ]
}
```

### 3.4 Streaming Protocol Support (`/v1/messages` SSE)
The stream processor handles Anthropic SSE event types:
1. `message_start`: Captures initial metadata and input token usage.
2. `content_block_start`: Distinguishes between `text`, `thinking`, and `tool_use`.
3. `content_block_delta`:
   - `thinking_delta`: Streams thought accumulation into reasoning builder.
   - `signature_delta`: Retains block signature for subsequent roundtrip replay.
   - `text_delta`: Live writes assistant text to UI writer.
   - `input_json_delta`: Accumulates streaming tool arguments.
4. `content_block_stop`: Finalizes individual blocks.
5. `message_delta`: Extracts `stop_reason` (`end_turn`, `tool_use`, `max_tokens`) and output token usage.

---

## 4. Implementation Roadmap

### Phase 1: Core Registry & Reasoning Dialects
1. Add `anthropic` to `internal/providerregistry/registry.go`.
2. Add `DialectAnthropicThinking` in `internal/reasoning/reasoning.go`.
3. Update `scripts/check_provider_docs.py` and registry tests.

### Phase 2: Native Anthropic Client Implementation
1. Create `internal/provider/anthropic.go` implementing `Completer`.
2. Create `internal/provider/anthropic_stream.go` for SSE block parsing.
3. Wire `NewAnthropic` in `internal/provider/provider.go` factory dispatch.

### Phase 3: Proxy & Model Dialect Auto-Detection
1. Update `internal/provider/llmproxycli.go` to support model-specific dialect overrides when Claude models are used through local proxies.
2. Ensure `claude-sonnet-5` is configured with appropriate thinking headers and token budgeting.

### Phase 4: Verification & Docs
1. Unit tests covering message translation, tool invocation, thinking signature retention, and streaming.
2. Update documentation (`docs/product/config.md`, `README.md`).

---

## 5. Summary of Benefits

- **Reliability:** Eliminates HTTP 400 errors caused by parameter mismatches in intermediate proxies.
- **Full Model Potential:** Unlocks full Claude 5 capabilities including Extended Thinking, prompt caching, and fast multi-step tool execution.
- **Dual Flexibility:** Supports direct API access via `ANTHROPIC_API_KEY` and proxy routing via `llmproxycli`.
