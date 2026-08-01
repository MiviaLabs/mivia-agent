# 43.1 - Declare and validate static skill tools

**Status:** Planned
**Depends on:** `00-overview.md`
**Goal:** Make the existing skill/tool check non-vacuous and fail closed on invalid static metadata.

## Scope

Update the eight shipped skill files:

- `.mivia/skills/architecture-review/SKILL.md`
- `.mivia/skills/bug-audit/SKILL.md`
- `.mivia/skills/concurrency-review/SKILL.md`
- `.mivia/skills/docs-update/SKILL.md`
- `.mivia/skills/feature-delivery/SKILL.md`
- `.mivia/skills/secure-change/SKILL.md`
- `.mivia/skills/verify-change/SKILL.md`
- `.mivia/skills/verify-code-change/SKILL.md`

Add explicit minimal `tools:` lists. The baseline classification to validate
against the actual skill instructions is:

| Skill family | Required static tools |
|---|---|
| `architecture-review`, `bug-audit`, `concurrency-review`, `secure-change` | `read_file`, `list_dir`, `grep`, `glob`, `find_references` |
| `docs-update` | `read_file`, `list_dir`, `grep`, `glob` plus `write_file`, `search_replace` — no `find_references`: the body never resolves code symbols and the shipped `docs` agent does not carry the tool |
| `feature-delivery` | The review set plus `write_file`, `search_replace`, `run_command` |
| `verify-change`, `verify-code-change` | The review set plus `run_command` |

Do not declare `search`, `fetch_url`, `extract`, or the dynamic
`read_skill_resource` capability unless a later design explicitly changes this
contract and adds the corresponding security tests.

`secure-change` and `concurrency-review` stay on the read-only review set, but
their instruction bodies contain imperative run-the-gate steps (secure-change
Method steps 8-9: static gates, secret-scan, bounded fuzz; concurrency-review
Non-determinism section: race detector / stress runs). Phase 1 amends those
steps so command-dependent actions run only when the invoking agent has command
execution and are otherwise reported `PARTIAL`/`NOT_RUN` with the reason.
Tool requirements are never inferred from prose.

## Production seams and validation

Use the existing `parseSkillTools` and `Definition.Tools` path. Add a static
declared-tool catalogue in `internal/tools` named `DeclaredToolNames()` that is
`AllToolNames()` minus the activation-only `read_skill_resource`. Validate every
non-empty declared name during skill discovery against that catalogue, and
validate agent effective tools against the same catalogue in
`internal/agents/resolve.go` (`validateCatalogueTools`), so neither a skill
frontmatter nor an agent TOML can statically require or declare the
activation-only capability. Preserve the existing distinction between omitted
metadata and explicit `tools: []` for user/project compatibility.

Fail closed on invalid static metadata:

- Unknown declared tool names are rejected. In the resilient multi-source
  loader (`LoadMarkdownSources`) the offending skill is skipped with a bounded
  warning so one user-authored file cannot block chat startup; the committed
  project catalogue hard-fails in the fixture and matrix tests.
- Duplicate tool names within one `tools:` list are rejected (change
  `parseSkillTools` to error on a repeated name instead of silently deduping).
- Duplicate frontmatter keys are rejected while preserving the parser's
  existing unknown-key rejection. Do not infer requirements from prose.

## Tests first

Add or extend tests in:

- `internal/skills/frontmatter_test.go`: duplicate-key rejection and malformed
  `tools` values.
- `internal/skills/loader_test.go`: unknown static tool rejection, explicit
  empty list semantics, and the eight committed skills' metadata being present.
- `internal/tools/names_test.go` (or the existing names test location): static
  versus dynamic catalogue behavior.
- `internal/agents/project_agents_fixture_test.go`: load the committed skill
  roots and assert every checked-in skill has a non-nil, valid declaration.

Required negative cases: unknown tool, empty tool name, duplicate tool entries
within one list (rejected, not deduped), duplicate frontmatter key, a committed
skill that omits metadata, a committed skill that statically declares
`read_skill_resource`, and a user-origin skill with an unknown declared tool
(skipped with a warning; chat startup unaffected). Existing custom/user skills
that omit `tools` remain covered by backward-compatibility tests until a
separate migration policy says otherwise.

## Verification

```text
go test ./internal/tools ./internal/skills ./internal/agents -count=1
go test ./internal/tools ./internal/skills ./internal/agents -race
```

Mutation proof: changing a committed skill to `tools: [not_a_tool]`, removing
its `tools:` key, or adding a duplicate frontmatter key must make the focused
contract test fail.
