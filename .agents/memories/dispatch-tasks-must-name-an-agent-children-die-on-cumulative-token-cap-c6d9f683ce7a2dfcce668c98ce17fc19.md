# dispatch_tasks must name an agent; children die on cumulative token cap

scope: project
verdict: mixed
tags: orchestration, dispatch_tasks, token-budget, review
created: 2026-09-07

## Summary
dispatch_tasks without the `agent` field runs tool-less one-shot calls that cannot read files; and sub-agents carry a MaxTotalTokens cap that kills long tasks (~iteration 22).

## What worked
- none

## What did not work
- none

## Why
In this workspace, two review batches were needed because omitting `agent` yields tool-less agents that correctly refuse to fabricate findings. Separately, two tool-enabled general-purpose tasks died with "agentloop: cumulative tokens exceed MaxTotalTokens" — the repo's own agent loop bound (adoptSDKBounds maps MaxContextTokens=1M onto the SDK's cumulative billed-token cap). Partition review tasks for this repo must be scoped lean (bounded git diffs, no whole-file reads) and ideally split so each stays under the cumulative cap.

## References
- none
