Implement {{ task }} using this approved plan:

{{ plan }}

## PR metadata

Provide `pr_title` and `pr_summary` in your structured output.

`pr_title` is a custom PR title. Follow the project PR-title policy.
The host validates `pr_title`. If the host rejects `pr_title`, the run returns to the repair_pr_metadata step to fix it.

`pr_summary` has exactly two sentences. State what the change does in the first sentence.
State why the change is needed in the second sentence.

Return only the declared structured output.
