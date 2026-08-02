# Plan 37 - Implementation overview (replacement design, 2026-08-02b)

Parent plan: `../37-reasoning-effort.md` §12.

Status: APPROVED for implementation. This supersedes the blocked design whose
central premise (provider-wide sampling suppression) the 2026-08-02 re-audit
disproved.

Goal: carry a model-scoped, provider-neutral reasoning level and an explicit
wire dialect through config, model binding, and **every** request constructor,
emitting the dialect's documented fields and changing nothing else.

## What changed versus the blocked design

| Blocked design | Replacement |
|---|---|
| `SuppressSampling` removes `temperature`/`top_p` | **Deleted.** No sampling parameter is ever removed. |
| Dialect is a provider property only | Dialect is declared **per model** (`reasoning_dialect`), defaulting to a vetted per-provider dialect where one exists. |
| DeepSeek gets a dialect by default | DeepSeek has **no** default dialect (its thinking mode needs `reasoning_content` replay we do not implement). Opt-in requires an explicit `reasoning_dialect`. |
| `off` emits no field on the openai dialect | `off` emits each dialect's documented disable value. |
| Propagation covers `session.go` + `loop.go` | Propagation covers all five request constructors, including the non-stream fallback in `readStream`. |
| `ExtraBody` precedence unstated | Reasoning fields merge **after** `ExtraBody`; an active model-scoped level wins. Deterministic and tested. |

## Required order

1. Phase 01 - `internal/reasoning` (no dependencies).
2. Phase 02 - provider dialects, `Request` fields, request-body merge, constructor defaults.
3. Phase 03 - `config.ModelSpec` fields and validation.
4. Phase 04 - propagation across every request path, with integration tests.
5. Phase 05 - example config, invariant, closeout verification.

Non-goals: Responses API, `verbosity`, reasoning-content history replay,
per-model value matrices, undocumented z.ai disable workarounds.

## Cross-phase invariant

An unset level produces the pre-change request body byte-for-byte. An active
level adds exactly the resolved dialect's fields and removes nothing. An
unresolvable dialect (none configured, none defaulted) emits nothing.
