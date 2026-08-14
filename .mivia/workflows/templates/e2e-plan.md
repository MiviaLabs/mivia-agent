# Plan (scratch live smoke test)

Task: {{ inputs.task }}

Write a short plan for adding two small documentation files:
`docs/e2e-notes/essential.md` (a few lines) and `docs/e2e-notes/deferred.md`
(a longer, padded file, at least 60 lines). Do not implement yet.

Reply with these three parts, in order:

1. A `<mivia_output>` opening tag, alone on a line.
2. One JSON object that satisfies the output schema for this task.
3. A `</mivia_output>` closing tag, alone on a line.

### Example

<mivia_output>
{"summary": "Add two scratch notes files for a live smoke test.", "steps": ["Create docs/e2e-notes/essential.md", "Create docs/e2e-notes/deferred.md"], "addressed_findings": [], "inspected": []}
</mivia_output>
