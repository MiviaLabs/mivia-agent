---
id: legacytui_exports_clichat_aliases_reachability_inventory_wave_2_slice_post_wave_1_08b2652fa8f86d33f5386075960fb3bc
title: 'legacytui exports + clichat aliases reachability inventory (wave-2 slice, post-wave-1)'
content: 'Full per-symbol reachability inventory of internal/cli/chat/legacytui_test_exports.go (73 exports) + 140 cli aliases in clichat_aliases_{repl,render,session}.go. Result: 4 chat exports PRODUCTION (ContextDispatcherFor, ApplyPrivacyPolicy, LoadChatSkills, OpenContextStore — all via wiring seams), 53 TEST-ONLY, 16 DEAD; aliases: 5 PRODUCTION (tui/run/run.go:31,33,36,329,348: SetSubagentProgress, Cle'
importance: high
x-scope: project
x-verdict: good
tags: [go, dead-code, legacytui-test-exports, clichat-aliases, reachability, mivia-agent, plan-2-slice-2]
updated: 2026-09-16
---

# legacytui exports + clichat aliases reachability inventory (wave-2 slice, post-wave-1)

## Summary
Full per-symbol reachability inventory of internal/cli/chat/legacytui_test_exports.go (73 exports) + 140 cli aliases in clichat_aliases_{repl,render,session}.go. Result: 4 chat exports PRODUCTION (ContextDispatcherFor, ApplyPrivacyPolicy, LoadChatSkills, OpenContextStore — all via wiring seams), 53 TEST-ONLY, 16 DEAD; aliases: 5 PRODUCTION (tui/run/run.go:31,33,36,329,348: SetSubagentProgress, Cle

## What worked
- none

## What did not work
- none

## Why
Method that worked: qualifier-capturing rg ((\w+)\.)?\bName\b with -o so chat./clichat./cliagents.-qualified refs are distinguishable from bare package-internal uses; scope alias-consumer greps to internal/cli root via --max-depth 1 (subdirs are other packages); substring false positives (newREPLRuntime, builtInSlashCommands, term.IsTerminal) only caught by full-line verification. All chat tests are package-internal so they reference exports UNQUALIFIED — naive chat\. greps miss them (wave-1 lesson, confirmed).

## References
- internal/cli/chat/legacytui_test_exports.go
- internal/cli/clichat_aliases_repl.go
- internal/cli/clichat_aliases_session.go
- internal/cli/clichat_aliases_render.go
- plans/dead-code-deletion-plan.md
