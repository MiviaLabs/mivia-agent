---
id: mivia_agent_check_prose_py_is_repo_wide_red_and_ignores_args_fix_commits_need_regression_class_sweep_trailers_fcdc7ca8401ba00232bb80b250acad2e
title: 'mivia-agent: check_prose.py is repo-wide red and ignores args; fix commits need Regression/Class/Sweep trailers'
content: 'Two verification traps in mivia-agent: (1) scripts/check_prose.py ignores file arguments and always scans docs/ repo-wide; it is permanently red with ~113 pre-existing >25-word-sentence violations outside docs/development/ - judge it by delta (stash the diff, compare) not by exit code. (2) commit-msg hooks require Regression/Class/Sweep trailers for fix commits; trailers must be inside a single -m'
importance: high
x-scope: project
x-verdict: good
tags: [verification, gates, commit-hooks, check_prose, gotcha]
updated: 2026-09-15
---

# mivia-agent: check_prose.py is repo-wide red and ignores args; fix commits need Regression/Class/Sweep trailers

## Summary
Two verification traps in mivia-agent: (1) scripts/check_prose.py ignores file arguments and always scans docs/ repo-wide; it is permanently red with ~113 pre-existing >25-word-sentence violations outside docs/development/ - judge it by delta (stash the diff, compare) not by exit code. (2) commit-msg hooks require Regression/Class/Sweep trailers for fix commits; trailers must be inside a single -m

## What worked
["Stash-baseline comparison proves a gate failure predates the diff", "fix() commits: include Regression/Class/Sweep inside the -m body; Class from .agents/quality/defect-taxonomy.md (budget/cap defects = DC-6)", "go-engineer runs commands; reviewer/docs roles are read-only - have them, not the parent, produce evidence"]

## What did not work
["Trusting subagent gate reports without reproducing (earlier slices reported check_prose green)", "Passing trailers as separate argv entries instead of inside one -m block"]

## Why
Both traps cost a retry each this session. The prose gate's red exit will keep failing CI-style checks for unrelated diffs until someone fixes the ~113 legacy sentences or scopes the script; treating exit code as the verdict would block every docs slice. The trailer rule is enforced by the commit-msg hook and is not obvious from git log alone.

## References
- scripts/check_prose.py
- .mivia/policy/commit-message.json
- .agents/quality/defect-taxonomy.md
