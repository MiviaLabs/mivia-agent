---
name: delivery
description: "ADLC delivery loop in skill form: Plan -> Breakdown -> Validate -> Finalize -> Implement (TDD) -> Audit -> Commit. Points at the rule, role files, and runtime templates without duplicating them."
tools:
  - read_file
  - list_dir
  - grep
  - glob
  - find_references
---

# Delivery (loop)

This skill is the loop, not the work. It tells the orchestrator the
shape of a delivery and points at the canonical sources for each step.
It does not write code, does not produce a plan file, and does not
spawn agents - the orchestrator does that, in order.

## Canonical sources (do not duplicate here)

- ADLC rule: `.agents/rules/05-adlc-agentic-development-lifecycle.md`.
  This is the canonical definition. If this skill disagrees with the
  rule, the rule wins.
- Role definitions: `.agents/agents/{planner,plan-reviewer,builder,reviewer}.md`.
- Subagent dispatch contract: `.agents/agents/*.md` (binary's
  workflow engine) or the Markdown set above (human/ADLC). Use one,
  not both, per dispatch.
- Workflow templates: `.mivia/workflows/templates/` (plan, plan-review,
  plan-tests, implement, review, repair, decompose, bugfix-*, e2e-*,
  review-panel-*). These are the runtime templates the binary reads.

## The loop (verbatim from ADLC)

1. **Plan** - `.agents/agents/planner.md` produces an in-context plan.
   No on-disk plan file; ADLC rule 05 forbids it.
2. **Breakdown** - planner subdivides the plan into chunks.
3. **Validate** - `.agents/agents/plan-reviewer.md` runs
   `architecture-review` against the plan and returns `Block / PASS /
   REJECT`.
4. **Finalize** - on `PASS`, the orchestrator captures the plan in
   context and routes to the builder.
5. **Implement (TDD)** - `.agents/agents/builder.md` writes code and
   tests, runs `make verify-fast`, returns chunk logs.
6. **Audit** - `.agents/agents/reviewer.md` re-runs `make verify`,
   dispatches the right per-lens skill via `.agents/skills/review/`,
   returns `Block / PASS / REJECT`.
7. **Commit** - the orchestrator commits with the conventional
   `type(scope): subject` format from `.mivia/policy/commit-message.json`.
   Pre-commit and commit-msg hooks enforce.

## Output (exact shape)

```text
Loop start: <one-line task description>
Step 1: <status> - <planner verdict or "skipped">
Step 2: <status> - <chunk count or "skipped">
Step 3: <status> - <plan-reviewer verdict>
Step 4: <status> - <builder dispatch or "skipped">
Step 5: <status> - <builder output>
Step 6: <status> - <reviewer verdict + lens findings>
Step 7: <status> - <commit landed | blocked | abandoned>
```

## Disallowed operations

- Writing a plan file. ADLC forbids it.
- Running any step's work directly. The orchestrator dispatches; this
  skill narrates.
- Committing. The orchestrator commits at step 7.
- Skipping a step because the previous one "felt good." Each step's
  verdict is the only thing that lets the next step start.

## Escalation

- **Two consecutive reviewer rounds fail.** Escalate to the user with
  the full round log attached. ADLC caps the hostile bug-audit loop at
  three rounds before human escalation.
- **A step finds a deviation between this skill and the ADLC rule.**
  Trust the rule; this skill is a router, not the source of truth.