# 05 — Agent model core: named agents in TOML

**Status:** **BLOCKED** — the original role-based design was rejected. This
overview defines the corrected agent-only model and its delivery phases.
**Date:** 2026-08-02
**Depends on:** shipped plans `01`, `04`, and `27`.
**Blocks:** `06`, `07`.
**Blast radius:** HIGH — agent definitions control prompts and tool exposure.

## Goal

Define a collection of named agent definitions. Each agent definition is one
TOML file containing its prompt, tools, model, and limits. The runtime may spawn
any number of disposable instances from the same definition concurrently.

There is no role concept, role collection, role-to-agent attachment, or separate
role binding. `researcher.toml` is one agent definition; every `researcher`
instance uses that definition.

## Phase map

| Phase | Goal | Depends on |
|---|---|---|
| [01 — config trust and schema](01-config-trust-and-schema.md) | Discover safe, presence-preserving agent files and trusted global settings | `01`, `04`, `27` |
| [02 — tool primitives](02-tool-primitives-and-scope.md) | Provide catalogue, scope, and immutable-registry primitives | `01` |
| [03 — agent definition resolution](03-agent-definition-resolution.md) | Resolve files into validated immutable agent definitions | `01`, `02` |
| [04 — CLI and dispatcher integration](04-cli-and-dispatcher-integration.md) | Select agents, build handlers, gate workspace content, preserve scope across model switches | `01`–`03`; atomic with `07` |
| [05 — verification and closeout](05-verification-and-closeout.md) | Run cross-surface gates and update the control-surface pointers | `01`–`04` |

Tests are authored in the phase that owns their boundary (RED before production
work). Phase 05 runs the complete suite; it does not defer all tests to the end.

## Storage model

Global settings remain in the trusted user file:

```text
~/.mivia/mivia.toml
```

It contains `[agents]` global settings only: `load_workspace_config` and the
guardrails. It does not contain `[[agents.roles]]` or agent definitions.

Each agent definition is its own top-level TOML file:

```text
~/.mivia/agents/researcher.toml          # trusted user agent
<workspace>/.mivia/agents/researcher.toml # untrusted workspace agent
```

The normalized filename is the canonical agent name. The file must contain a
matching `name`; a mismatch is fatal. The built-in root fallback is private
runtime behavior, not a selectable or inheritable named agent, so every named
agent is file-backed.

The loader publishes an immutable snapshot. Handlers may be reused for many
invocations, but every invocation gets a fresh loop, dispatcher, and scoped
registry derived from that snapshot. No published definition or shared registry
is mutated after publication.

## Source precedence and trust

1. Private compiled fallback for an otherwise unconfigured root session.
2. User agent files under `~/.mivia/agents/`; always loaded.
3. Workspace agent files under `<workspace>/.mivia/agents/`; loaded only when
   the user file's `load_workspace_config = true` enables them.
4. The `--agent <name>` selection chooses one definition for the root session;
   dispatch tools use the same agent names for spawned instances.

The workspace cannot set or loosen the global gate or guardrails. Workspace
`[agents]` values in `mivia.toml` are ignored with a warning. User guardrails are
read only from `~/.mivia/mivia.toml` at its fixed path. `.mivia/agent-prompt.md`
remains a separately documented repo-owned prompt surface and is not silently
claimed to be covered by the agent-file gate.

If user and workspace agent directories resolve to the same directory after
`filepath.EvalSymlinks` and `filepath.Clean`, treat the files as user files only;
never reinterpret trusted files as workspace files. Workspace files cannot
replace same-named user files: warn with both paths and ignore the workspace
file.

Agent discovery is fail-closed for path escapes. Reject symlinked agent
directories and files, non-regular files, hardlink ambiguity where identity
cannot be verified, and replacement races between discovery and read. Reads
must verify the opened file remains beneath the intended source directory.

## Agent definition schema

Each file uses top-level keys:

```toml
name             = "researcher"
description      = "Use for codebase exploration, locating code."
tools            = ["read_file", "grep", "glob", "list_dir"]
disallowed_tools = ["run_command"]
model            = "glm-4.5-air"
max_turns        = 12
system_prompt    = """
You are a read-only research agent. Search, read, summarize. Never edit.
"""
```

Optional fields preserve presence with pointers. `tools_add` and
`tools_remove` are applied after inheritance and are mutually exclusive with an
explicit `tools` list. Unknown fields, empty required strings, zero
`max_turns`, cycles, and unknown parents are fatal. Agent descriptions are
sanitized before entering model-facing tool descriptions.

`skills` remains blocked until plan `06` supplies a real enforcement point. It
must be rejected in `05`, or `05` and `06` must land atomically. It may not be
accepted and silently ignored.

Inheritance is between file-backed agent definitions only and must have explicit
trust rules: a workspace definition must not use inheritance to widen a user
guardrail or bypass the workspace gate. The private compiled fallback is not a
public parent name. Missing parents, source-boundary violations, parent changes,
and cycles are tested and fail closed.

## Namespace and selection

Agent names share a dispatcher namespace with built-in handlers and skills.
Normalize names consistently, reject invalid filenames, and reject collisions
with skills or reserved handlers with both source paths in the error. There is
one explicit `agent` selection field in dispatching APIs; do not retain a
separate configuration-level `role` field or `role > handler` precedence.

The same named definition may back many concurrent runtime instances. A race
test must fan out multiple instances from one definition, exercise cancellation,
and prove that a model-switch generation cannot mutate an in-flight instance's
captured definition or registry.

## Required acceptance coverage

- one-file parsing, filename/name agreement, unknown-key rejection, and
  user/workspace source provenance;
- user-only gate and guardrails, workspace values ignored, same-directory/home
  handling, symlink and replacement-race refusal;
- inheritance, deltas, cycles, source-boundary rules, empty toolsets, catalogue
  validation, sanitization, and namespace collisions;
- root and spawned registry scope, dispatch-boundary denial, and many-instance
  race coverage;
- `mivia chat --agent <name>`, model switching, resume/idempotency behavior,
  and an execution-level denial test for excluded tools.

Allocate the invariant ID at implementation landing; `INV-AG-28` is already in
use. Never reserve a number in this planning document.

## Implementation files

| File | Responsibility |
|---|---|
| `internal/config/agents.go` | Global settings and fixed-directory file discovery |
| `internal/agents/agent.go` | `AgentSpec`, `ResolvedAgent`, provenance, immutable catalog |
| `internal/agents/resolve.go` | Inheritance and effective-definition resolution |
| `internal/agents/catalogue.go` | Name, source, filename, and catalogue validation |
| `internal/agents/policy.go` | Guardrail evaluation; delegates scope filtering to `internal/tools` |
| `internal/tools/names.go` / `scope.go` | Tool catalogue and scope primitives |
| `internal/subagents/names.go` | Reserved handler names |
| `internal/cli/agent_definitions.go` | Layer-B load/selection wiring |
| `internal/cli/agent_handlers.go` | Layer-C handler construction, if needed to preserve file/function limits |
| `internal/cli/model_binding.go` | Agent-aware model-switch dispatcher construction |

Plan `07` must not duplicate handler construction. Its task-binding work and
this plan's agent registry must land as one enforced boundary.
