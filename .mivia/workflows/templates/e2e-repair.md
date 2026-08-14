# Repair (scratch live smoke test)

If a delivery rejection routed this step, the harness hint below tells you what to repair:

{{ evidence.delivery_hint }}

Fix exactly what the hint describes. Do not invent a `deferred_files` field -
the host owns any diff-size split entirely.

Reply with these three parts, in order:

1. A `<mivia_output>` opening tag, alone on a line.
2. One JSON object that satisfies the output schema for this task.
3. A `</mivia_output>` closing tag, alone on a line.

### Example

<mivia_output>
{"summary": "Repaired the reported delivery rejection.", "files_changed": [], "addressed_findings": [], "inspected": [], "pr_title": "test(test): scratch live smoke test", "pr_summary": "Adds two scratch documentation files for a live delivery smoke test. Safe to close."}
</mivia_output>
