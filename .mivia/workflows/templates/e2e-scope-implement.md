# Implement (scratch chunk-scope smoke test)

Task: {{ inputs.task }}

Chunk scope (the declared slice for this chunk-mode run):

{{ inputs.chunk_scope }}

This scratch run deliberately tests the host's chunk-scope guard. Create
BOTH of these ordinary files, on purpose:

1. `testdata/e2e-smoke/scope-ok.md` - a short markdown file, 2-3 lines.
2. `testdata/e2e-smoke/scope-extra.md` - a short markdown file, 2-3 lines.

Neither file is write-blocklisted or otherwise forbidden - both are ordinary
writes you are allowed to make and must make. Report BOTH files honestly in
`files_changed`. Leave `blocked_paths` empty: that field is only for a write
the write tools themselves refused (a permission error), which will not
happen here - it is not for a file that happens to be outside the chunk's
declared scope. Scope is a separate, later check the host performs on its
own; do not anticipate or self-report it.

Reply with these three parts, in order:

1. A `<mivia_output>` opening tag, alone on a line.
2. One JSON object that satisfies the output schema for this task.
3. A `</mivia_output>` closing tag, alone on a line.

### Example

<mivia_output>
{"summary": "Added the in-scope note and one deliberate out-of-scope note.", "files_changed": ["testdata/e2e-smoke/scope-ok.md", "testdata/e2e-smoke/scope-extra.md"], "addressed_findings": [], "inspected": [], "pr_title": "test(test): scope guard smoke", "pr_summary": "Adds one scratch file for a live chunk-scope guard smoke test. Safe to close."}
</mivia_output>
