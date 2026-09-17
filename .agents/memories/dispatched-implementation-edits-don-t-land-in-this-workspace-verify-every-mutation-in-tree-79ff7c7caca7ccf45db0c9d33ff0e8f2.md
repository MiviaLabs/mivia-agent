---
id: dispatched_implementation_edits_don_t_land_in_this_workspace_verify_every_mutation_in_tree_79ff7c7caca7ccf45db0c9d33ff0e8f2
title: 'Dispatched implementation edits don''t land in this workspace; verify every mutation in-tree'
content: 'In this workspace, dispatched implementation agents (go-engineer etc.) edit an isolated environment: their reported diffs do NOT land in the shared tree (verified via git status during the dead-code delivery). Read-only dispatched roles (reviewer/auditor/verifier) work normally. Also: search_replace refuses pure deletions (''new_string already present'' quirk); read->write round-trips corrupt shell-'
importance: high
x-scope: project
x-verdict: mixed
tags: [dispatch_tasks, go-engineer, worktree, search_replace, verify-landing, delivery-loop, diff-coverage]
updated: 2026-09-16
---

# Dispatched implementation edits don't land in this workspace; verify every mutation in-tree

## Summary
In this workspace, dispatched implementation agents (go-engineer etc.) edit an isolated environment: their reported diffs do NOT land in the shared tree (verified via git status during the dead-code delivery). Read-only dispatched roles (reviewer/auditor/verifier) work normally. Also: search_replace refuses pure deletions ('new_string already present' quirk); read->write round-trips corrupt shell-

## What worked
["Verified landing with git status + read_file before proceeding; switched to direct file edits for mutations (evidence: t1 sandbox diff never appeared in tree)", "Byte-precise deletions via run_command python3 -c with count==1 assertions; git commit -F - via stdin avoids all shell quoting", "Hostile challenge panel (reviewer+architecture-review, auditor) refuted the plan's S2 with file:line evidence before any code moved", "git commit -- <tracked paths> + separate git add for new files; amend -F - to fix message disclosure"]

## What did not work
["Trusted a dispatched implementer's self-reported diff without verifying the shared tree - a1's audit then ran against an empty diff", "search_replace on pure deletions: new_string already exists in the file, tool refuses with 'edit already applied'", "Transcribing file content through read->write corrupted a shell-quote comment (run'\"'\"'s -> run'\"\"\"'s); caught by od -c in audit a1b", "Skipped the Phase-0 baseline verify-fast; the branch's pre-existing diff-coverage debt was discovered only at the end"]

## Why
The ADLC rule routes Step 4 implementation to dispatched go-engineer agents, but their edits stayed in a sandbox: t1 reported a applied diff and green gates while the shared tree was at HEAD (audit a1 proved it). Delivery on this machine must apply edits with direct file tools and reserve dispatched agents for read-only roles, or verify landing after every dispatched mutation. Tool quirks (pure-deletion refusals, quote corruption on file rewrites, byte-precise python3 -c edits) cost three audit findings in one delivery.

## References
- none
