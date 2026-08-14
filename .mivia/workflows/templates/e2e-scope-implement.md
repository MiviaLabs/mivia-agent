# Implement (scratch chunk-scope smoke test)

Task: {{ inputs.task }}

Chunk scope (the declared slice for this chunk-mode run):

{{ inputs.chunk_scope }}

This scratch run deliberately tests the host's chunk-scope guard. Create
BOTH of these files, on purpose - the second one is OUTSIDE the declared
chunk slice and the host must refuse the delivery because of it:

1. `testdata/e2e-smoke/scope-ok.md` - a short markdown file, 2-3 lines.
2. `testdata/e2e-smoke/scope-extra.md` - a short markdown file, 2-3 lines.

Report BOTH files in `files_changed` honestly.

Reply with these three parts, in order:

1. A `<mivia_output>` opening tag, alone on a line.
2. One JSON object that satisfies the output schema for this task.
3. A `</mivia_output>` closing tag, alone on a line.

### Example

<mivia_output>
{"summary": "Added the in-scope note and one deliberate out-of-scope note.", "files_changed": ["testdata/e2e-smoke/scope-ok.md", "testdata/e2e-smoke/scope-extra.md"], "addressed_findings": [], "inspected": [], "pr_title": "test(test): scope guard smoke", "pr_summary": "Adds one scratch file for a live chunk-scope guard smoke test. Safe to close."}
</mivia_output>
