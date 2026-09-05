---
id: load_tools_deferred_stage_shadows_the_synchronous_unadmitted_tool_hot_path_64f326f742bb650f4ab75af081511c34
title: 'load_tools deferred stage shadows the synchronous unadmitted-tool hot path'
content: 'The mivia agent loop hot-load path is shadowed whenever load_tools staged the tool and publication deferred.'
importance: medium
tags: [load_tools, admission, mcp, tool-surface, design-gap, staged-tools]
updated: 2026-09-04
---

# load_tools deferred stage shadows the synchronous unadmitted-tool hot path

## Summary
The mivia agent loop already has a same-turn hot-load path (Options.UnadmittedToolHandler → serveUnadmittedTool → admitForExecution, sync execution with approval+budget), but it is shadowed whenever load_tools staged the tool and publication deferred: StagedToolMessage is checked first and returns the "retry next step" denial. Calling load_tools therefore yields a worse experience than calling the

## What worked
- Traced full mechanism: load_tools → stage → Surface hook publication at step boundary
- Located both code paths and the ordering decision in agentloop_tool_error.go
- Found the deferral rationale: R2-1/R2-2 dispatcher fencing in internal/chat/admission_status.go (sibling turn / background run must not have its dispatcher widened/closed mid-flight)

## What did not work
- none

## Why
User feedback (design complaint): requesting a tool via load_tools and then calling it should hot-load immediately, not defer to the next step boundary. The code confirms the sync path exists but is intentionally ordered after StagedToolMessage (agentloop_tool_error.go:125-127 comment). Any fix must handle double-charging the admission attempt and respect R2-1/R2-2 dispatcher fencing.

## References
- internal/agent/agentloop_tool_error.go
- internal/chat/session_turn_surface.go
- internal/chat/admission_status.go
