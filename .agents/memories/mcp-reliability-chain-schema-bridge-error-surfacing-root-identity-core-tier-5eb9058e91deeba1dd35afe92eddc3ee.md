---
id: mcp_reliability_chain_schema_bridge_error_surfacing_root_identity_core_tier_5eb9058e91deeba1dd35afe92eddc3ee
title: 'MCP reliability chain: schema bridge, error surfacing, root-identity core tier'
content: 'Full chain making MCP tools (codegraph) actually usable in mivia: core-tier fix for the root identity, error text surfaced at every layer, and a schema bridge that no longer collapses description-bearing schemas.'
importance: medium
tags: [mcp, codegraph, reliability, error-visibility, schema-bridge, tool-tiers, root-identity, load_tools, dc-9]
related: [load_tools_deferred_stage_shadows_the_synchronous_unadmitted_tool_hot_path_64f326f742bb650f4ab75af081511c34, two_paths_execute_a_tool_call]
updated: 2026-09-11
---

# MCP reliability chain: schema bridge, error surfacing, root-identity core tier

## Summary
Full chain making MCP tools (codegraph) actually usable in mivia: core-tier fix for the root identity, error text surfaced at every layer (validation, transport error, isError content), and the schema bridge no longer collapses description-bearing schemas to an empty object. All verified live in-session.

## What worked
- Four commits on dev: 6e91f4aa (root-identity core tier), 7707daef (surface CallTool error text), a8f6bc4e (schema bridge keeps descriptions + isError content), b6a8b4bb (allowlist codegraph)
- Real root cause of "codegraph never works": bridgeSchemaValue nuked the whole advertised schema over unlisted keys like "description" - model blind to parameters, sent empty args, server rejected
- Verified live: codegraph_explore callable as core tool with query param, no agents, no load_tools; config in repo + ~/.mivia/mivia.toml, codegraph upgraded to v1.6.0, stale v1.5.0 daemons reaped
- Error transparency chain: validation errors land in chat; server isError content now flows through redaction + 512-byte bound
- Root-identity core tier, in detail: `withMCPServerToolsAlwaysCore` returned early on `selected == nil`, so the root identity kept MCP tools deferred even though `SetupSessionMCPTools` attaches exactly those servers. Fix: `identityMCPServerScope(selected, res)` mirrors `SelectedOrGlobalMCPServers`, and the tier split now exempts the root's global MCP tools from deferral. Proven by an e2e test with NO agent files that fails under the old behavior (tool named only in the prompt's deferred index) and passes after.

## What did not work
- Deferring a configured, successfully-connected global MCP server behind `load_tools`. It is advertised but locked, and step-boundary publication can defer indefinitely (sibling turns, background orchestration) - the exact "codegraph configured but never used" report.

## Why
Three separate suppressions compounded into "codegraph is configured but never works": the schema bridge cut the parameter contract (model sent empty args), CallTool discarded isError content (no one could see why), and the tier plan deferred MCP tools for the root identity (no agents defined, since the workspace runs the root identity because no agent named `config.DefaultAgentName` exists in `.agents/agents/`). Each layer hid the next; fixing visibility first is what made the rest debuggable.

## References
- internal/mcp/render.go
- internal/mcp/manager.go
- internal/mcp/tool.go
- internal/cliagents/tool_tiers.go
- internal/cliagents/mcp_scope.go
- internal/clichat/chat_mcp_entrypoint_integration_test.go
- internal/cliagents/tool_tiers_mcp_test.go

## History
- Merged in `mcp_tools_core_tier_fix_for_the_root_identity_codegraph_usable_without_agents_17f9018285674d91d776054fc513fa95` on 2026-09-11 (housekeeping: that file documented commit 6e91f4aa, which this one already listed as a bullet; its root-cause paragraph, "did not work" entry, and test references are folded in above).
