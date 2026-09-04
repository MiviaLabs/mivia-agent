---
id: prompt_lines_require_observed_failures
title: Compiled prompt lines require an observed failure mode
content: Add a line to a compiled prompt constant only for an observed failure mode, and check the change in behavior; do not add or reword lines on speculation.
importance: high
tags: [[prompts, agents, reliability, instruction-density]]
---

The compiled prompt constants (`BuiltInOrchestratorPrompt`,
`BuiltInGeneralPurposePrompt` in `internal/agents/builtin.go`) are a
bounded instruction budget, not free space. Two research results set
the bar (recorded 2026-08-30):

- Instruction-following compliance degrades as instruction density
  grows, with bias toward earlier lines (IFScale, arXiv 2507.11538).
  Each added line taxes compliance with every other line.
- Prompt wording is paraphrase-brittle in both directions (arXiv
  2401.00595, 2510.05152). A rewording of a line that works is a
  regression risk, not a neutral edit.

Before you add or reword a compiled prompt line:

1. Name the observed failure mode the line corrects. No observed
   failure, no line.
2. Check the change: at minimum run the pin, genericity, and budget
   tests; for behavior claims, compare sessions or e2e runs before
   and after.
3. Pin the fragment in `TestBuiltInPromptDisciplineLines` only after
   it earns its place, and record the trigger in the test comment.

Prefer the harness for reliability work: independent review agents,
workflow verifier steps, and deterministic gates outperform prompt
self-instruction for verification (Verification Horizon, arXiv
2606.26300).
