# Memories auto-stage via pre-commit hook

scope: project
verdict: good
tags: hooks, pre-commit, memories, git
created: 2026-09-04

## Summary
Pre-commit now auto-stages .agents/memories changes (add/edit/delete) into every commit; commit aa06ddf3. Memories-only changes still need --allow-empty because git skips hooks with an empty index.

## What worked
- .githooks → scripts/git-hooks/pre-commit now runs `git add -A -- .agents/memories` before the gates, so new/edited/deleted memories ride along with any commit
- Escape hatch for memories-only changes: git refuses to run pre-commit with nothing staged; use `git commit --allow-empty` and the hook stages the memories into that commit
- aa06ddf3 replaced the legacy .mivia/memory.db auto-stage (staged-modification-only, could never carry a new memory)

## What did not work
- none

## Why
Every memory previously needed a separate manual commit (the user had to ask for it explicitly twice). The db auto-stage block this replaced predated the markdown memory store and could never stage a new memory.

## References
- scripts/git-hooks/pre-commit
- scripts/test_git_hooks.py
