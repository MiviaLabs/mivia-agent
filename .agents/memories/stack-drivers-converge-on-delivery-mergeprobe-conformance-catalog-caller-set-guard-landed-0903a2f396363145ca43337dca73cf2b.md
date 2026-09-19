---
id: stack_drivers_converge_on_delivery_mergeprobe_conformance_catalog_caller_set_guard_landed_0903a2f396363145ca43337dca73cf2b
title: 'Stack drivers converge on delivery.MergeProbe; conformance catalog + caller-set guard landed'
content: 'Stack-driver sibling drift fixed by the sibling-implementations-drift recipe: shared delivery.MergeProbe/ProbeRunMerged (pushed-evidence gate -> local ancestor -> remote PR, fail-closed), a 5-scenario conformance catalog both drivers must wire, and a go/packages caller-set guard proving only delivery + internal/cli/chat call MergePullRequest. Docs now state the engine is observe-only for merging.'
importance: medium
x-scope: project
x-verdict: good
tags: [stack-driver, merge-probe, conformance, sibling-drift, go-packages-guard]
updated: 2026-09-18
---

# Stack drivers converge on delivery.MergeProbe; conformance catalog + caller-set guard landed

## Summary
Stack-driver sibling drift fixed by the sibling-implementations-drift recipe: shared delivery.MergeProbe/ProbeRunMerged (pushed-evidence gate -> local ancestor -> remote PR, fail-closed), a 5-scenario conformance catalog both drivers must wire, and a go/packages caller-set guard proving only delivery + internal/cli/chat call MergePullRequest. Docs now state the engine is observe-only for merging.

## What worked
- none

## What did not work
- none

## Why
Docs (workflows-guide.md:854, workflow-stack-settle.md) decided goal B: engine auto-delivers but never merges. The actual live bug was prMerged's FindByHead-then-IsMerged ordering stalling squash-merged pruned-branch stacks. Pattern to reuse for any two-implementation contract: catalog with owner labels pinned by a golden count, unhandled owned row fails the driver runner, symbol-identity (not grep) caller-set guard.

## References
- internal/workflows/delivery/merge_probe.go
- internal/workflows/delivery/stack_conformance.go
- internal/workflows/delivery/merge_callers_contract_test.go
- docs/architecture/workflow-stack-settle.md
