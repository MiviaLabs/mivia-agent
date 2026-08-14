# Implement (scratch live smoke test)

Task: {{ inputs.task }}

Plan:

{{ evidence.plan }}

Create exactly these two files:

- `docs/e2e-notes/essential.md`: a short markdown file, 2-3 lines, any content.
- `docs/e2e-notes/deferred.md`: a padded markdown file with at least 60 lines
  of filler content (e.g. a numbered list from 1 to 60), so it is large on
  its own.

Reply with these three parts, in order:

1. A `<mivia_output>` opening tag, alone on a line.
2. One JSON object that satisfies the output schema for this task.
3. A `</mivia_output>` closing tag, alone on a line.

### Example

<mivia_output>
{"summary": "Added two scratch notes files.", "files_changed": ["docs/e2e-notes/essential.md", "docs/e2e-notes/deferred.md"], "addressed_findings": [], "inspected": [], "pr_title": "test(test): scratch live smoke test", "pr_summary": "Adds two scratch documentation files for a live delivery smoke test. Safe to close."}
</mivia_output>
