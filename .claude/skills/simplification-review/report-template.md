Use this structure for the report. Keep every conclusion evidence-based. Omit
only sections that have no relevant information.

```text
Result: PASS | FINDINGS | PARTIAL | NOT_RUN
Scope: <reviewed diff, files, or packages and baseline>
Summary: <one sentence>
Evidence:
- <search, reference count, or recorded decision>: <what it establishes and its limits>
Findings:
- [SR-1] <symbol/file: excess or missing simplicity, consumer evidence, cost, simplest sufficient alternative, tradeoff>
RejectedConcerns:
- <candidate rejected by contrary evidence or a recorded decision>
ResidualRisk: none | <specific uncertainty>
NextAction: none | <specific simplification, decision, or measurement>
```

- `PASS`: the scoped code is adequately simple for its demonstrated requirements.
- `FINDINGS`: at least one evidence-backed simplification or pattern-fitness finding.
- `PARTIAL`: required scope or consumer evidence is missing.
- `NOT_RUN`: no reviewable code in scope.
