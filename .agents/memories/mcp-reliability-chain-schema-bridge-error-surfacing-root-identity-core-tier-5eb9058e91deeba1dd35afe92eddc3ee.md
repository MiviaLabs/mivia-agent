---
id: mcp_reliability_chain_schema_bridge_error_surfacing_root_identity_core_tier_5eb9058e91deeba1dd35afe92eddc3ee
title: 'MCP reliability chain: schema bridge, error surfacing, root-identity core tier'
content: 'Full chain making MCP tools (codegraph) actually usable in mivia.'
importance: medium
tags: [mcp, codegraph, reliability, error-visibility, schema-bridge, dc-9]
updated: 2026-09-04
---

# MCP reliability chain: schema bridge, error surfacing, root-identity core tier

## Summary
Full chain making MCP tools (codegraph) actually usable in mivia: core-tier fix for the root identity, error text surfaced at every layer (validation, transport error, isError content), and the schema bridge no longer collapses description-bearing schemas to an empty object. All verified live in-session.

## What worked
- Four commits on dev: 6e91f4aa (root-identity core tier), 7707daef (surface CallTool error text), a8f6bc4e (schema bridge keeps descriptions + isError content), b6a8b4bb (allowlist codegraph)
- Real root cause of "codegraph never works": bridgeSchemaValue nuked the whole advertised schema over unlisted keys like "description" - model blind to parameters, sent empty args, server rejected
- Verified live: codegraph_explore callable as core tool with query param, no agents, no load_tools; config in repo + ~/.mivia/mivia.toml, codegraph upgraded to v1.6.0, stale v1.5.0 daemons reaped
- Error transparency chain: validation errors land in chat; server isError content now flows through redaction + 512-byte bound

## What did not work
- none

## Why
Three separate suppressions compounded into "codegraph is configured but never works": the schema bridge cut the parameter contract (model sent empty args), CallTool discarded isError content (no one could see why), and the tier plan deferred MCP tools for the root identity (no agents defined). Each layer hid the next; fixing visibility first is what made the rest debuggable.

## References
- internal/mcp/render.go
- internal/mcp/manager.go
- internal/mcp/tool.go
- internal/cliagents/tool_tiers.go
