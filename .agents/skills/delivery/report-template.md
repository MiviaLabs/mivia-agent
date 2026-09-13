# Delivery loop report template

```text
Delivery: <one-line task description>
Started: <ISO timestamp>
Finished: <ISO timestamp | not yet>

Step 1 (Plan): <verdict>
  output: <planner block excerpt or pointer>

Step 2 (Breakdown): <chunk count>
  slices: <slice count, in commit order>

Step 3 (Validate): <approved | changes_requested (+ reject: true|false)>
  findings: <count>
  routed back: <yes | no>

Step 4 (Finalize): <status>

Steps 5-7 repeat once per slice, in slice order. No slice is committed
before its Step 6 loop reports a zero-finding round.

Step 5 (Implement, slice <id>): <chunk-by-chunk summary>

Step 6 (Review, slice <id>): <zero findings | findings -> fixed -> re-review>
  rounds: <N> (unbounded until a zero-finding round)
  re-runs: <list of commands and PASS/FAIL>
  lens: <name from .agents/skills/review/>
  findings: <count in final round> / <total across all rounds>

Step 7 (Commit, slice <id>): <commit SHA | blocked | abandoned>
  reason on blocked/abandoned: <explanation>

Round count: <total review rounds across all slices; per slice: <id>=<N>>
```
