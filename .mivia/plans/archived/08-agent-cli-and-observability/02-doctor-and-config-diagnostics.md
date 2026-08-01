# 08.02 - Doctor and configuration diagnostics

**Goal:** Report agent loading truthfully even when runtime providers are unavailable.
**Depends on:** [01](01-agent-catalog-cli.md).

## Work

- Extend the grammar to `mivia doctor [--config PATH] [--workspace DIR]`.
  `--config` keeps its existing provider-config meaning; agent discovery still
  reads its trusted global gate from the fixed user config. `--workspace`
  defaults to `.` and is shared with `mivia agents` path resolution.
- Reuse the phase-01 inspection projection. Doctor prints config/provider
  readiness first, then an `agents:` section containing sorted loaded,
  shadowed, malformed/unreadable, and empty/not-present states plus source and
  bounded remediation. It reports `workspace agent files: always loaded` and
  separately `workspace prompts/project skills: enabled|disabled`.
- Missing provider credentials remains a nonzero `doctor` result, but only
  after the complete agent diagnostic is printed. Malformed/unreadable agent
  files also make doctor nonzero; the returned error names only the count and
  class of failures, never a prompt, secret, or raw parser content. If both
  apply, output contains both diagnoses and the exit error deterministically
  reports configuration diagnostics before provider readiness.
- Do not call provider construction, dispatcher construction, or workspace
  tool registration. Do not turn a successful empty collection into a gated
  state.

## RED/GREEN coverage

- `TestDoctorReportsAgentLoadStateBeforeMissingCredentialError`
- `TestDoctorReportsWorkspaceAgentLoadedWhenPromptSkillGateOff`
- `TestDoctorSeparatesEmptyCollectionFromPromptSkillGate`
- `TestDoctorReportsMalformedAgentAlongsideValidDefinitions`
- `TestDoctorExitPrecedenceForMalformedAndMissingCredential`
- `TestDoctorWorkspaceAndConfigFlagGrammar`
- `TestDoctorAgentDiagnosticsContainNoPromptDigestOrSecret`

Run `go test ./internal/cli ./internal/config ./internal/agents -run
'TestDoctor|TestAgentCatalog'`, then `go test -race ./internal/cli
./internal/config ./internal/agents`.
