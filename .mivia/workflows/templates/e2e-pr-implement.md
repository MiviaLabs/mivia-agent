# Implement (scratch PR-metadata smoke test)

Task: {{ inputs.task }}

Plan:

{{ evidence.plan }}

Create exactly one file: `testdata/e2e-smoke/pr-metadata.md`, a short markdown
file, 2-3 lines, any content.

This scratch run deliberately tests the host's PR-title/commit-subject
policy check. Set `pr_title` to EXACTLY this literal string, unchanged:
`add stuff` (no scope, on purpose - this is expected to be rejected).

Reply with these three parts, in order:

1. A `<mivia_output>` opening tag, alone on a line.
2. One JSON object that satisfies the output schema for this task.
3. A `</mivia_output>` closing tag, alone on a line.

### Example

<mivia_output>
{"summary": "Added one scratch note file.", "files_changed": ["testdata/e2e-smoke/pr-metadata.md"], "addressed_findings": [], "inspected": [], "pr_title": "add stuff", "pr_summary": "Adds one scratch documentation file for a live PR-metadata repair smoke test. Safe to close."}
</mivia_output>
