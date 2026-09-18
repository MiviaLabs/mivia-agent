---
id: ledger_package_split_panel_agenttools_siblings_validator_init_hook_seam_555876bf837d0e500d454568b8568ebc
title: 'Ledger package split: panel + agenttools siblings, validator init-hook seam'
content: 'Split internal/workflows/ledger into ledger (persistence+task ledger, no coordinator/subagents imports, enforced by two import-layers deny rows), internal/workflows/panel (coordinator + fingerprint validation registered into the ledger via SetPanelContentValidator init hook; repository fails closed when unset), and internal/workflows/agenttools (workflow_* tools, Service, Engine seam). View types'
importance: medium
x-scope: project
x-verdict: neutral
tags: [ledger, panel, agenttools, package-split, import-layers, refactor, mivia-agent]
updated: 2026-09-18
---

# Ledger package split: panel + agenttools siblings, validator init-hook seam

## Summary
Split internal/workflows/ledger into ledger (persistence+task ledger, no coordinator/subagents imports, enforced by two import-layers deny rows), internal/workflows/panel (coordinator + fingerprint validation registered into the ledger via SetPanelContentValidator init hook; repository fails closed when unset), and internal/workflows/agenttools (workflow_* tools, Service, Engine seam). View types

## What worked
- none

## What did not work
- none

## Why
Records the seam decisions future work must respect: (1) panel content validation lives in panel and reaches the ledger only through the registered hook - a binary linking ledger without panel refuses panel attempts by design; (2) ledger's in-package tests use a mirrored stub validator (panel_content_mirror_test.go) that must stay identical to panel.ValidateTaskContent because in-package tests cannot import panel (cycle); (3) the durable panel types stay in ledger, so PanelTaskSpec.Clone/ValidateLegacy/WorkFingerprintValue and ClaimHolderFromContext are exported. Also: dispatched reviewer/auditor children twice died with 'trim: invalid context DTO ... malformed arguments' agentloop errors - keep their briefs tiny and verify their claims in-tree.

## References
- internal/workflows/ledger/panel_content.go
- internal/workflows/panel/content.go
- internal/workflows/agenttools/doc.go
- .mivia/policy/import-layers.json
