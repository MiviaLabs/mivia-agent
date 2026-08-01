# 43.3 - Reconcile the agent roster and close the contract

**Status:** Planned
**Depends on:** `02-live-compatibility-enforcement.md`
**Goal:** Ship a coherent agent team whose skill allowlists and tools are mechanically compatible.

## Scope

Update only the affected roster and canonical control-surface references:

- `.mivia/agents/docs.toml`
- `.mivia/agents/reviewer.toml`
- `.mivia/agents/security.toml`
- `.mivia/agents/verifier.toml` if the final matrix requires clarification
- `internal/agents/project_agents_fixture_test.go`
- `.mivia/INDEX.md`
- this plan directory's status headers after implementation

Remove `verify-code-change` from agents that intentionally lack
`run_command`; keep command-backed verification on `verifier` and
`go-engineer`. Do not grant shell execution to read-only review or docs agents.
If a skill's minimum requirements differ from the Phase 1 classification,
update the metadata and matrix from demonstrated behavior, not convenience.

## Contract test

Add a live roster matrix test that loads the same skill registry and committed
agent definitions used by runtime, then asserts for every explicit
agent/skill pairing:

```text
skill allowlist permits the skill
AND
final effective tools cover every static skill requirement
```

Also assert that every committed skill is either in the matrix or deliberately
owned by the unrestricted root. The test must report the exact agent, skill,
and missing tool on failure.

## Documentation and closeout

Update `.mivia/INDEX.md` to point to this directory while active and archive
the plan only after all phases ship. Do not create a second policy document;
the metadata contract belongs to the skill frontmatter and the enforcement
code/tests own the mechanical rule.

Run the complete repository gates:

```text
make verify
make test
make race
make secret-scan
make docs-check
```

Run CLI smoke checks only if their current command syntax is confirmed from
`mivia --help`; record exact output and do not claim a smoke check from source
inspection.

## Acceptance and rollback

Acceptance requires a clean committed roster, focused tests, all listed gates,
and a completion report with changed files, evidence, and residual risk.
Rollback if any specialist receives a newly privileged tool, a valid skill is
unavailable to its intended agent, or the final/live registry and slash paths
disagree.
