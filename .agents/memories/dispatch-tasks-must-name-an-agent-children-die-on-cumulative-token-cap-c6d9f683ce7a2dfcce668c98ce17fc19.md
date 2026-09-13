---
id: dispatch_tasks_must_name_an_agent_children_die_on_cumulative_token_cap_c6d9f683ce7a2dfcce668c98ce17fc19
title: 'dispatch_tasks must name an agent; children die on cumulative token cap'
content: 'dispatch_tasks without the agent field runs tool-less one-shot calls; sub-agents also carry a MaxTotalTokens cap that kills long tasks around iteration 22.'
importance: medium
tags: [orchestration, dispatch_tasks, token-budget, review]
related: [branch_tip_moves_during_agent_runs_pin_a_hash_for_diff_scoped_audits_c4b05cbeb324667c319c968d3c1d19f0, no_per_agent_spend_ceilings, preventing_agent_over_exploration, task_identity_two_forms]
updated: 2026-09-07
---

# dispatch_tasks must name an agent; children die on cumulative token cap

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
