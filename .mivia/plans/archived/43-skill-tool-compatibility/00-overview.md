# 43 - Skill/tool compatibility contracts

**Status:** Implemented - all three phases shipped (static metadata, live enforcement, roster matrix)
**Date:** 2026-08-01
**Owner:** Agent/runtime maintainers
**Depends on:** archived plan 06 (agent-skill binding), archived plan 32 (skill resources)
**Blocks:** None
**Scope:** `.mivia/skills`, `.mivia/agents`, `internal/skills`, `internal/tools`, `internal/agents`, `internal/cli`

## 1. Problem and objective

The repository already parses `tools:` in skill frontmatter and already checks
the declared skill tools against an agent's effective tools. The check is
currently vacuous in the shipped control surface: all eight checked-in skills
omit `tools:`. In addition, the security review found that the check is not
consistently applied to the final live registry and every invocation surface.

Close the gap without expanding ordinary agent privileges:

1. Every shipped skill has explicit, minimal static tool requirements.
2. Skill tool names are validated against a static declared-tool catalogue.
3. Compatibility is checked against the final scoped registry at every agent
   invocation boundary, including slash activation.
4. Dynamic `read_skill_resource` access remains a separate activation capability,
   not a blanket skill requirement.
5. The committed agent roster is compatible with its skill allowlists, and the
   compatibility matrix remains enforced by tests.

## 2. Confirmed findings and decisions

| Finding | Disposition |
|---|---|
| All eight shipped skills omit `tools:` | Confirmed; Phase 1 closes it with explicit metadata. |
| Parser/loader and agent subset checks already exist | Confirmed; preserve the API and make the existing contract non-vacuous. |
| Unknown declared tool names can survive discovery | Confirmed; Phase 1 validates them at discovery. |
| `reviewer`, `security`, and `docs` allow `verify-code-change` without `run_command` | Confirmed; Phase 3 removes the incompatible binding rather than granting shell access. |
| `secure-change` and `concurrency-review` instructions contain imperative command-running steps (static gates, fuzz, race detector) | Confirmed; Phase 1 amends those steps to be conditional on available command execution; classification stays read-only, no shell granted. |
| Runtime checks use the agent TOML list rather than the final live registry | Confirmed; Phase 2 checks the post-disable/deny scoped registry. |
| Direct slash skill activation does not use the same explicit tool check | Confirmed; Phase 2 routes it through the same policy seam. |
| User/project skill precedence can diverge between catalogue and runtime | Confirmed; Phase 2 makes the selected origin and precedence explicit and tests it. |
| `read_skill_resource` is injected per activation | Confirmed; keep it out of static `tools:` requirements and test its bounded capability separately. |

## 3. Non-goals

- Do not add broad tools to read-only agents merely to satisfy an allowlist.
- Do not infer tool requirements from prose in a skill body.
- Do not add delegation flags, roles, or a new general permission language.
- Do not make `read_skill_resource` a permanent agent capability.
- Do not change root orchestration privileges; root remains a deliberate,
  full-catalogue exception.
- Do not rewrite the frontmatter parser or replace the existing agent model.

## 4. Dependency graph

```text
Phase 1: static metadata + discovery validation
    |
    v
Phase 2: live-registry and invocation-surface enforcement
    |
    v
Phase 3: roster reconciliation + docs + full verification
```

Each phase is independently testable. Phase 2 must consume Phase 1's static
catalogue semantics; Phase 3 must consume Phase 2's final policy seam.

## 5. ADLC implementation waves

The eventual implementation must follow the repository ADLC. Within each phase:

1. Write the failing contract tests first.
2. Implement the smallest production/configuration change.
3. Run an adversarial bug/security review of the phase diff.
4. Run focused gates, then the repository gates.
5. Reconcile any finding before moving to the next phase.

The implementation task list must obey the one-file task rule: each production
task changes one file and has a preceding test task; a reviewer follows every
two or three production tasks.

## 6. Plan scorecard

| Criterion | Result | Reason |
|---|---|---|
| Compiles | PASS | Uses existing frontmatter, registry, and agent policy seams. |
| No dependency cycles | PASS | Static catalogue remains in `internal/tools`; policy consumes it. |
| No breaking public API | PASS | Changes are internal, control-surface metadata, or tests. |
| Testable in isolation | PASS | Each phase has focused loader, policy, CLI, and roster cases. |
| Backward-compatible config | PASS with explicit migration | Omitted `tools` remains accepted for external/user skills; checked-in skills become explicit; duplicate entries within one `tools:` list and duplicate frontmatter keys fail closed; unknown declared tool names skip with a warning for user/project skills in the resilient loader (committed catalogue hard-fails); `read_skill_resource` is excluded from the static declared-tool catalogue for both skills and agent TOMLs. |
| Security boundary is honest | PASS after Phase 2 | Dynamic resource access and root/slash behavior are tested separately. |
| Every new behavior has a negative test | PASS | Unknown tools, missing tools, shadowing, disabled tools, and slash bypasses are covered. |

## 7. Rollback criteria

Stop and revert the affected phase if any valid shipped agent becomes
unselectable, a skill gains command/network access without a demonstrated need,
user/project precedence becomes ambiguous, a slash path bypasses the chosen
policy, or a repository gate fails with no documented pre-existing cause.

Rollback is safe because the change is additive metadata plus internal checks;
revert the phase commit and retain this plan for a corrected attempt.

## 8. Acceptance

The plan is complete only when all three phase files are implemented, the
compatibility matrix test passes for the committed roster, the direct and agent
skill surfaces have the same declared-tool decision, and `make verify` passes.
The completion report must list exact commands and results; no check may be
claimed from inspection alone.
