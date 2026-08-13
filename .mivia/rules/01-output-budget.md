# Output Budget

## During Work

- Status only when it changes user understanding: scope, blocker, imminent multi-file edit, verification progress, or command failure.
- Each status update: one or two short sentences.
- Do not paste raw logs unless asked. Report command + key failing line or reason.
- Do not restate the full task or paste large code already in the tree unless required for a decision.

## Final Responses

Required shape (concise bullets preferred):

1. **Outcome**
2. **Changed files** (absolute or repo-relative paths)
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

Exceed caps only when correctness requires it (e.g. security finding detail, failing test output the user must act on).

## Inter-Agent Messages

`finding`/`question`/`ask`/`answer`/`steer` bodies (`post_message`, `send_to_task`): target ≤4 sentences. State the claim and its evidence pointer (file:line, command, run id); do not restate the parent task or narrate steps already visible in the sender's own findings.

Mechanically backstopped, not advisory-only: `internal/agentmsg.Validate` rejects (does not truncate) any body over `messaging.max_body_bytes` (default 2048 bytes, `internal/config/defaults.go`) before it reaches the ledger. A sender that hits this must shorten and retry — the reject-not-truncate behavior exists so a compressed evidence pointer is never silently cut off mid-message.
