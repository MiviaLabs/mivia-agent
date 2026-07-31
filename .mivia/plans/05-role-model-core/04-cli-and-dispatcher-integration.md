# 05.4 — CLI, prompt gates, and dispatcher integration

**Status:** BLOCKED on the final-registry and plan-07 atomicity decisions.
**Parent:** [`00-overview.md`](00-overview.md) §§5–8, 9–10.
**Depends on:** phases `01`–`03`.
**Must land atomically with:** the role-routing work in plan `07` wherever
per-role spawned-handler registration or task binding is involved.

## Scope and ordering

Load skills before Layer-B role validation so role-versus-skill collisions can
be checked. Preserve the `useTools`/`--no-tools` behavior: pure-chat startup
must not become fatal merely because a workspace skill is malformed, and roles
are not resolved when tools are disabled.

Apply workspace prompt stripping and source provenance before prompt selection or
provider construction. The same user-only gate controls workspace roles,
`[chat].system_prompt`, `[subagents].system_prompt`, and workspace skill
handlers. `.mivia/agent-prompt.md` remains repo-owned and explicitly ungated;
that distinction is documented, not silently omitted.

Construct one final registry, then build every handler and dispatcher from that
registry. Do not attach a dispatcher to one registry and replace `sess.Tools`
afterward. Route both initial startup and model switches through the same
role-aware builder; `internal/cli/model_binding.go` is a required production
path.

## Owned production files

- `internal/cli/chat_command.go`, `chat_repl.go`, `dispatcher.go`;
- `internal/cli/model_binding.go`;
- `internal/cli/root.go`, `dispatch.go`, and `orchestrate.go`;
- a new `internal/cli/role_handlers.go` if needed to keep Layer-B loading and
  Layer-C registration separate;
- the explicitly required call-site updates in interactive and delegation
  tests.

Per-role spawned-handler ownership must be reconciled with plan `07` before
implementation. If `07` remains the owner, this phase owns the common builder
and root scope only and the two plans ship as one release. If 05 owns it, amend
07 to remove the duplicate registration design first.

## TDD and focused tests

Own the integration RED tests before wiring:

- `TestRoleNameCollidesWithSkill`;
- `TestRootSession_AgentFlag` and `TestRootScopedRegistry_AfterAttach`;
- `TestRootSession_AgentFlagUnknownName`;
- `TestRoleScopedAgentCannotWriteFile`;
- the built-binary `mivia chat --agent <role>` test;
- `TestWorkspaceSystemPromptStrippedWhenGateOff`;
- `TestUserConfigSystemPromptAlwaysLoaded`;
- `TestWorkspaceSubagentSystemPromptStrippedWhenGateOff`;
- `TestWorkspaceSkillHandlersNotRegisteredWhenGateOff`;
- `TestWorkspaceSkillHandlersRegisteredWhenGateOn`;
- a model-switch regression test proving role scope, prompt provenance, and
  final registry identity survive dispatcher rebuilds.

Mutation proofs M10 and M13–M16 belong here. The integration test must prove
execution denial, not only registry contents.

## Exit criteria

Initial chat, one-shot/TUI paths, and model switching all use the same role-aware
construction contract; workspace content is gated before it reaches prompts or
handler registration; and no route can regain excluded tools through a stale
registry pointer or the unscoped default handler.
