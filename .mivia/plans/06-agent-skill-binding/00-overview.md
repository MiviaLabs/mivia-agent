# 06 — Agent–skill binding

**Status:** DESIGN — blocked until plans `05` and `07` expose the agent-selection and task-binding seams.
**Goal:** Restrict which skills each named agent may invoke, with real metadata and per-instance enforcement.
**Depends on:** `05-agent-model-core`.
**Coordinates with:** `07-agent-routing` for the explicit `agent` task field and handler-bypass prevention.
**Blast radius:** MODERATE.

## Semantics

The `skills` key belongs in an individual agent file, for example
`~/.mivia/agents/researcher.toml`. It is an invocation allowlist, not a preload:

```toml
skills = ["bug-audit"] # this agent may fan out to this skill
```

For a root definition, omitted means all trusted available skills; explicit `[]`
means none. For an inherited definition, omission inherits the parent and only
the root definition's omission means all. Skills remain delegated handlers with
their existing untrusted-content wrapper. There is no role-level skill object
and no skill collection embedded in `mivia.toml`.

## Enforcement scope

The gate is enforced at every path where a skill name becomes a task, using the
selected immutable agent snapshot rather than a global or stale handler registry.
The binding must cover `dispatchTasksTool.buildTasks`, `spawnAgentTool.Execute`,
resume/retry, and any handler path that can synthesize a skill task. An explicit
`agent` field is authoritative; a legacy `handler` field must not bypass it.

The v1 capability boundary is root fan-out unless plan 07 proves nested skill
invocation exists and wires the same check there. If nested invocation is not
enforceable, the field is either documented as root-only or removed; it must not
be presented as a general capability gate.

The companion check `Definition.Tools ⊇ skill.Tools` is required only after
skill frontmatter metadata is parsed and published. It must not pass vacuously.

## Phase map

| Phase | Goal | Depends on |
|---|---|---|
| [01 — skill metadata](01-skill-metadata.md) | Parse and publish validated skill tool metadata | `25` |
| [02 — allowlist resolution](02-agent-allowlist-resolution.md) | Resolve file-backed `skills` with inheritance, provenance, and trust | `01`, `05` |
| [03 — runtime enforcement](03-runtime-enforcement.md) | Enforce the selected agent's skill set at every reachable task boundary | `02`, `07` |
| [04 — verification and closeout](04-verification-and-closeout.md) | Prove isolation, shadowing resistance, lifecycle behavior, and gates | `03` |

## Changes

| Site | File | Change |
|---|---|---|
| Skill metadata | `internal/skills/loader.go`, `internal/skills/skills.go` | Parse and publish tool metadata |
| Allowlist resolution | `internal/agents/policy.go` | Resolve source, inheritance, nil/empty semantics, and provenance |
| Runtime boundary | `internal/cli/agent_skill_policy.go`, `dispatch.go`, `orchestrate.go` | Reject disallowed skills without handler bypass |
| Scope construction | `internal/cli/agent_handlers.go`, `internal/subagents/multi_step.go` | Derive immutable per-instance scope |

## Verification

- `TestSkillToolsParsedAndPublished` and `TestSkillToolsSubsetOfAgentTools`;
- `TestAgentSkillAllowlist_OmittedAllowsAll`, `_EmptyAllowsNone`, and inherited
  omission semantics;
- `TestUnknownSkillRejected`, `TestWorkspaceSkillCannotShadowUserBinding`, and
  `TestWorkspaceGateRequired`;
- `TestAgentSkillAllowlist_PerInstance`, `TestSkillCannotBypassAgentSelection`,
  `TestAgentSkillBindingSurvivesModelSwitch`, and
  `TestResumeRechecksAgentAccess`;
- mutation proofs for dropping the allowlist, treating `[]` as all, bypassing
  the selected agent, and skipping the tool-subset check;
- `go test ./internal/agents/... ./internal/skills/... ./internal/cli/... -race`;
- `make verify`, `make invariants`, `make validate-invariants`, and `make race`.

If root-only enforcement proves too confusing, remove `skills` from v1 rather
than shipping a misleading field. It can return when nested agents receive a
delegation capability.
