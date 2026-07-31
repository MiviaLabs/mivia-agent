# 05.4 — CLI, prompt gates, and dispatcher integration

**Status:** BLOCKED on final-registry and plan-07 atomicity decisions.
**Goal:** Select named agents and run many correctly scoped instances across every dispatcher construction path.
**Depends on:** phases `01`–`03`.
**Must land atomically with:** plan `07` task binding and handler-routing work.

## Scope and ordering

Load skills before agent-definition validation so agent-versus-skill collisions
can be checked. Preserve the `useTools`/`--no-tools` behavior: pure-chat startup
must not become fatal merely because a workspace skill is malformed.

Apply workspace prompt stripping and source provenance before prompt selection or
provider construction. The user-only gate controls workspace agent files,
`[chat].system_prompt`, `[subagents].system_prompt`, and workspace skill
handlers. `.mivia/agent-prompt.md` remains a separately documented, repo-owned
surface.

Construct one final registry per invocation, then build every handler and
dispatcher from that registry. Route initial startup and model switches through
the same agent-aware builder; `internal/cli/model_binding.go` is mandatory.

## Owned production files

- `internal/cli/chat_command.go`, `chat_repl.go`, `dispatcher.go`;
- `internal/cli/model_binding.go`;
- `internal/cli/root.go`, `dispatch.go`, and `orchestrate.go`;
- `internal/cli/agent_definitions.go` and, if needed for structure limits,
  `internal/cli/agent_handlers.go`.

There is one explicit `agent` binding field. Do not add or preserve a separate
configuration-level `role` field or role/handler precedence. Plan `07` must not
duplicate handler construction.

## TDD and focused tests

Own integration RED tests before wiring:

- `TestAgentNameCollidesWithSkill`;
- `TestRootSession_AgentFlag` and `TestRootScopedRegistry_AfterAttach`;
- `TestRootSession_AgentFlagUnknownName`;
- `TestAgentScopedLoopCannotWriteFile`;
- built-binary `mivia chat --agent <name>` coverage;
- workspace prompt and skill-handler gate tests;
- model-switch generation coverage proving scope and prompt provenance survive
  dispatcher rebuilds;
- many concurrent instances from one definition, including cancellation.

Mutation proofs M10 and M13–M16 belong here. Tests must prove execution denial,
not only registry contents.

## Exit criteria

Initial chat, one-shot/TUI paths, and model switching use the same agent-aware
construction contract; workspace content is gated before prompts or handlers;
and no instance can regain excluded tools through a stale registry pointer.
