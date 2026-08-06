# 58 - Workflow delivery hardening

**Status**: Draft. Challenge complete. Do not implement until Step 0 is locked.

## Goal

Make `feature-delivery` run a small change in an isolated worktree, verify it with host-owned checks, and publish one draft pull request.

## Confirmed defects

- The workflow referenced an absent `planner` agent.
- A workflow agent with `run_command` is rejected by design.
- The configured SQLite path did not expand `~`.
- Workflow templates are absent.
- `workflows validate` does not load templates or other runtime references.
- `go-default` only checks for a worktree and `go.mod`.
- Workflow steps cannot activate a skill.
- A failed evidence gate cannot send its failure evidence to a repair step.

## Locked boundaries

- Workflow agents do not use `run_command`, commit, push, or read secrets.
- Host-owned evidence gates run fixed commands. TOML never supplies commands.
- General `feature-delivery` remains unchanged for interactive agents.
- Workflow steps must bind a skill explicitly before a workflow-safe delivery skill can govern them.

## Design

1. Add `skill` to workflow agent steps. Validate that the named agent allows it. Pass the selected skill to the runtime request.
2. Add `workflow-feature-delivery`. Keep the delivery method: scope, TDD, negative tests, malformed input cases, security checks, fuzz decision, hook policy, and honest report. Replace command execution with evidence-gate evidence.
3. Keep `workflow-engineer` restricted. Allow the workflow-safe skill only.
4. Add missing plan, test-plan, implement, and review templates. The templates carry the workflow delivery contract.
5. Replace repeated `go-default` with host-owned profiles: `go-test`, `go-verify`, and `go-final`. They run fixed tests, build, vet, contracts, secrets, and race checks as appropriate.
6. Add failure-evidence transitions from each gate to the repair step.
7. Make validation load all templates, schemas, agents, skills, verifier profiles, and delivery settings.
8. Add a committed-workflow contract test. It rejects absent references, `run_command` workflow agents, incompatible skill tools, duplicate or missing gates, and a delivery base other than `master`.

## Waves

1. Schema and runtime skill binding with unit tests.
2. Workflow-safe skill and restricted agent configuration with compatibility tests.
3. Templates and validated workflow definition.
4. Fixed verifier profiles and evidence-repair routing.
5. Contract tests and an isolated Git integration test.
6. Run `make verify`, `make race`, and one real small workflow run with `--allow-publish`.

## Challenge disposition

Architecture and correctness reviews both returned BLOCK. The plan adopts their findings. A skill alone is not sufficient because the current runtime never activates skills. Host-owned verifier profiles are required because the current evidence gate does not execute delivery checks.

## Rollback criterion

Stop if fixed host-owned verifier profiles cannot run safely in the workflow worktree, or if a workflow can widen command authority through its TOML.
