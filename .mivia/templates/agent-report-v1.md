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
