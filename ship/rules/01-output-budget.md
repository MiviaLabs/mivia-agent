# Output Budget

## During Work

- Status only when it changes user understanding: scope, blocker, imminent multi-file edit, verification progress, or command failure.
- Each status update: one or two short sentences.
- Do not paste raw logs unless asked. Report command + key failing line or reason.
- Do not restate the full task or paste large code already in the tree unless required for a decision.

## Final Responses

Required shape (concise bullets preferred):

1. **Outcome**
2. **Changed files** (repo-relative paths)
3. **Verification** (commands + result)
4. **Residual risk / blockers**

Forbidden in final responses unless user asked:

- Broad rationale essays
- Repeated context from earlier turns
- External research dumps unrelated to the result
- Full file dumps when a path + summary suffices

## Task Slicing

- Implementation: finish one production unit + its tests before the next unit.
- Audits: concrete findings with file references and the test that would catch each issue.
- Handoffs: read-first files, exact scope, verification commands, mutation-proof targets.
- Multi-hour work: checkpoint after each verifiable slice; do not batch silent changes across unrelated packages.

## Budget Defaults

| Artifact | Default cap |
|----------|-------------|
| Status update | ≤ 2 sentences |
| Final response (routine change) | ≤ ~25 lines unless multi-file inventory requires more |
| Inline code in chat | Only when reviewing or when no path exists yet |
| Log excerpts | ≤ 15 lines, scrubbed |
