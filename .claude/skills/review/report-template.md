# Routing report template

```text
Verdict: <route | skip | escalate>

Routes:
- primary: <lens-name>
  rationale: <one line>
- secondary: <lens-name> | none
  rationale: <one line>

Skipped:
- <lens-name>: <reason>

If escalate, attach:
- reason: <why this router cannot decide>
- suggested resolution: <split, dispatch orchestrator, or human>
```