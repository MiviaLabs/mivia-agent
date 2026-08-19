# Operating Doctrine

Brand: **MiviaLabs**. Product CLI binary: **`mivia`**. Entrypoint: `cmd/mivia/`.

## Canonical Source Order

1. System / tool instructions
2. `.mivia/` - project control surface (rules, skills, policy, quality)
3. `AGENTS.md` - repo-level agent behavior
4. Task prompt

If an adapter conflicts with `AGENTS.md` or `.mivia/`, follow `AGENTS.md` / `.mivia/` and fix the adapter. Do not invent a second doctrine in adapter files.

## Scope Control

- Before implementation, read the relevant plan/task docs under `docs/` and the owning entry in `docs/OWNERS.yaml`.
- Stay inside the named task, branch, package, or file boundary unless the user expands scope.
- Do not implement product code outside an agreed task or explicit user request.
- Preserve existing docs and user changes unless the task requires editing them.
- Prefer the smallest change that satisfies the acceptance criteria.

## Documentation-First Work

- Code changes that alter behavior, flags, config, security posture, or public API must update the **canonical** doc for that topic (see `.agents/rules/40-docs-ownership.md`).
- If implementation reveals a task split, update the plan/task file before writing the second production unit.
- Completion reports name changed files, verification run, and residual risk. Do not claim “done” without verification status.

## Idempotency

- Writers, generators, init/update commands, and importers must be rerunnable with **no diff** for the same inputs.
- Every writer needs an idempotency test.
- Generated order is deterministic: sort map keys, filenames, hook names, and registry entries before write.

## Verification Contract

Every implementation response follows the report shape in `.agents/rules/01-output-budget.md`. Unverified claims are forbidden. State assumptions and evidence gaps explicitly.

## Product Naming

- Ship and refer to the CLI as **`mivia`**.
- Module/repo path may be `github.com/MiviaLabs/mivia-agent`; the **shipping CLI name is only** `mivia`.
- User-facing docs, help text, install instructions, and Makefile targets use `mivia`.

## Host vs model-facing surface

- **Host** (this codebase) is Go. **Model-facing tools and compiled default prompts** are project- and language-generic for any user workspace.
- Full rule: `.agents/rules/60-tools-project-language-generic.md`.

## Fail Closed

- Protected actions (commit beyond agreed scope, push, PR open, deploy, release, live external calls) require explicit user intent and policy/hook allowance.
- Malformed hook payloads that request protected actions must be rejected.
- Prefer deny-by-default for path writes outside the repo and for network unless a surface is designed for it.

## Long-Running Work

- Mivia supports **hours-long orchestration**. Default timeouts are advisory, not hard ceilings.
- The orchestrator agent receives **heartbeat/progress events** from running subagents and can react (cancel stalled, extend deadlines, redirect).
- Zero timeout means **no timeout** - the task runs until completion, cancellation, or budget exhaustion.
- See `.agents/rules/70-long-running-heartbeat.md`.
