# 08.03 - Identity and observability

**Goal:** Keep definition, instance, and model-generation identity distinct across user-facing runtime surfaces.
**Depends on:** [01](01-agent-catalog-cli.md), [02](02-doctor-and-config-diagnostics.md), and shipped `07`.

## Data contract and ownership

- Define an `events`-owned typed identity payload rather than using
  `Event.Metadata`: `DefinitionName`, `DefinitionSource`, `InstanceID`, and
  `ModelGeneration`. Values are copied only from canonical runtime state and
  validated/bounded at construction. The type has no fields for digest, path,
  prompt, content, error, arbitrary metadata, or tools.
- `chat.Session` owns a binding-generation counter protected by its existing
  lock. `NewSession` publishes generation 1; `SwitchBinding` increments it
  only after all idle/switch/catalog checks succeed. A turn captures that
  binding generation with its model/provider snapshot.
- `agentSessionState` owns the root definition identity and configured baseline
  prompt/turn settings. `agentTaskHandler` combines its immutable definition
  with a freshly generated opaque invocation ID for routed lifecycle identity;
  it must never use caller/model-controlled run or task IDs. Existing task
  name/digest ledger fields remain authorization-only; no ledger-event schema
  or root-chat persistence change is in scope.
- One CLI event-enrichment helper is the only publisher for root and routed
  lifecycle boundaries. It attaches identity to session/turn and subagent
  start/end events, and bridges agent-loop events without changing their raw
  content fields or copying new content into the identity payload. Effective
  tool names are rendered once in CLI catalog/explain only, not events.

## Runtime behavior

- Preserve selected root definition identity, prompt provenance, and effective
  scope across `/model`; the new binding gets a new generation while the
  definition and instance ID do not change.
- Preserve current `model` semantics: a definition's bare `model` is the
  spawned task default. Thread the active provider-qualified catalog through
  the task-handler options and validate that default immediately before the
  selected routed task runs; an unrelated definition must not block root
  startup or handler registration. A root `/model` override remains
  provider/catalog validated and has no effect on a spawned definition's
  default. Do not add an allowed-model field or call this a model policy.
- Make `/agent` switching transactional. Build candidate prompt, max turns,
  scoped registry, dispatcher, and identity before committing. On success,
  replace selection and binding together; on failure retain the old state.
  Applying a nil prompt/max-turn field restores the startup baseline.
- Add `/agents` as a read-only list command in the slash catalogue, classic
  dispatcher, and TUI dispatcher. Keep `/agent [name]` as the only selector.
  Both commands must have matching help, completion, line-mode, and TUI
  behavior.
- Plain REPL banner/status and TUI chrome/status show active definition name,
  source, and current model generation without exposing paths/digests. No
  persistence or resume claim is added for root chat sessions.

## RED/GREEN coverage

- `TestSlashAgentsWorksInPlainREPLLineModeAndTUI`
- `TestSlashCatalogHelpAndCompletionExposeAgentCommandsOnBothSurfaces`
- `TestREPLAndTUIShowActiveAgentAndGeneration`
- `TestAgentDefinitionAndInstanceIdentityAreDistinct`
- `TestModelGenerationMonotonicWhenSwitchingBack`
- `TestModelSwitchPreservesRootAgentDefinitionPromptAndScope`
- `TestRootAgentSwitchFailureLeavesPreviousStateUntouched`
- `TestRootAgentSwitchRestoresBaselineForUnsetPromptAndTurns`
- `TestSpawnedAgentModelDefaultUsesActiveProviderCatalog`
- `TestModelSwitchDoesNotChangeSpawnedAgentDefault`
- `TestAgentLifecycleIdentityContainsNoPromptPathDigestToolsOrContent`
- `TestAgentLifecycleIdentityDoesNotExposeCallerTaskID`
- `TestConcurrentInstancesShareDefinitionButNotInstanceIdentity`
- retain `TestResumeFailsWhenAgentDefinitionChangesBeforeLedgerMutation` as
  the routed-task regression from `07`; do not relabel it as root-chat resume.

Run focused tests in `./internal/chat ./internal/events ./internal/agent
./internal/cli ./internal/coordinator ./internal/ledger ./internal/subagents`,
then `go test -race` on those affected packages. Mutation proofs cover dropped
generation increments, non-atomic agent switch, missing identity enrichment,
and event payloads widened with a forbidden field.
