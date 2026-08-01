# 34 - xAI provider via API key

**Status:** DESIGN - not yet implemented.
**Date:** 2026-08-02
**Depends on:** `internal/provider` factory registry, `internal/providerregistry`,
`internal/config` (env-file + provider runtime resolution), **plan 37**
(reasoning-effort field in the shared adapter - grok models are reasoning models).
**Blocks:** nothing. **Amends:** nothing.
**Blast radius:** LOW - one new OpenAI-compatible provider behind the existing
factory seam. No new auth surface; an xAI API key is a static secret like
DeepSeek/OpenRouter/z.ai.

---

## 1. Goal

Add `xai` as a first-class provider that authenticates with a static xAI API key
(`XAI_API_KEY`), talking to the public xAI API at `https://api.x.ai/v1`. This is
the pay-as-you-go / console.x.ai path - **not** the Grok Build subscription path
(plan 35 covers that). It is the simplest possible xAI integration and exists so a
user with an API key can use grok models from mivia today, with zero new auth code.

## 2. Why this is a separate plan from subscription auth

Research (`.mivia/plans/33-lifecycle-hooks.md` appendix, grok-build source analysis)
established that xAI has two distinct credential paths:

| | API key (this plan) | Subscription token (plan 35) |
|---|---|---|
| **Credential** | Static `xai-...` string | OAuth2 access_token + refresh_token (JWT) |
| **Source** | `console.x.ai` | `grok login` browser/device-code flow |
| **Endpoint** | `api.x.ai/v1` | `cli-chat-proxy.grok.com` (inference proxy) |
| **Refreshable** | No | Yes (PKCE + refresh_token) |
| **Carries entitlement** | No (balance on account) | Yes (team principal in JWT) |
| **Our effort** | Trivial - reuse OpenAI-compat | Significant - OAuth client (plan 36) |

A user with a console API key should not wait for an OAuth client to ship. This plan
is the 90%-coverage path with near-zero code, because xAI's API is OpenAI-compatible
and we already have the adapter.

## 3. What exists today

Three OpenAI-compatible providers are registered as factories:

- `internal/providerregistry/registry.go` - `Descriptor` map: `deepseek`, `openrouter`, `zai`. Each has `DefaultModel`, `DefaultURL`, `DefaultAPIKeyEnv`.
- `internal/provider/{deepseek,openrouter,zai}.go` - thin constructors calling `NewOpenAICompatWithOptions(CompatOptions{...})`.
- `internal/provider/openai_compat.go:406` - `httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)`. The bearer header is already correct for xAI.
- `internal/config/load.go:57` - `envfile.Lookup(pc.APIKeyEnv, envMap)` resolves the key from the env file.
- `internal/config/types.go:271` - `ProviderRuntime` carries `APIKey` to the factory.

xAI fits this shape exactly. No new auth code, no new header logic, no new config
section beyond a provider block the user already knows how to write.

## 4. Design - three small additions

### 4a. Provider descriptor

`internal/providerregistry/registry.go`:

```go
"xai": {
    Name: "xai", DefaultModel: "grok-4.5",
    DefaultURL: "https://api.x.ai/v1", DefaultAPIKeyEnv: "XAI_API_KEY",
},
```

### 4b. Provider factory

`internal/provider/xai.go` (new):

```go
func NewXAI(opts Options) (Completer, error) {
    base := opts.BaseURL
    if base == "" {
        d, ok := providerregistry.Lookup("xai")
        if !ok {
            return nil, fmt.Errorf("provider %q has no built-in descriptor", "xai")
        }
        base = d.DefaultURL
    }
    return NewOpenAICompatWithOptions(CompatOptions{
        Name: "xai", BaseURL: base, APIKey: opts.APIKey,
    }), nil
}
```

No custom headers, no error parser, no `X-XAI-Token-Auth`. That header is a
subscription-session signal (plan 35); a bare API key uses standard `Bearer` auth,
which `OpenAICompat` already sets.

### 4c. Register the factory

`internal/provider/provider.go` - in `registerBuiltins`, alongside the other three:

```go
if err := registry.register("xai", NewXAI); err != nil {
    builtinsErr = err
    return
}
```

### 4d. Error parser (optional, defer)

xAI returns errors as `{"error": {"message": "...", "type": "...", "code": "..."}}`,
which the default `sanitizeErr` path handles. A dedicated `xaiErrorParser` (like
`zaiErrorParser`) is only warranted if xAI echoes sensitive material in error
messages the way z.ai does (`zai_errors.go`). Research found no evidence xAI's API
echoes request content or keys in errors, so the default path is safe for v1. Add a
parser only if a real error-leak is observed.

## 5. Reasoning (consumed via plan 37)

grok models are reasoning models. grok-4.5 supports `reasoning_effort` of `low`,
`medium`, `high` (default `high`) and **cannot disable reasoning**. grok-4.3 adds
`none`. Neither supports `minimal`/`xhigh`/`max`.

This plan does **not** add reasoning code. It depends on plan 37, which defines the
provider-aware `ReasoningDialect` abstraction. The xAI factory selects
`openaiDialect{}` (plan 37 §3a), which maps the internal `ReasoningLevel` to the
top-level `reasoning_effort` string. The user sets `reasoning = "high"` in `[chat]`
and the shared adapter stamps the wire field and suppresses `temperature`.

The one xAI-specific note: because grok-4.5 **always** reasons, a user who leaves
`reasoning_effort` unset against grok-4.5 still gets reasoning (default `high`), and
the `temperature = 0` default in `mivia.toml.example` will be rejected by the API.
Documenting this in the example block (§5a) is the mitigation; the real fix is plan
37's temperature suppression.

### 5a. Example config note

The `[providers.xai]` block in `mivia.toml.example` should carry a comment:

```toml
[providers.xai]
# grok-4.5 is a reasoning model. Set [chat].reasoning_effort = "high" (or low/medium).
# grok-4.5 cannot disable reasoning; grok-4.3 supports "none".
# When reasoning_effort is set, temperature is suppressed automatically (plan 37).
```

## 6. User-facing config

A user adds this to `mivia.toml`:

```toml
[provider]
name = "xai"

[providers.xai]
models = [
  { name = "grok-4.5", context_window_tokens = 256000 },
  { name = "grok-code", context_window_tokens = 256000 },
]
default_model = "grok-4.5"
api_key_env = "XAI_API_KEY"
base_url = "https://api.x.ai/v1"
```

And in `~/.mivia/.env`:

```
XAI_API_KEY=xai-...
```

Switch with `mivia --provider xai`. No CLI flag, no `mivia login` - the key is a
static env secret, identical to how DeepSeek and z.ai work today.

## 7. What this does NOT do

- **No subscription auth.** A SuperGrok/X Premium+ subscription cannot be used
  through this path. `grok login` tokens are not API keys and hit a different
  endpoint. Plan 35 covers importing a subscription session.
- **No OAuth client.** This plan adds zero auth code. An API key is a static secret.
- **No `X-XAI-Token-Auth` header.** That header distinguishes subscription sessions
  from deployment keys on xAI's inference proxy. A bare API key does not need it.
- **No custom error parser** unless a leak is found (§4d).

## 8. Verification

- `go test ./internal/provider/...` - new `xai_test.go`: factory builds with a key,
  sets `Authorization: Bearer`, hits the configured base URL, fail-closes without a key
- `go test ./internal/providerregistry/...` - `xai` descriptor resolves
- `go build ./... && go vet ./...`
- Manual: `mivia --provider xai` with a real key completes a chat turn

## 9. Invariant

No new invariant row. This plan adds a provider behind an existing, tested seam
(`NewOpenAICompatWithOptions`), changes no auth surface, and introduces no
privilege boundary. `INV-AG-2` (run_command as argv) and `INV-AG-5` (redaction
opt-in) already cover the relevant properties.

## 10. Rollback

If xAI's API proves not to be OpenAI-compatible in some edge case (tool-call schema,
streaming chunk shape), the fix is a `CompatOptions` tweak in `xai.go`, not a
revert. The descriptor and factory registration are additive and cost nothing when
the provider is unused.

## 11. Sequencing

1. `internal/providerregistry/registry.go` - add `xai` descriptor
2. `internal/provider/xai.go` (new) + `xai_test.go` - factory + tests
3. `internal/provider/provider.go` - register factory
4. `.mivia/mivia.toml.example` - document the `[providers.xai]` block + reasoning note (§5a)
5. Land **after** plan 37 so the reasoning field exists
6. `docs/development/providers.md` (if exists) - add xAI section
