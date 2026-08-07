# 60 - Ollama Cloud provider (API key only)

**Status:** IMPLEMENTATION-READY - product scope locked to cloud + API key.
**Date:** 2026-08-07
**Depends on:** shipped OpenAI-compatible adapter (`internal/provider`), closed
provider registry (`internal/providerregistry`), explicit model catalogs
(archived plan 29).
**Blocks:** nothing.
**Blast radius:** LOW - thin built-in provider over existing `OpenAICompat`. Same
shape as archived plan 34 (xAI) and plan 38 (OpenAI). No empty-key path, no
loopback HTTP exception, no local daemon support.

---

## 0. Challenge and validation record

### Round 1–2 (local + cloud design)

Earlier Step 0 rounds returned CONDITIONAL PASS for a local-daemon + optional-key
design (empty key on loopback, cloud-via-local, loopback HTTP allowlist). Those
findings are **superseded** by the product scope change in Round 3.

Useful carry-overs that still apply:

| Item | Keep? |
|------|-------|
| Single provider name `ollama` | Yes |
| OpenAI-compat transport only | Yes |
| No remote model discovery | Yes |
| No default reasoning dialect / no reasoning replay until proven | Yes |
| Factory errors must not name env vars | Yes |
| Explicit TOML model catalog | Yes |
| `AllowsEmptyAPIKey` / `CredentialOK` / loopback HTTP | **No — dropped** |
| Cloud via local daemon (`ollama signin`, `*:cloud` on localhost) | **No — dropped** |
| Local daemon (`http://127.0.0.1:11434`) | **No — dropped** |

### Round 3 (product scope lock)

**Operator decision:** no local Ollama daemon. Cloud only, with a real API key.

| Concern | Disposition |
|---------|-------------|
| Local daemon | **Out of scope** for this plan |
| Cloud via local daemon | **Out of scope** |
| Auth | **Required** `OLLAMA_API_KEY` (same fail-closed path as deepseek/openrouter/zai) |
| Default base URL | `https://ollama.com/v1` |
| Empty-key / loopback HTTP policy work | **Cut** — not needed |

---

## 0.1 Research summary

### What mivia has today

| Piece | Fact |
|-------|------|
| Completer | `internal/provider` — `Chat` / `ChatStream` / `ChatTurn` |
| Transport | `OpenAICompat` → POST `{baseURL}/chat/completions`, SSE, tools, Bearer |
| Built-ins | `deepseek`, `openrouter`, `zai` only (closed registry) |
| Auth | Non-empty API key required at factory, doctor, catalog selectable, chat entry |
| URL policy | HTTPS unless `MIVIA_ALLOW_INSECURE_HTTP=1` |
| Catalog | Explicit TOML `models[]`; no remote discovery |

Thin-provider templates: archived plan 34 (xAI), plan 38 (OpenAI).

### Ollama Cloud (in scope)

Sources: [Cloud](https://docs.ollama.com/cloud),
[Authentication](https://docs.ollama.com/api/authentication),
[OpenAI compatibility](https://docs.ollama.com/api/openai-compatibility).

| Item | Value |
|------|--------|
| OpenAI-compat base | `https://ollama.com/v1` |
| Auth | `Authorization: Bearer $OLLAMA_API_KEY` |
| Key source | [ollama.com/settings/keys](https://ollama.com/settings/keys) |
| Chat path | `POST /v1/chat/completions` (SSE stream, tools, `reasoning_effort`) |

### Explicitly not in product scope

| Mode | Why cut |
|------|---------|
| Local daemon `http://localhost:11434/v1` | Operator does not want daemon |
| Cloud via local (`ollama signin` + `*:cloud` on loopback) | Cloud only via API key |
| Native `/api/chat` NDJSON | Second client; OpenAI path is enough |

---

## 1. Goal

Add first-class built-in provider **`ollama`** that talks to **Ollama Cloud** at
`https://ollama.com/v1` with a required **`OLLAMA_API_KEY`**.

Reuse `OpenAICompat`. Do not add a second HTTP stack, native client, remote model
discovery, empty-key auth, or loopback HTTP exceptions.

---

## 2. Why a registered provider

The factory and descriptor registries are **both closed**:

- `provider.NewForProvider` rejects names outside `builtinFactories`.
- `register()` refuses a factory without a matching `Descriptor`.
- `config.normalizeProviderConfigs` rejects unknown `[providers.*]` names at load.

Even though the API is OpenAI-compatible, mivia needs the descriptor + factory +
registration triplet.

---

## 3. Locked design decisions

| Concern | Decision |
|---------|----------|
| Provider identity | Single name `ollama` |
| Transport | OpenAI-compat only (`OpenAICompat` → `/chat/completions`) |
| Default URL | `https://ollama.com/v1` |
| Default key env | `OLLAMA_API_KEY` |
| Auth | **Required** non-empty key — same gates as other built-ins |
| Authorization header | Existing always-on `Bearer` (key always present at construction) |
| Loopback / empty-key policy | **None** — do not add |
| Local daemon | **Out of scope** |
| Cloud-via-local daemon | **Out of scope** |
| Model discovery | **None** — explicit TOML catalog |
| Reasoning default | No provider default dialect; model TOML may set `reasoning_dialect = "openai"` |
| Reasoning replay | Off until proven required |
| Error parser | Default OpenAI parser (status + type only) |
| Native `/api/*`, embeddings, Responses API, vision host messages | Out of scope |

### Operator profile (only one)

```toml
[provider]
name = "ollama"

[providers.ollama]
models = [
  { name = "gpt-oss:120b", context_window_tokens = 128000 },
  # Optional thinking model:
  # { name = "gpt-oss:20b", context_window_tokens = 128000,
  #   reasoning_efforts = ["low", "medium", "high", "max"],
  #   reasoning = "medium", reasoning_dialect = "openai" },
]
default_model = "gpt-oss:120b"
api_key_env = "OLLAMA_API_KEY"
base_url = "https://ollama.com/v1"
```

```bash
export OLLAMA_API_KEY=...   # required
mivia doctor
mivia chat --provider ollama -p "hi"
```

Confirm example model names and `context_window_tokens` at implement time against
the current Ollama Cloud catalog. Prefer conservative windows if unsure.

---

## 4. Ground truth

No special auth or URL policy gaps for cloud-only Ollama. Existing fail-closed
key and HTTPS rules already fit.

What already works:

- Tool calls + SSE streaming on OpenAI-compat
- Explicit model catalog and context budgets
- Retry transport (429/5xx) for cloud rate limits
- `base_url` / `api_key_env` overrides (operator may point at a compatible HTTPS
  proxy; not a second product mode)

---

## 5. Implementation design

### 5.1 Descriptor

`internal/providerregistry/registry.go` — no new Descriptor fields:

```go
"ollama": {
	Name: "ollama", DefaultModel: "gpt-oss:120b",
	DefaultURL: "https://ollama.com/v1", DefaultAPIKeyEnv: "OLLAMA_API_KEY",
},
```

Update `registry_test.go` (hard-codes three provider names today).

### 5.2 Factory

`internal/provider/ollama.go` (new):

```go
func NewOllama(opts Options) (Completer, error) {
	base := opts.BaseURL
	if base == "" {
		d, ok := providerregistry.Lookup("ollama")
		if !ok {
			return nil, fmt.Errorf("provider %q has no built-in descriptor", "ollama")
		}
		base = d.DefaultURL
	}
	return NewOpenAICompatWithOptions(CompatOptions{
		Name:              "ollama",
		BaseURL:           base,
		APIKey:            opts.APIKey,
		CacheUsageEnabled: opts.CacheUsageEnabled,
		// No default Reasoning dialect.
		// No RequiresReasoningReplay.
	}), nil
}
```

Register in `registerBuiltins` next to deepseek/openrouter/zai.

### 5.3 Auth

**No new predicate.** Keep existing gates:

| Site | Behavior (unchanged for ollama) |
|------|----------------------------------|
| `provider.NewForProvider` | Fail if key empty |
| `config.resolveProviderRuntimes` | Selectable only when key set |
| `cli` doctor | Not ready when key missing |
| `cli.runConfiguredChatOnce` | Refuse when `!APIKeySet` |

Factory errors stay env-name-free (`missing API key for provider %q`). Doctor and
chat may name `OLLAMA_API_KEY`.

### 5.4 Authorization header

No change. Key is always non-empty when a completer is built. Existing
`Authorization: Bearer …` stays byte-identical for other providers.

### 5.5 URL policy

No change. Default is HTTPS. Do not add loopback HTTP exceptions for ollama.

### 5.6 Reasoning (optional, model-declared)

Ollama Cloud OpenAI-compat accepts `reasoning_effort` (`high` / `medium` / `low` /
`max` / `none`). That matches dialect `openai`.

Do **not** add `ollama` to `reasoning.defaultDialects` until product-vetted.
Operators declare efforts + dialect on the model entry when needed.

### 5.7 Catalog

No change to Selectable rules. Without key: group visible, not selectable,
`DisabledReason = "credential unavailable"`.

---

## 6. Example config and env

### 6.1 `.mivia/mivia.toml.example`

```toml
[providers.ollama]
# Ollama Cloud (OpenAI-compatible). Requires OLLAMA_API_KEY.
# Keys: https://ollama.com/settings/keys
# Confirm model names against the current cloud catalog before shipping.
models = [
  { name = "gpt-oss:120b", context_window_tokens = 128000 },
]
default_model = "gpt-oss:120b"
api_key_env = "OLLAMA_API_KEY"
base_url = "https://ollama.com/v1"
```

Update `[provider]` comment:

```toml
# Supported: deepseek (default), openrouter, zai, ollama
```

### 6.2 `.env.example`

```text
OLLAMA_API_KEY=
```

Comment: required for provider `ollama` (Ollama Cloud).

### 6.3 Owned docs

Update `docs/product/config.md`:

- Defaults table row for Ollama Cloud
- Required `OLLAMA_API_KEY`
- Default `https://ollama.com/v1`
- No local daemon mode in this release
- Explicit catalog; no remote discovery
- Honest `context_window_tokens` per cloud model

---

## 7. Files to touch

| File | Change |
|------|--------|
| `internal/providerregistry/registry.go` | Descriptor |
| `internal/providerregistry/registry_test.go` | Names list / lookup |
| `internal/provider/ollama.go` | Factory |
| `internal/provider/ollama_test.go` | Default URL, Bearer key, override base URL |
| `internal/provider/provider.go` | `registerBuiltins` |
| `internal/provider/provider_test.go` | Available-provider list includes `ollama` |
| `.mivia/mivia.toml.example` | Example block + supported comment |
| `.env.example` | `OLLAMA_API_KEY` |
| `docs/product/config.md` | Operator guide |

Do **not** change: `validateBaseURL`, empty-key paths, Authorization omit logic,
doctor optional-key UX, `chat_command` credential predicate.

Do **not** pre-add a live `[providers.ollama]` before the descriptor ships.

---

## 8. Implementation waves (TDD)

### Wave 1 - Register ollama

1. RED/GREEN: registry contains `ollama` with cloud default URL and `OLLAMA_API_KEY`.
2. RED/GREEN: `NewOllama` uses default URL; override honored; key required at
   `NewForProvider`.
3. RED/GREEN: empty key still fails closed (same as other providers).
4. RED/GREEN: factory errors remain free of env var names.
5. `registerBuiltins`; update hard-coded name lists.

Wave gate: `go test ./internal/providerregistry/... ./internal/provider/...`

### Wave 2 - Config surfaces + docs

1. Example TOML + `.env.example` + `docs/product/config.md`.
2. Empty reasoning dialect matches absent `DefaultDialect("ollama")`.
3. Catalog/doctor use existing key rules (no new code if defaults work).

Wave gate: config/cli tests still green; docs-check if required.

### Wave 3 - Manual smoke

```bash
export OLLAMA_API_KEY=...
# [provider] name = "ollama" with example catalog
mivia doctor
mivia chat --provider ollama -p "Reply with the word pong only."
```

Without key: doctor not ready; chat refuses.

If tool calls misbehave on a model, document model choice; do not invent a native
API path.

### Wave 4 - ADLC audit + verify

- Hostile check: empty key never builds a completer; no accidental empty-key path.
- `make verify` / package tests as required.
- Commit: `feat(agent): add ollama cloud provider`

---

## 9. Security and privacy

| Risk | Mitigation |
|------|------------|
| Missing key | Existing fail-closed gates |
| Workspace TOML points `base_url` at attacker with real key | Pre-existing accepted class; document |
| Provider error text leak | Default OpenAI parser (status/type only) |
| Cloud egress of workspace content | Document: all ollama traffic goes to cloud |
| Secrets in logs | Never log Authorization; synthetic keys in tests |

---

## 10. Out of scope (explicit)

- Local Ollama daemon (`localhost:11434`)
- Cloud via local daemon (`ollama signin`, `*:cloud` on loopback)
- Empty API key / optional auth
- Loopback HTTP allowlist for ollama
- Native `/api/chat` / NDJSON client
- Live model discovery (`/api/tags`, `/v1/models`)
- `ollama pull` / model management CLI
- Multimodal vision messages in mivia history
- Embeddings / OpenAI Responses API
- Second provider name `ollama-cloud`
- Default reasoning dialect table entry for ollama
- Changing the explicit TOML catalog product rule

A later plan may add local daemon support; it must re-run Step 0 for empty-key and
HTTP policy (do not revive Round 1 design without re-challenge).

---

## 11. Open decisions (dispositioned)

| # | Decision | Disposition |
|---|----------|-------------|
| 1 | Local daemon | **Out of scope** |
| 2 | Cloud via API key only | **Locked** |
| 3 | Empty-key / loopback policy | **Not required** |
| 4 | Default URL | **`https://ollama.com/v1`** |
| 5 | Example model names / windows | Implement-time against current cloud catalog |
| 6 | `tool_choice` | Smoke-gated; `OmitToolChoice` only if live cloud rejects it |
| 7 | Commit scope | `feat(agent): …` |

---

## 12. Verification checklist

Automated:

- [ ] `go test ./internal/providerregistry/...`
- [ ] `go test ./internal/provider/...`
- [ ] Hard-coded provider name lists updated
- [ ] Empty key fails closed for ollama
- [ ] Factory errors contain no env var names
- [ ] `go build ./... && go vet ./...`
- [ ] No secrets or raw provider error text in fixtures

Manual:

- [ ] `mivia doctor` OK with `OLLAMA_API_KEY` set
- [ ] Chat completes against Ollama Cloud
- [ ] Without key: doctor not ready; chat refuses
- [ ] Agent tool turn on a tool-capable cloud model

---

## 13. Rollback

Additive provider. Remove descriptor, factory, registration, and example config.
No data migration. Sessions bound to `ollama/...` fail model resume until the
provider returns.

---

## 14. Sequencing vs other provider plans

| Plan | Relation |
|------|----------|
| 38 OpenAI / archived 34 xAI | **Best template** — thin factory + required key |
| 31 Kimi | Do not copy reasoning-history work unless proven |
| 46 prompt caching | No request markers; optional usage capture only |

Independent of OpenAI/xAI registration.

---

## 15. Step 0 status

Round 3 product scope supersedes Round 1–2 local-auth design. Implementation is
a thin registered OpenAI-compat provider. No further challenge required before
Wave 1 unless HEAD auth gates change.

---

## 16. Outcome expected after ship

```bash
export OLLAMA_API_KEY=...
# [provider] name = "ollama"
# base_url defaults to https://ollama.com/v1
mivia doctor
mivia chat -p "hi"   # POST https://ollama.com/v1/chat/completions
```

No local `ollama serve` required.
