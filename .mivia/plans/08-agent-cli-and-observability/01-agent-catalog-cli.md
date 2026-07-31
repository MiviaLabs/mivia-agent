# 08.01 — Agent catalog CLI

**Goal:** Inspect selectable named-agent definitions from the immutable registry without provider or dispatcher construction.
**Depends on:** plan `05`.

## Work

- Add `mivia agents list` with selectable user/workspace definitions, source,
  gate state, effective tools, model policy, and turn budget.
- Show the private compiled root fallback separately as `root fallback`; never
  make it selectable or expose it as a file-backed name.
- Add `mivia agents explain <name>` for inheritance, winning sources, deltas,
  guardrails, disabled tools, and file paths.
- Sanitize descriptions and errors; never print raw system prompts, secrets, or
  model dumps.

## Verification

`TestAgentsListShowsSelectableDefinitionsOnly`,
`TestAgentsListShowsRootFallbackSeparately`,
`TestAgentsExplainShowsInheritanceAndWinningSources`,
`TestAgentsExplainDoesNotPrintRawSystemPrompt`,
`TestAgentsListWorksWithoutProviderKey`, and
`TestUnknownAgentListsAvailableNamesAndSources`.
