# 00 — Agent program overview

**Status:** Program index.
**Date:** 2026-08-02
**Scope:** File-backed, named agent definitions for mivia. Each agent is one
TOML file with its own prompt, tools, model binding, and optional skill policy.
The runtime may spawn any number of disposable instances from one definition.

This replaces the earlier design that mixed a role collection into
`mivia.toml`. There is no collection-level role abstraction and no
`[[agents.roles]]` table.

## Program invariants

1. **One agent, one definition file.** The canonical identity is the filename
   `<name>.toml`; the in-file `name` must match it exactly.
2. **One agent, one runtime identity.** An instance runs one immutable resolved
   definition. Many concurrent instances may use the same definition.
3. **Global config is not the definition store.** User `~/.mivia/mivia.toml`
   owns the agent gate and global guardrails. Agent definitions live under
   `~/.mivia/agents/` or the gated workspace `.mivia/agents/` directory.
4. **Workspace input only narrows privilege.** User configuration is the
   authority for whether workspace agent files load. Workspace files cannot
   enable themselves, loosen guardrails, or silently shadow a trusted user
   definition.
5. **Authorization is enforced at dispatch.** A filtered registry or prompt is
   not a privilege boundary; the dispatcher must enforce the selected agent's
   tool and skill policy.
6. **No field without an enforcement point.** Omit speculative fields rather
   than publishing configuration that silently does nothing.
7. **Compiled surfaces remain generic.** User-provided agent prompts and
   descriptions are runtime data; compiled tool descriptions and fallback
   prompts remain project- and language-generic.
8. **Trust and lifecycle are explicit.** Symlinks, ambiguous identities,
   mutable snapshots, resume authority, model switching, and idempotency are
   fail-closed concerns covered by the phase plans.

## Plan set

Each plan is a directory. Small plans use only `00-overview.md`; larger plans
contain numbered implementation phases and their own verification gate.

| # | Plan | Ships alone? | Depends on |
|---|---|---:|---|
| ✅ `01` | [Dispatch-boundary tool authorization](01-dispatch-boundary-tool-authorization.md) | shipped | — |
| ✅ `02` | [Run-handle ownership](02-run-handle-ownership.md) | shipped | — |
| ~~`03`~~ | [Agentkit embedded serving](03-agentkit-embedded-serving.md) | closed | — |
| ✅ `04` | [Workspace namespace `.mivia/`](04-workspace-namespace-mivia.md) | shipped | — |
| `05` | [Agent model core](05-agent-model-core/00-overview.md) | no | `01`, `04` |
| `06` | [Agent–skill binding](06-agent-skill-binding/00-overview.md) | no | `05`, `07` |
| `07` | [Agent routing](07-agent-routing/00-overview.md) | no | `02`, `05` |
| `08` | [Agent CLI and observability](08-agent-cli-and-observability/00-overview.md) | no | `07` |
| `09` | [Agent docs and examples](09-agent-docs-and-examples/00-overview.md) | no | `02`, `08` |

## Ordering

`01` establishes dispatch enforcement. `02` establishes run ownership. `05`
defines trusted, immutable TOML agents; `07` binds those definitions to task
selection and many-instance execution; `06` then couples skills to the same
agent identity and proves the metadata check is non-vacuous. `08` exposes the
effective snapshot and runtime identity. `09` updates owned documentation and
examples only after the behavior is settled.

Plans `05` and `07` must land atomically at their shared selection seam. Plan
`06` may not publish a `skills` field unless its runtime enforcement point is
implemented. Invariant IDs are allocated at implementation landing, not in
these design files; recompute the lowest free ID then.

## Deliberately excluded

No role collection, `can_spawn`, `max_depth`, `inherits_pool`, per-agent
provider credentials, handoff/control transfer, or any other field without a
real enforcement hook ships as reserved schema. A future capability may be
added only with a new bounded plan and a concrete dispatch boundary.

## Verification contract

Every plan gets its own hostile challenge, task validation, implementation
verification, and bug-audit record. The closeout gate for the program is:

```text
make verify
make test
make race
make docs-check
make invariants
make validate-invariants
```

The final implementation must also produce the required `mivia-report/v1` and
leave no stale role-based path or schema in active documentation.
