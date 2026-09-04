---
name: memory-housekeeping
description: "Audit and maintain the project memory store: verify facts, delete stale or duplicate entries, update outdated ones, and create missing ones. Use for memory cleanup and accuracy passes."
tools:
  - memory_search
  - memory_save
  - memory_delete
  - read_file
  - list_dir
  - grep
  - glob
  - search
  - run_command
---

This skill is defined in `.agents/skills/memory-housekeeping/SKILL.md`.

Read that file now and follow it exactly. It is the only definition. This file
is an alias so Claude Code can find the skill. Bundled resources live beside
the canonical file.
