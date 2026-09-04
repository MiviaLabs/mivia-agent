---
id: no_per_agent_spend_ceilings
title: Do not add timeout_seconds/max_tokens ceilings to .agents/agents/*.md
content: Keep max_turns=0 (unlimited); rely on session-level caps only.
importance: medium
tags: [mivia, agents, config, budget]
---

Do not add `timeout_seconds` or `max_tokens` ceilings to `.agents/agents/*.md`,
and keep `max_turns = 0` (unlimited).

Ceilings kill agents mid-work on large scopes. The user explicitly rejected
this proposal even though the parser comment in
`internal/config/agents_parse.go` warns about unbounded spend.

Rely on session-level caps only. If spend becomes a real problem, raise it as
a question rather than unilaterally adding per-agent ceilings.
