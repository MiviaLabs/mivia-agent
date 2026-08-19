# mivia-report/v1 - workflow-runs-analysis grammar

Use the canonical mivia-report/v1 envelope (`.agents/templates/agent-report-v1.md`).
The Findings grammar below is extended for validated process-quality analysis.
Keep each field to one line or short bullet; do not add narrative sections.

```md
ReportFormat: mivia-report/v1
Skill: workflow-runs-analysis
Result: PASS|PARTIAL|NOT_RUN
Scope: <window and anchor>; <status composition>; <runs inspected after cap>; <excluded runs>
Summary: <one sentence>
Evidence:
- <tool call>: PASS|FAIL|NOT_RUN - <short note>
Findings:
- [WA-n] <severity> <one-line observed claim> | Evidence: <run_id#step#attempt, observation time> | Occurrences: <x of y inspected> | Validation: CONFIRMED (subagent)|CONFIRMED (projection)|CONFIRMED (definition)|CONFIRMED (executor, inspect-only)|INSUFFICIENT_EVIDENCE|NOT_VALIDATABLE | Remediation: <concrete process change and owner>
Recommendations:
- <process improvement, clearly labeled inferred when applicable>
ResidualRisk: none|<short exact risk>
NextAction: none|<exact action>
```

Validation tag semantics:

- `CONFIRMED (subagent)` - an independent validator re-derived the fact from
  raw tool outputs.
- `CONFIRMED (projection)` - triple verification passed: verbatim quote,
  independent projection from a second source, and a documented negative
  check.
- `CONFIRMED (definition)` - the fact is a declared workflow property, cited
  by TOML path and key. Weaker than subagent or projection validation.
- `CONFIRMED (executor, inspect-only)` - the fact required `workflow_inspect`,
  which refuses non-participant child tasks; verified by the executor only.
- `INSUFFICIENT_EVIDENCE` - excluded from Findings. Do not report it.
- `NOT_VALIDATABLE` - interpretation no tool can confirm; excluded from
  Findings, allowed in Recommendations when clearly labeled.

Result semantics:

- `PASS` - analysis complete; every finding validated; `ResidualRisk: none`.
- `PARTIAL` - analysis complete but limited (status fallback used, or
  inspect-level evidence could not be independently refuted).
- `NOT_RUN` - no `.mivia/workflows/`, or zero runs in the window.

Zero-failure window: state the measured absence, give the sample frame, and
report the computable process signals from the sampled runs.
