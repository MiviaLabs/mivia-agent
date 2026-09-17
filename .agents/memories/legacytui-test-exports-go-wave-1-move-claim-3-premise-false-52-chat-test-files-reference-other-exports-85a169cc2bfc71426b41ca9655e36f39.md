---
id: legacytui_test_exports_go_wave_1_move_claim_3_premise_false_52_chat_test_files_reference_other_exports_85a169cc2bfc71426b41ca9655e36f39
title: 'legacytui_test_exports.go wave-1 move: claim-3 premise false, 52 chat test files reference other exports'
content: 'Round-2 validation of the NewTestTerminal relocation spec: claims 1/2/4 held (alias is clichat_aliases_repl.go:79-80 incl. comment; zero non-test consumers repo-wide; fixtures_test.go name free), but claim 3 failed — 52 internal/cli/chat *_test.go files reference other exports of legacytui_test_exports.go, decisively legacytui_test_exports_coverage_test.go which exists to drive 30+ of them. Amendm'
importance: high
x-scope: project
x-verdict: mixed
tags: [go, legacytui, test-exports, spec-validation, clichat-aliases, mivia-agent]
updated: 2026-09-16
---

# legacytui_test_exports.go wave-1 move: claim-3 premise false, 52 chat test files reference other exports

## Summary
Round-2 validation of the NewTestTerminal relocation spec: claims 1/2/4 held (alias is clichat_aliases_repl.go:79-80 incl. comment; zero non-test consumers repo-wide; fixtures_test.go name free), but claim 3 failed — 52 internal/cli/chat *_test.go files reference other exports of legacytui_test_exports.go, decisively legacytui_test_exports_coverage_test.go which exists to drive 30+ of them. Amendm

## What worked
- Verified by direct reads + repo-wide grep rather than trusting spec text
- Full ^package scan of internal/cli/chat confirmed no chat_test external package
- io-import survival check pre-empted a would-be build break in spec execution

## What did not work
- Spec author's claim 3 ("no OTHER export referenced by chat tests") was accepted into the checklist without prior verification

## Why
Any future wave touching legacytui_test_exports.go must assume chat tests reference most of its ~70 exports; "tests don't reference this file" is false. Also: after removing NewTestTerminal the io import stays used (WriteWorktreeList:351, RunWorktreeWithIO:356); deleting alias lines 79-80 alone leaves a gofmt-invalid double blank line; all chat test files are package chat (no external chat_test), so test-only NewTestTerminal resolves for them.

## References
- internal/cli/clichat_aliases_repl.go
- internal/cli/chat/legacytui_test_exports.go
- internal/cli/chat/legacytui_test_exports_coverage_test.go
