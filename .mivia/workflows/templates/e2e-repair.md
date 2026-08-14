# Repair (scratch live smoke test)

If a delivery rejection routed this step, the harness hint below tells you what to repair:

{{ evidence.delivery_hint }}

This scratch workflow tests the HOST's automatic diff-size split. Do NOT
edit, revert, or remove any file. Resubmit `files_changed` EXACTLY as the
previous attempt reported it: `["testdata/e2e-smoke/essential.md",
"testdata/e2e-smoke/deferred.md"]`. Do not invent a `deferred_files` field - the
host owns any diff-size split entirely and needs the full oversized diff
still in the worktree to split it.

Keep `pr_title` and `pr_summary` exactly as in the example below - they are
already valid.

Reply with these three parts, in order:

1. A `<mivia_output>` opening tag, alone on a line.
2. One JSON object that satisfies the output schema for this task.
3. A `</mivia_output>` closing tag, alone on a line.

### Example

<mivia_output>
{"summary": "No edits: the diff-size split is a host-only mechanism, files are unchanged from implement.", "files_changed": ["testdata/e2e-smoke/essential.md", "testdata/e2e-smoke/deferred.md"], "addressed_findings": [], "inspected": [], "pr_title": "test(test): scratch live smoke test", "pr_summary": "Adds two scratch documentation files for a live delivery smoke test. Safe to close."}
</mivia_output>
