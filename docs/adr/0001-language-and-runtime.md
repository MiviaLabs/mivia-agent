# ADR 0001: Language and Runtime

## Status

Accepted

## Context

Mivia needs a local CLI agent that can fan out many concurrent subagents without
thrashing a developer machine. The agentkit MVP validated control-surface ideas
but used a transitional product identity.

## Decision

- Host language: **Go**
- Binary name: **`mivia`**
- Module path: `github.com/MiviaLabs/mivia-agent`
- Subagents: in-process tasks with shared resource pools

## Consequences

- Strong concurrency and single-binary ship story
- Tooling ecosystem for agents is thinner than Python; MCP covers polyglot tools
- Race detector and explicit caps are first-class quality requirements
