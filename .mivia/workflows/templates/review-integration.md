# Integration Review Step

Independently review the implemented delivery for `{{ inputs.task }}` for CROSS-LAYER
interaction defects that no single package-scoped review can see.

Approved plan:

{{ evidence.plan }}

Test plan:

{{ evidence.test_plan }}

Implementation summary:

{{ evidence.implementation }}

Prior code review (when present):

{{ evidence.review }}

Evidence below may be a reference envelope containing a preview and a ledger ref. When it is, read the FULL artifact with workflow_inspect(run_id, step, attempt) instead of relying on the preview.

Read the relevant source and tests. Do not edit files. Do not run commands, commit, push,
publish, or read secret-like files.

Your job is the interaction surface, not a second line-by-line code review. For each surface
below, check whether the change creates, misses, or fails to guard a cross-layer defect, and
verify the claim against the source you actually read. Cite file:line for every finding.

1. **Context budget x tool results**: could a single tool result produced or consumed by this
   change exceed the model prompt budget (tool-result caps are uncapped by default)? Does the
   change interact with compaction triggers, elision, or `EffectivePromptTokens` such that an
   oversized result is sent to the provider, hard-aborts the turn, or is silently dropped?

2. **Retry x state consistency**: does the change interact with any retry path (prompt-too-long
   compact-and-retry, transient provider retry, schema retry)? Could a retry leave stale state
   behind (e.g. a preparation, checkpoint digest, or history snapshot that no longer matches
   what was actually sent), double-retry, or silently lose data without a model-visible notice?

3. **Preparation / commit consistency**: does the change mutate conversation history or
   preparation metadata such that a durable commit's `BaseDigest`/`ActiveContext` could diverge
   from the messages the model actually saw?

4. **Concurrency / cancellation**: does the change introduce shared mutable state, goroutine or
   channel hazards, or interact with steer/cancel paths in a way that could race, leak, or hang?

5. **Boundary honesty**: does the report claim host evidence (tests, gates) the workflow context
   does not provide, or claim behavior that was not actually exercised end-to-end?

Request changes for any confirmed interaction defect. Do not approve a step only because it has
a low-severity finding. Do not request changes based on source you did not inspect. Return only
the declared structured output. List every workspace path you independently inspected in
`inspected`. Use `approved` only when no interaction-surface finding remains. Otherwise use
`changes_requested` and list each finding with severity and a concrete reason that cites the
evidence.

## Output contract

Reply with only a JSON object that satisfies the output schema appended to this task. Do not
use a skill report format, markdown, or extra fields. The schema declares the only valid keys.
An invalid shape is rejected and you will be asked again with the schema.
