# 43.2 — Enforce compatibility against the live runtime surface

**Status:** Planned
**Depends on:** `01-static-skill-metadata.md`
**Goal:** Ensure declared requirements are checked against what the invocation can actually use.

## Scope

Align these runtime paths around one policy seam:

- `internal/cli/agent_skill_policy.go`
- `internal/cli/task_routing.go`
- `internal/cli/dispatcher.go`
- `internal/cli/agent_task_handler.go`
- `internal/cli/slash_catalog.go` and its handler path
- `internal/cli/agent_definitions.go`
- `internal/agents/skill_policy.go`
- `internal/skills/loader.go`

The policy must evaluate the selected skill definition after origin resolution,
the active tool registry after disable/deny filtering, and the selected agent's
skill allowlist. Avoid a second, subtly different slash-only implementation.

## Decisions to implement

1. **Live registry:** a skill is invocable only when every declared static tool
   is present in the final scoped registry. An agent TOML list is an input, not
   proof that the live registry contains the capability.
2. **Slash activation:** direct skill slash activation uses the same tool and
   skill policy check. The unrestricted compiled root remains allowed because
   it intentionally owns the full workspace catalogue.
3. **Origin precedence:** preserve the existing security intent that a trusted
   user skill cannot be silently replaced by a project skill. Use one resolved
   origin/definition for catalogue, runtime, and slash lookup, and emit a
   warning or fail closed on an ambiguity.
4. **Resources:** `read_skill_resource` remains an invocation-scoped capability
   injected only for a manifest-approved resource. Its presence is checked in
   the resource activation path, not treated as a static agent tool.

## Tests first

Extend focused tests in:

- `internal/agents/skill_policy_test.go`: static tool superset and unknown
  tool behavior against effective tools.
- `internal/cli/agent_skill_policy_test.go`: disabled/denied live-tool cases,
  root exception, and explicit empty allowlist behavior.
- `internal/cli/agent_routing_test.go` and dispatcher tests: route-time and
  handler-time rechecks.
- `internal/cli/slash_catalog_test.go` and skill activation tests: slash skill
  cannot run with an unmet declared requirement; allowed skill still activates.
- `internal/cli/agent_definitions`/loader tests: user/project same-name skill
  precedence and identical catalogue/runtime options.
- `internal/skills/resources_test.go`: resource access remains bounded and
  does not become a persistent agent tool.

Required negative cases: a TOML-allowed tool disabled at runtime, a project
skill shadowing a user skill with different metadata, a direct slash invocation
with an unmet requirement, and a resource reader used outside its activation.

## Verification

```text
go test ./internal/skills ./internal/agents ./internal/cli -count=1
go test -race ./internal/skills ./internal/agents ./internal/cli
```

Mutation proof: bypassing the shared check in slash dispatch, validating only
the agent TOML list, or restoring project-over-user shadowing must make the
corresponding focused test fail.
