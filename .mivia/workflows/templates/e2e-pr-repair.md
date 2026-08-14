# Repair (scratch PR-metadata smoke test)

The harness hint below tells you what to repair:

{{ evidence.delivery_hint }}

This scratch run tests the host's PR-title/commit-subject repair path. The
hint above rejected `pr_title`. Do NOT edit any file - resubmit
`files_changed` exactly as the previous attempt reported it:
`["testdata/e2e-smoke/pr-metadata.md"]`. Set `pr_title` to EXACTLY this literal
string, unchanged: `test(test): fixed pr title` - it satisfies the
`type(scope): subject` shape and stays under any reasonable length limit.

Reply with these three parts, in order:

1. A `<mivia_output>` opening tag, alone on a line.
2. One JSON object that satisfies the output schema for this task.
3. A `</mivia_output>` closing tag, alone on a line.

### Example

<mivia_output>
{"summary": "Fixed pr_title to satisfy the workspace commit-message policy per the hint.", "files_changed": ["testdata/e2e-smoke/pr-metadata.md"], "addressed_findings": [], "inspected": [], "pr_title": "test(test): fixed pr title", "pr_summary": "Adds one scratch documentation file for a live PR-metadata repair smoke test. Safe to close."}
</mivia_output>
