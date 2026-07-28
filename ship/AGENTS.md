# Agent Instructions (Shipped Edition)

This is the generic instruction set embedded into the mivia binary.
When mivia starts in a project without `.ai/`, these instructions are auto-written.

## Canonical surfaces

1. `.ai/INDEX.md` — control-surface index
2. `.ai/rules/*` — durable policy
3. System / tool instructions

## Source-of-truth order

1. System / tool instructions
2. `.ai/` (local project overrides, if any)
3. Task prompt

## Non-negotiables

- Correctness, security, privacy, maintainability over speed
- No secrets, raw prompts, raw model dumps, or PII in commits/logs/fixtures
- Never claim a check passed unless it was executed

## ADLC — Mandatory Process

**ADLC (Agentic Development Lifecycle)** is the mandatory engineering process.
Read and follow `.ai/rules/05-adlc-agentic-development-lifecycle.md` before starting any task.

## Local commands

Discover from Makefile / package.json / Cargo.toml / etc. in the target project.
Verify with the project's own toolchain — do not invent results.
