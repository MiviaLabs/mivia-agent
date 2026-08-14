# mivia-report/v1

Use this compact, structured report for every skill completion. Keep each field
to one line or short bullet; do not add narrative sections.

```md
ReportFormat: mivia-report/v1
Skill: <skill name>
Result: PASS|BLOCK|PARTIAL|NOT_RUN
Scope: <exact files/packages>
Summary: <one sentence>
Evidence:
- <command or method>: PASS|FAIL|NOT_RUN - <short note>
Findings:
- none
ResidualRisk: none|<short exact risk>
NextAction: none|<exact action>
```

When `Scope` includes any path under `internal/workflows/`, `NextAction` must
not be `none`: state the exact live e2e smoke workflow offered to the user
(`e2e-split-test`, `e2e-pr-metadata-test`, `e2e-scope-escape-test`, or an ad
hoc `scripts/run-delivery-workflow.sh` task) - unit tests passing is not
"verified" for this subsystem (see the ADLC's Step 5 gate note). A report
with `Scope` in `internal/workflows/` and `NextAction: none` is itself a
defect an auditor must flag.
