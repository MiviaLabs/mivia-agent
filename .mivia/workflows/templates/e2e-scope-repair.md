# Repair (scratch chunk-scope smoke test)

The harness hint below tells you what to repair:

{{ evidence.delivery_hint }}

This scratch run tests the host's chunk-scope guard repair path. The hint
above refused the delivery because a file is outside the chunk's declared
slice. DELETE the out-of-scope file `testdata/e2e-smoke/scope-extra.md`
(remove it from the worktree), keep `testdata/e2e-smoke/scope-ok.md`
unchanged, and resubmit `files_changed` as exactly
`["testdata/e2e-smoke/scope-ok.md"]`.

Reply with these three parts, in order:

1. A `<mivia_output>` opening tag, alone on a line.
2. One JSON object that satisfies the output schema for this task.
3. A `</mivia_output>` closing tag, alone on a line.

### Example

<mivia_output>
{"summary": "Deleted the out-of-scope file per the scope-guard hint.", "files_changed": ["testdata/e2e-smoke/scope-ok.md"], "addressed_findings": [], "inspected": [], "pr_title": "test(test): scope guard smoke", "pr_summary": "Adds one scratch file for a live chunk-scope guard smoke test. Safe to close."}
</mivia_output>
