# 09.02 - Loader-backed agent examples

**Goal:** Ship test-only, source-safe examples whose documented behavior is
proved through the real parser, discovery, and resolver boundaries.
**Depends on:** shipped plans `05`–`07`; it must reach GREEN before phase 01
publishes the documentation contract.

## Exact files and semantics

```text
internal/config/testdata/agent-examples/
  user-mivia.toml                 # trusted [agents] gate fixture only
  user-agents/researcher.toml     # full tools baseline + explicit skills
  user-agents/engineer.toml       # inherits researcher + tools delta
  workspace-agents/reviewer.toml  # separate workspace provenance/name
```

The fixture names are test-only and are never copied into the repository's
live `.mivia/agents/` directory. They omit `system_prompt`, `model`, and any
credential/path-bearing setting. The user global fixture explicitly exercises
the gate without presenting it as workspace authority. The example set covers
full-list versus inherited delta semantics, skill nil/non-nil behavior, and
distinct user/workspace provenance without relying on a default `mivia` name.

Retain `.mivia/mivia.toml.example` as its current full configuration example;
extend `internal/config/load_test.go` only as needed to validate its agent
comments/config shape. Do not describe it as a minimal agent-only sample.

## RED/GREEN tests

- Add `TestAgentExampleFixturesParseAndResolve`: parse every agent fixture;
  copy the global fixture to temporary `~/.mivia/mivia.toml` and load its
  `[agents]` fragment through `LoadAgentsGlobal`; resolve using
  `tools.AllToolNames` and a named fixture skill catalogue; assert names,
  parent, effective tools, skills, and no empty toolset. The gate-only global
  fixture deliberately has no provider catalog, so it must not be passed to
  `config.Load`.
- Add `TestAgentExampleFixturesDiscoverWithTrustBoundaries`: copy the fixtures
  into temporary HOME/workspace locations; call discovery/`LoadAndResolveOpts`;
  assert user/workspace provenance, user-wins shadows, workspace definitions
  still load when the user gate is off, and the gate remains independently
  represented for prompt/project-skill loading.
- Add a table-driven fixture-mutation test for unknown key, filename/name
  mismatch, unknown tool, unknown skill, `tools` plus a delta, and missing
  inherited parent. Each mutation must fail at its intended parser/resolver
  boundary.
- Add fixture-hygiene coverage that refuses `system_prompt`, credential-like
  values, provider/model fields, and absolute paths in committed example files.
  `make secret-scan` remains a repository gate, not proof of loader semantics.
- Do not duplicate existing unit tests merely to count coverage. Reuse their
  helpers where possible, but keep the composed fixture/trust test separate
  from `TestProjectAgentDefinitionsResolve`, which validates live repo agents.

Run `go test ./internal/config ./internal/agents -run 'TestAgentExample'` RED
then GREEN, followed by `go test -race ./internal/config ./internal/agents`.
