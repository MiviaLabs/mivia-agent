# 08.01 - Agent catalog CLI and diagnostics seam

**Goal:** Inspect resolved named-agent definitions without provider, dispatcher,
or workspace-tool construction.
**Depends on:** shipped plans `05` and `06`.

## Owned seams and contract

- Add an error-collecting discovery/report API below the CLI that returns
  ordered file diagnostics plus every safely resolved definition. It preserves
  existing fail-closed runtime loading: chat still rejects a malformed selected
  collection. Inspection can instead report each malformed/unreadable file and
  continue with independent files.
- Add a resolution trace owned by `internal/agents`, populated while resolving:
  parent chain; final source/path; per-field winner for description, model,
  prompt presence, and max turns; tool baseline/add/remove operations;
  guardrail removals; and skill allowlist result. It must never retain prompt
  text in the public trace. Do not reconstruct a trace from a flattened
  `ResolvedAgent`.
- Add a pure CLI catalog projection/formatter owned by `internal/cli`. It
  consumes the report and emits a fixed, sorted human format. List rows show
  `name`, `source`, `state`, definition-effective tools, spawned-task model
  default (`(inherit session)` when empty), and turn budget. The fallback is a
  separate non-selectable row. Explain shows the selected row's bounded path,
  parent chain, field winners, tool deltas/guardrails, effective denylist, and
  skill scope; never `SystemPrompt` or its digest.
- Wire `root.go` and usage through one `runAgents` parser:
  `agents list [--workspace DIR]` and `agents explain <name> [--workspace
  DIR]`. Reject extra arguments and unknown subcommands/flags. Command output
  goes to stdout; warnings/diagnostics go to stderr through injectable writers
  for tests. Exit nonzero for an unknown explain name or any malformed/unreadable
  collection entry, after printing safe diagnostic rows.
- Workspace definitions are always candidates. User-name shadows are reported
  as `shadowed by user`, while the user `load_workspace_config` state is shown
  separately as `workspace prompts/project skills: enabled|disabled`.

## RED/GREEN coverage

- `TestAgentsListShowsSelectableDefinitionsOnly`
- `TestAgentsListShowsRootFallbackSeparately`
- `TestAgentsListReportsWorkspaceAgentsRegardlessOfGate`
- `TestAgentsExplainShowsResolutionTraceAndWinningSources`
- `TestAgentsExplainDoesNotPrintSystemPromptDigestOrSecret`
- `TestAgentsListWorksWithoutProviderKeyOrDispatcher`
- `TestAgentsListStableOrderingAndFieldOrder`
- `TestUnknownAgentListsAvailableNamesAndSources`
- `TestAgentCatalogReportsMalformedFileWithoutDroppingValidDefinition`
- `TestAgentsCommandRejectsInvalidGrammar`

Run `go test ./internal/config ./internal/agents ./internal/cli -run
'TestAgents|TestAgentCatalog'` before and after each owned implementation
micro-task, then `go test -race ./internal/config ./internal/agents
./internal/cli` at the phase gate.
