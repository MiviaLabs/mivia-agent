Use this structure for the report. Every conclusion must trace to an executed
measurement. Omit only sections that have no relevant information.

```text
Result: PASS | FINDINGS | PARTIAL | NOT_RUN
Scope: <reviewed code, workload, and baseline>
Summary: <one sentence>
Environment: <hardware, OS, runtime/toolchain versions, load conditions>
Measurements:
- <command>: <aggregate result with variance and run count>
Artifacts: none | <profile files written to temporary locations and their disposal>
Findings:
- [PERF-1] <location: measured cost, baseline delta, consequence, simplest remedy, tradeoff>
RejectedConcerns:
- <suspected issue the profile or benchmark disproved>
ResidualRisk: none | <unmeasured workload or environment gap>
NextAction: none | <specific measurement, decision, or change>
```

- `PASS`: measurements show the scoped code meets its requirement or introduces no regression.
- `FINDINGS`: at least one measurement-backed regression or inadequacy.
- `PARTIAL`: required measurements could not run or no baseline is available.
- `NOT_RUN`: no reviewable scope.
