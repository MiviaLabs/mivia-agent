---
id: sdk_adapter_simplification_validated_dead_surface_and_guardrails_edcad37eb3ef8abcc349c576beca328c
title: 'SDK adapter simplification: validated dead surface and guardrails'
content: 'Task ''simplify remaining adapters'' validated: internal/agent bridge (admission/execution/budgets/events) has NO flattenable wrappers — every one carries gap-ledger-documented policy. Real dead surface is internal/sdkadapter: mcp.go, workspace.go, skill.go, hooks.go, ledger.go (whole files) plus WrapCompleter/Err* sentinels in usage.go and ChatStream/usage-converters in provider.go all have zero pr'
importance: high
x-scope: project
x-verdict: good
tags: [sdkadapter, sdk-adoption, dead-code, gap-ledger, refactor-plan]
updated: 2026-09-16
---

# SDK adapter simplification: validated dead surface and guardrails

## Summary
Task 'simplify remaining adapters' validated: internal/agent bridge (admission/execution/budgets/events) has NO flattenable wrappers — every one carries gap-ledger-documented policy. Real dead surface is internal/sdkadapter: mcp.go, workspace.go, skill.go, hooks.go, ledger.go (whole files) plus WrapCompleter/Err* sentinels in usage.go and ChatStream/usage-converters in provider.go all have zero pr

## What worked
["Gap ledger + field-mapping doc reconcile the SDK boundary; import-layers.json row for internal/sdkadapter pins the seam.", "Parallel read-only researcher dispatch mapped consumers repo-wide before any edit; per-symbol qualified greps (sdkadapter.X) gave exact consumer lists.", "Deletion tiers validated by combining external grep, in-package grep, and build-tag awareness (ledger_sqlite stub)."]

## What did not work
- none

## Why
The deleted binding plan (docs/plans/ui-replacement-and-sdk-integration.md, removed in a6586ff0) planned B.2 #9-#14 SDK drops that never landed; sdkadapter bridge files built for them are zero-consumer. doc.go's 'only seam' claim is stale since internal/agent adopted SDK agentloop directly (40 files). Future simplification work must check the gap ledger first — its keep/adopted verdicts are recorded decisions that suppress findings.

## References
- plans/simplify-remaining-adapters-plan.md
- docs/development/sdk-gap-ledger.md
- internal/sdkadapter/doc.go
