---
id: mcp_tools_core_tier_fix_for_the_root_identity_codegraph_usable_without_agents_17f9018285674d91d776054fc513fa95
title: 'MCP tools core-tier fix for the root identity (codegraph usable without agents)'
content: 'Fixed global MCP servers being deferred behind load_tools for root identity.'
importance: medium
tags: [mcp, codegraph, tool-tiers, root-identity, load_tools, regression-test]
updated: 2026-09-04
---

# MCP tools core-tier fix for the root identity (codegraph usable without agents)

## Summary
Fixed global (=true) MCP servers (e.g. codegraph) being deferred behind load_tools for the root/no-agent-selected identity, which made them effectively uncallsble: advertised but locked, and step-boundary publication can defer indefinitely. Now they are always core, callable turn one, with no agents required in .agents/agents.

## What worked
- Root cause: withMCPServerToolsAlwaysCore returned early on selected == nil, so the root identity (GlobalServerIDs scope) kept MCP tools deferred even though SetupSessionMCPTools attaches exactly those servers
- Fix: identityMCPServerScope(selected, res) mirrors SelectedOrGlobalMCPServers; tier split now exempts the root's global MCP tools from deferral (internal/cliagents/tool_tiers.go, mcp_scope.go isMCPServerToolForServers)
- Proof: new e2e test with NO agent files fails under old behavior (tool named in prompt's deferred index) and passes with the fix; fakeProviderServer now stitches cache-marked system content parts before matching
- make go-check (fmt + go test ./... + vet) green; make build rebuilt ./mivia

## What did not work
- none

## Why
The workspace runs the root identity because no agent named config.DefaultAgentName exists in .agents/agents/. The tier plan only exempted selected agents' EffectiveMCPServers from deferral, so a configured, successfully-connected global MCP server was advertised yet never callable without load_tools, whose publication can defer again (sibling turns, background orchestration) - the exact "codegraph configured but never used" report.

## References
- internal/cliagents/tool_tiers.go
- internal/cliagents/mcp_scope.go
- internal/clichat/chat_mcp_entrypoint_integration_test.go
- internal/cliagents/tool_tiers_mcp_test.go
