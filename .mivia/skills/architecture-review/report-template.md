Use this structure for the report. Keep every conclusion evidence-based. Omit
only sections that have no relevant information.

```text
Result: PASS | BLOCK | PARTIAL | NOT_RUN
Scope: <reviewed artifacts and baseline>
Summary: <one sentence>
Drivers:
- <requirement or quality scenario>
Evidence:
- <artifact, search, or check>: <what it establishes and its limits>
Findings:
- [AR-1] <finding with consequence, alternative, tradeoff, and action>
RejectedConcerns:
- <candidate rejected by contrary evidence>
ResidualRisk: none | <specific uncertainty>
NextAction: none | <specific decision, evidence, or change required>
```

- `PASS`: adequate evidence and no blocking structural gap.
- `BLOCK`: a confirmed requirement-threatening flaw or unenforced unsafe sequencing.
- `PARTIAL`: useful review, but required scope, evidence, a decision, or measurement is missing.
- `NOT_RUN`: no reviewable architecture.
