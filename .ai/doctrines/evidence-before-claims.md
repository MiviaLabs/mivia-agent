# Evidence Before Claims

Coding agents can produce coherent explanations that are still wrong.
Material engineering claims need traceable evidence.

## Principle

An agent may infer, estimate, or propose. It must not present those as verified fact.

## Two evidence questions

### What should happen?

1. Explicit user constraints and acceptance criteria
2. Approved product or engineering decisions
3. Authoritative specifications and contracts
4. Accepted tests and project documentation
5. Stated assumptions when intent is incomplete

### What currently happens?

1. Reproducible runtime behavior
2. Focused tests, logs, observed outputs
3. Code and configuration
4. Version history and deployment state
5. Current official external docs when external behavior matters

Tests, documentation, and code can disagree. Reconcile contradictions before relying on them.

## Required behavior

- Inspect the actual repository before making implementation claims.
- Run the relevant check before claiming it passed.
- Distinguish facts, inferences, assumptions, and unresolved uncertainty.
- Limit claims to the scope actually inspected or executed.

## Failure pattern

A coherent explanation is not evidence. A plausible API is not an existing API.
A proposed verification command is not a completed verification result.
