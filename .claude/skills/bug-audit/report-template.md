Choose exactly one output shape and emit no preamble.

For a confirmed or suspected defect:

```markdown
### N. High: short title

Confidence: Confirmed | Suspected

Contract violated:
- Expected invariant.

Evidence:
- Exact expression or literal code evidence.

Reachable path:
- Input and branch sequence that reaches the failure.

Impact:
- Concrete user, operator, security, tenant, or data consequence.

Why safeguards failed:
- Why existing guards or tests do not prevent this.

Remediation:
- Smallest correct fix boundary.

Regression:
- Test name or boundary that must fail before the fix.
```

When no defect is confirmed, emit only: `No real bug was found.`
