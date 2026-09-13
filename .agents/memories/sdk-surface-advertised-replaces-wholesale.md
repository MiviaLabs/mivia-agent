---
id: sdk_surface_advertised_replaces_wholesale
title: SDK Surface.Advertised replaces the offered tool set wholesale - nil clears it
content: agentloop.applySurface assigns next.defs = s.Advertised unconditionally, so any non-nil Surface with a nil Advertised erases every tool definition; only a nil *Surface keeps the previous set.
importance: high
x-scope: project
x-verdict: bad
tags: [sdk, agent-loop, tools, surface, nil-semantics, dc-27]
related: [provider_audit_dir_shows_the_real_request, two_paths_execute_a_tool_call, hand_enumerated_struct_fields]
updated: 2026-09-11
---

# A nil field in `agentloop.Surface` does not mean "no change"

`mivia-ai-sdk/agentloop/surface.go` `applySurface`:

```go
next := runSurface{defs: s.Advertised, schemas: compileSchemas(s.Advertised), ...}
if s.Registry != nil { next.reg = s.Registry }
if s.Scope != nil { next.scope = s.Scope }
```

`Registry` and `Scope` are keep-on-nil. `Advertised` is **not**: it is
assigned unconditionally. The only way to keep the previous offered set is
to return a nil `*Surface` from the hook.

## What it cost

`bridgeSDKBridgeSurface` (`internal/agent/agentloop_adapter.go`) set
`out.Advertised` only when the host's per-step hook carried `ToolSpecs`,
and returned a non-nil Surface whenever it carried a Registry. Its own
comment guarded the both-nil case and missed the registry-only one.

`internal/chat`'s hook carries `ToolSpecs` only when the session pinned an
advertised snapshot, and the launch attach was the only place that pinned
one. So every session built anywhere else - headless automation runs,
pooled TUI sessions, subagent and workflow loops - had its ENTIRE tool
array erased at the first step boundary. The model answered in prose, the
turn ended, the step succeeded and the run reported success: a `bug-audit`
run finished in 12 seconds having read two files.

The fix restates the rotated registry's own definitions
(`sdkagentloop.Definitions(reg, nil)` - the host never sets a Scope, so
nil matches what the SDK computed for request 0) when the host pins none.

## How to apply

- Treat every field of an SDK options struct as replace-on-apply until the
  SDK's own apply function says otherwise. Read `applySurface`, do not
  infer from the field name or from a sibling field's behaviour.
- A per-step hook that can return a partially-populated struct needs a test
  for EVERY combination of populated fields, not just all-set and all-nil.
  The missing case here was exactly one combination.
- Symptom shape to recognise: a run that ends successfully after one tool
  roundtrip, with a final assistant message that reads like a plan. That is
  a model with no tools, not a model that gave up.
