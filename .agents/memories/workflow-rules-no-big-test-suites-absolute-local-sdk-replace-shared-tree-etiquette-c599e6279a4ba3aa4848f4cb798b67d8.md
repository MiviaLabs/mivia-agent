---
id: workflow_rules_no_big_test_suites_absolute_local_sdk_replace_shared_tree_etiquette_c599e6279a4ba3aa4848f4cb798b67d8
title: 'Workflow rules: no big test suites; absolute local SDK replace; shared tree etiquette'
content: 'In mivia-agent, never run package-wide or ./... go test; verify with go build, go vet, and targeted -run tests; keep go.mod''s mivia-ai-sdk replace as an absolute local path; the tree is shared, so stage/commit only your own files by explicit path.'
importance: high
tags: [testing, verification, go, git-workflow, user-preference]
updated: 2026-09-07
---

# Workflow rules: no big test suites; absolute local SDK replace; shared tree etiquette

## Summary
Mivia-agent repo: user forbids running large test suites (a package-wide go test was canceled); verify with go build, go vet, and targeted -run tests only. go.mod must keep the absolute local replace path for mivia-ai-sdk, and the tree is shared with other agents, so stage/commit only your own files.

## What worked
["Build (go build ./...), go vet ./internal/agent/, and narrow `go test -run=<specific tests>` are acceptable verification", "Never run package-wide or ./... go test suites in this workspace", "go.mod SDK replace must keep the ABSOLUTE path /home/mac/projects/mivialabs/mivia-ai-sdk (local checkout, not a remote tag)", "Tree is shared with other agents: stage only your own files by explicit path; never stash/reset/clean"]

## What did not work
- none

## Why
Explicit user corrections during the SDK adoption review/fix session: canceled `go test ./internal/agent/` and ordered never to run big test suites; required the go.mod replace to stay absolute-local (rejected a relative ../mivia-ai-sdk normalization); warned another agent works in the same tree so no stash/reset/delete and only explicit-path staging.

## References
- none
