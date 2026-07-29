# Plan: ZAI / GLM Provider Adapter

**Status:** Ready for ADLC Step 0 — revised after provider-architecture refactor
**Date:** 2026-07-29
**Scope:** Add a safe, standard-endpoint `zai` (GLM) OpenAI-compatible provider.

## 1. Decision record

The prerequisite provider consolidation is complete (`e069064`). Its delivered
architecture differs from the earlier deferred plan:

- Provider metadata is in `internal/providerregistry`, not `internal/provider`.
- Provider factories are registered explicitly by `registerBuiltins` in
  `internal/provider/provider.go`; there is no public `Providers` map and no
  init-time registration.
- The extensible shared-client constructor is
  `NewOpenAICompatWithOptions(CompatOptions)`. The older `NewOpenAICompat`
  still accepts five string arguments.
- `CompatOptions.ExtraHeaders`, `ExtraBody`, and `ErrorParser`, plus transport
  fields for reasoning and web-search data, already exist.

This slice supports ZAI's standard PaaS endpoint only. It deliberately does
not add endpoint auto-detection, Coding Plan support, forced thinking mode,
reasoning display, or web-search citation rendering. Those are separate
features with additional state, privacy, and concurrency requirements.

## 2. Confirmed API contract

| Item | Value |
|---|---|
| Protocol | OpenAI-compatible Chat Completions and SSE |
| Standard base URL | `https://api.z.ai/api/paas/v4` |
| Endpoint | `POST /api/paas/v4/chat/completions` |
| Auth | `Authorization: Bearer <ZAI_API_KEY>` |
| Header | Send `Accept-Language: en-US,en` (documented default and used in examples) |
| Default model | `glm-5.2` |
| Flat errors | `{"code": N, "message": "..."}` |
| Search results | `web_search` is top-level in the non-stream response, not nested in `message` |

Sources: ZAI HTTP API introduction, Chat Completion reference, Thinking Mode,
Coding Plan Quick Start, and Error Codes documentation at `docs.z.ai`.

## 3. Scope and non-goals

### In scope

- Selecting `zai` via TOML or `--provider zai`.
- Defaults: model `glm-5.2`, standard PaaS base URL, and `ZAI_API_KEY`.
- The standard chat, streaming, tool-call, and flat-error paths.
- Request `Accept-Language: en-US,en` and clear ZAI error messages.
- Accurate transport decoding for top-level non-stream `web_search` data.
- Example and owned product configuration documentation.

### Explicitly deferred

- Automatic switch between standard and Coding Plan URLs. The previous proposal
  incorrectly treated 1113, 1309, and 1311 as endpoint-mismatch codes; they
  are documented quota/subscription/model-entitlement errors. The previous
  mutable-base-URL wrapper was also race-prone and could not tell an explicit
  configured URL from a resolved default.
- Coding Plan support. Its preserved-thinking contract requires replaying
  exact `reasoning_content` through tool-result history; `provider.Message`
  and the agent loop cannot do that today.
- Sending `thinking: {type: enabled}` unconditionally. Recent ZAI models
  already enable thinking by default, and changing this needs an explicit
  user-facing policy.
- Rendering or persisting reasoning traces and search citations. The existing
  response fields are transport-only: the agent loop consumes only content and
  tool calls. Hidden reasoning must not become durable session content without
  a privacy-reviewed, opt-in rendering design.

## 4. Implementation plan

### 4.1 Shared wire correction first

Modify `internal/provider/openai_compat.go` and its tests so a non-stream
response decodes `web_search` from the response top level and copies it into
`Response.WebSearch`. Preserve the existing response API. Do not invent SSE
web-search semantics that ZAI's streaming contract does not document.

### 4.2 Provider metadata and factory registration

1. Modify `internal/providerregistry/registry.go` to add a `zai` descriptor:
   - `Name: "zai"`
   - `DefaultModel: "glm-5.2"`
   - `DefaultURL: "https://api.z.ai/api/paas/v4"`
   - `DefaultAPIKeyEnv: "ZAI_API_KEY"`
2. Modify `internal/provider/provider.go` so `registerBuiltins` explicitly
   registers `NewZAI`. This keeps metadata and factories consistent and makes
   `provider.New` dispatch it.

### 4.3 Adapter

Create `internal/provider/zai.go`:

```go
func NewZAI(opts Options) (Completer, error) {
    base := opts.BaseURL
    if base == "" {
        descriptor, _ := providerregistry.Lookup("zai")
        base = descriptor.DefaultURL
    }
    return NewOpenAICompatWithOptions(CompatOptions{
        Name:    "zai",
        BaseURL: base,
        APIKey:  opts.APIKey,
        ExtraHeaders: map[string]string{
            "Accept-Language": "en-US,en",
        },
        ErrorParser: zaiErrorParser,
    }), nil
}
```

`zaiErrorParser(statusCode, body)` must only claim a ZAI flat error when JSON
contains a non-empty `message` and a `code`. It returns `nil` for malformed
JSON and OpenAI-shaped error envelopes so the generic error handling remains
authoritative. Its formatted error must be sanitized and bounded; it must not
include API keys or request content.

### 4.4 Configuration documentation

Modify `mivia.toml.example` and the owned `docs/product/config.md` to list
`zai`, show its standard URL and `ZAI_API_KEY`, and document `--provider zai`.
Do not advertise the Coding Plan URL until its reasoning-history contract is
implemented.

## 5. TDD task graph and test matrix

Execute through ADLC; each production change gets a compiling RED test before
its implementation.

| Wave | Files / focus | Required assertions |
|---|---|---|
| 1 | `openai_compat_test.go` then `openai_compat.go` | Top-level `web_search` is preserved from a valid non-stream response; existing nested assumptions are removed or retained only where another documented provider needs them. |
| 2 | `providerregistry/registry_test.go` then `registry.go` | `Lookup("zai")`, stable sorted names, and all ZAI defaults. |
| 3 | `zai_test.go` then `zai.go` | Default and explicit base URL; request path; Bearer auth; `Accept-Language` for non-stream and SSE; flat errors on non-2xx and HTTP-200 envelopes; OpenAI-shaped error falls through; valid non-stream and streaming responses. |
| 4 | `provider_test.go` then `provider.go` | `provider.New` dispatches ZAI and supported-provider diagnostics include it. |
| 5 | `config/load_test.go` and/or `pipeline_integration_test.go` | `config.Load` defaults and env lookup for TOML `zai`; `ProviderOverride: "zai"`; end-to-end load → `provider.New` → `ChatTurn` against `httptest`. |
| 6 | docs/example | Config syntax and supported-provider list agree with runtime. |

Use `httptest` to inspect the wire contract rather than private fields. Include
an immutability assertion that mutating the caller's options map cannot alter
later requests. Error tests must check that error output is bounded and does
not echo the API key or prompt. No test may call unexported `config.resolveProvider`
from the `provider` package.

## 6. Verification

Run after each implementation wave as applicable:

```text
go test ./internal/provider/... ./internal/providerregistry/... ./internal/config/... -race -count=1
go build ./...
go vet ./...
make verify
make race
make secret-scan
make docs-check
```

Optional manual smoke test, never logged with a real key:

```text
ZAI_API_KEY=... ./mivia chat --provider zai --model glm-5.2 --no-tools -p "say hello"
```

## 7. Follow-up plan prerequisites

Before adding Coding Plan URL support, auto-detection, thinking controls, or
reasoning/search presentation, first design and test:

1. explicit-versus-default base-URL provenance through config and provider
   options;
2. race-free endpoint selection that cannot redirect concurrent calls;
3. exact reasoning-content preservation across assistant tool calls and tool
   results, with a privacy/retention policy; and
4. an opt-in renderer and owned documentation for any user-visible reasoning
   or citation data.
