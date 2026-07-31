# 40 — Import Codex / ChatGPT subscription session

**Status:** DESIGN — not yet implemented.
**Date:** 2026-08-02
**Depends on:** plan 38 (openai provider descriptor + factory), plan 37 (reasoning
field), the OAuth research in §11.
**Blocks:** nothing. **Amends:** plan 38 §6 (the "no subscription auth" carve-out —
this plan fills it).
**Blast radius:** MEDIUM — reads a credential file we do not own (`~/.codex/auth.json`),
validates a JWT, sends a bearer to OpenAI's backend. Risk is credential handling and
the key insight in §3: codex auth is coupled to the `codex` runtime in ways grok's is
not.

---

## 1. Goal

Let a user with a **ChatGPT subscription** (Plus / Pro / Team / Business / Enterprise)
use their subscription from mivia by importing the session token that `codex login`
produced — the same "import, don't reimplement" model as plan 35 (grok), applied to
OpenAI's Codex CLI. mivia reads `~/.codex/auth.json`, extracts the access token, and
uses it as a bearer.

## 2. How Codex subscription auth works (research summary)

Full detail in §11. Essential facts:

- `codex login` offers "Sign in with ChatGPT" (browser OAuth, default) or
  `codex login --device-auth` (device code, RFC 8628 beta). Both produce an OAuth
  token set cached locally.
- **Storage** — `~/.codex/auth.json` (plaintext, or OS keyring via
  `cli_auth_credentials_store = "keyring"`). File format includes `auth_mode`,
  `tokens.access_token`, `tokens.id_token`, `tokens.refresh_token`,
  `last_refresh`. `auth_mode = "chatgpt"` marks a subscription session; `apikey`
  marks a static key.
- **Refresh** — automatic, in-process, before expiry. Refresh tokens are used to get
  fresh access tokens via OpenAI's token endpoint.
- **Entitlement** — the subscription (Plus/Pro/etc.) is encoded in the token's
  workspace/account claims, not in an account balance. Subscription auth unlocks
  plan features like "fast mode" and the latest models that API-key auth does not.
- **Billing** — ChatGPT sign-in uses **plan credits** (included in subscription); API
  key uses **standard API pricing**. A user on Plus drawing on subscription credits
  vs. pay-per-token is the whole point.
- **The copy problem** — GitHub issue #15502: copying `auth.json` across machines is
  **unreliable**. Refresh succeeds (`last_refresh` updates) but the session is then
  rejected. This is a known limitation, not user error. mivia's import must not assume
  a copied file works.

## 3. The critical difference from plan 35 (grok import)

Plan 35 reads `~/.grok/auth.json` and it mostly just works, because grok's token is a
bearer accepted at the inference proxy. Codex's subscription auth is **more coupled**:

| Aspect | grok (plan 35) | codex (this plan) |
|---|---|---|
| Token accepted at | inference proxy, bearer-only | OpenAI backend; may need client headers |
| Cross-machine copy | works (file is portable) | **unreliable** (issue #15502) |
| Refresh by third party | common (pi-xai-oauth etc.) | OpenAI tolerates it but it's undocumented |
| `auth_mode` field | absent | `chatgpt` vs `apikey` — must check |
| Endpoint | `cli-chat-proxy.grok.com` | OpenAI backend (model-dependent) |

**Implication:** v1 of this plan is **read-only import with explicit re-login
guidance**, the same Strategy A as plan 35. mivia does not refresh codex tokens. The
copy-file limitation means the doc must say: run `codex login` on the **same machine**
as mivia, don't copy `auth.json` across hosts.

## 4. Credential resolution and precedence

Within the `openai` provider path:

```
1. OPENAI_API_KEY env / model.api_key   ← plan 38 (static key)
2. ~/.codex/auth.json (subscription)    ← this plan (imported token)
3. (error: no credential)
```

Opt-in via config, never auto-scan `~/.codex/`:

```toml
[providers.openai.subscription]
import_from = "~/.codex/auth.json"
# When import is active and no API key is set, mivia reads the codex session.
```

When the subscription block is present and `OPENAI_API_KEY` is unset, mivia resolves
the credential from `auth.json`. The base URL stays `api.openai.com/v1` (codex tokens
are accepted at the standard endpoint, unlike grok's proxy split).

## 5. Token validation

On resolve:

1. Read `~/.codex/auth.json`. If absent → "run `codex login` first, or set
   `OPENAI_API_KEY`".
2. Check `auth_mode == "chatgpt"`. If `apikey` → treat as a regular API key (or
   ignore; the user should use `OPENAI_API_KEY` directly). Refuse to silently use an
   `apikey`-mode file as a subscription token.
3. Extract `tokens.access_token`. Decode JWT claims (no signature verification — we
   are the consumer) to read `exp` and workspace for diagnostics.
4. If `exp` past → "session expired; run `codex login` and retry". Do not refresh
   (§3).
5. If within 5-min window → warn at startup, proceed.

## 6. Reasoning

Consumed via plan 37 — same as plan 38. Codex subscription users get GPT-5.x models
with `reasoning_effort` support. The user sets `reasoning_effort` in `[chat]` and the
shared adapter stamps it; the import path changes nothing about reasoning.

## 7. The `mivia login --codex` shim (optional)

Like plan 35's `mivia login --grok`, a convenience that shells out to `codex login`
(non-interactive device-auth if no browser). Not mivia's own login flow; delegates to
the codex CLI, which owns the OAuth client.

## 8. Security considerations

| Concern | Mitigation |
|---|---|
| Reading another tool's credential file | Opt-in only (§4); never scan `~/.codex/` without config |
| Cross-machine copy unreliable | Document: run `codex login` on the same host as mivia (§3) |
| Token plaintext on disk | codex's choice; we neither weaken nor improve it. Document keyring option (`cli_auth_credentials_store = "keyring"`) |
| `auth_mode` confusion | Refuse `apikey`-mode files as subscription tokens (§5) |
| Token in process memory | Same as any API key; INV-AG-5 covers previews; never log |

## 9. Verification

- `go test ./internal/provider/...` — `codex_subscription_test.go`: resolve from a
  fixture `auth.json`, check `auth_mode`, decode `exp`, fail-closed on expired /
  missing / `apikey`-mode / malformed
- `go test ./internal/config/...` — `[providers.openai.subscription]` parses
- `go build ./... && go vet ./...`
- Manual: `codex login` → `mivia --provider openai` (with subscription block)
  completes a turn using plan credits

## 10. Invariant

A new row (next free `INV-AG-33`): *Codex subscription import is opt-in via
`[providers.openai.subscription].import_from`; mivia never scans `~/.codex/` without
it. An imported token is read-only — mivia does not write `auth.json` and does not
refresh. A file with `auth_mode != "chatgpt"` is refused as a subscription credential.
An expired token fails closed with a re-login instruction.*

## 11. Appendix — Codex subscription auth (research)

**Acquisition** — `codex login` browser flow (default) or `codex login --device-auth`
(RFC 8628, beta). OAuth 2.1 Authorization Code + PKCE, loopback callback on
`localhost:1455` (overridable). "Sign in with ChatGPT" draws on plan credits;
"Provide your own API key" uses standard API pricing.

**Storage** — `~/.codex/auth.json` (plaintext) or OS keyring
(`cli_auth_credentials_store = "file" | "keyring" | "auto"`). File shape includes
`auth_mode` (`chatgpt` | `apikey`), `tokens.{access_token, id_token,
refresh_token}`, `last_refresh`.

**Refresh** — automatic, in-process, before expiry, using the refresh token against
OpenAI's token endpoint. Active sessions usually continue without re-login.

**Copy limitation** (issue #15502) — copying `auth.json` to another machine: refresh
runs and `last_refresh` updates, but the resulting session is rejected
("authentication session could not be refreshed automatically"). The limitation
applies to both `CODEX_HOME` sharing and cross-machine reuse. **Run `codex login` on
the same host as mivia.**

**Access tokens** — `CODEX_ACCESS_TOKEN` env / `codex login --with-access-token` for
CI. These are workspace-scoped agent credentials, distinct from Platform API keys.
Documented as the CI/CD path.

**Entitlement** — subscription auth unlocks plan features (fast mode, latest models
like gpt-5.5 sign-in-only) that API-key auth does not. The subscription tier
(Plus/Pro/Team/Business/Enterprise) is encoded in the token's workspace principal.

## 12. Rollback

If imported-token auth proves unreliable (session rejection, scope drift), the
fallback is plan 38 (API key). The subscription import is additive config — removing
the `[providers.openai.subscription]` block returns the user to the key path. The
copy-file limitation (§3) means this plan's UX is inherently more fragile than plan
35's; document it prominently.

## 13. Sequencing

1. `internal/provider/codex_auth.go` (new) — `resolveCodexSession(path)` reads
   `auth.json`, checks `auth_mode`, decodes JWT claims, returns `(token, error)`
2. `internal/config/types.go` — reuse the `SubscriptionConfig` shape from plan 35 on
   the openai provider section
3. `internal/config/load.go` — when subscription import is active and no API key,
   resolve the token
4. `internal/provider/openai.go` — accept a resolved token via `Options` (same path
   plan 35 adds for grok)
5. `internal/provider/codex_subscription_test.go` — fixture-based resolve + expiry
6. `mivia login --codex` shim (optional, §7)
7. Docs + `mivia.toml.example` subscription block + copy-file warning
8. Invariant `INV-AG-33`

Land **after** plans 37 (reasoning) and 38 (openai provider).
