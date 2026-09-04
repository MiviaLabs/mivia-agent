---
name: delivery
description: ADLC delivery loop in skill form: Plan -> Breakdown -> Validate -> Finalize -> Implement (TDD) -> Audit -> Commit. Points at the rule, role files, and runtime templates without duplicating them.
tools:
  - read_file
  - list_dir
  - grep
  - glob
  - find_references
---

This skill is defined in `.agents/skills/delivery/SKILL.md`.

Read that file now and follow it exactly. It is the only definition. This file
is an alias so Claude Code can find the skill. Bundled resources live beside
the canonical file.
