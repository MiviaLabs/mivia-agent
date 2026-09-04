# Delivery loop report template

```text
Delivery: <one-line task description>
Started: <ISO timestamp>
Finished: <ISO timestamp | not yet>

Step 1 (Plan): <verdict>
  output: <planner block excerpt or pointer>

Step 2 (Breakdown): <chunk count>

Step 3 (Validate): <approved | changes_requested (+ reject: true|false)>
  findings: <count>
  routed back: <yes | no>

Step 4 (Finalize): <status>

Step 5 (Implement): <chunk-by-chunk summary>

Step 6 (Audit): <approved | changes_requested>
  re-runs: <list of commands and PASS/FAIL>
  lens: <name from .agents/skills/review/>
  findings: <count>

Step 7 (Commit): <commit SHA | blocked | abandoned>
  reason on blocked/abandoned: <explanation>

Round count: <number of audit cycles before terminal verdict>
```