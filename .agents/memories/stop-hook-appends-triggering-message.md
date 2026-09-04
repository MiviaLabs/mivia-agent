---
id: stop_hook_appends_triggering_message
title: agentloop ContinueOnStop fires after the triggering assistant message is already in history
content: The SDK loop appends resp.Message to the run history BEFORE the ContinueOnStop decision, so a continuation can only append and the host must drop empty assistant shapes at the trim input and the session write-back.
importance: high
tags: [agentloop, ContinueOnStop, mivia-ai-sdk, empty-response, continuation]
updated: 2026-09-04
---

## When this applies

Any host code that continues a graceful stop through `agentloop.Options.ContinueOnStop` (mivia-ai-sdk v0.1.3 and later), and any code that reads `StopDecision.History` at such a stop.

## The corrected assumption

A continuation is not a replay from a clean pre-turn history. At `StopEmptyResponse` and `StopNoToolCalls`, `run.go` has already appended `resp.Message`, so the triggering assistant message sits inside `d.History`. A hook that returns messages APPENDS to that grown history; it cannot remove or rewrite anything in it.

## Why it matters

A retry built on the old replay assumption silently duplicates the triggering assistant message, or appends a message that makes the NEXT iteration's trim fail. The genuinely-empty assistant shape is the sharp edge: this repo's message-shape validation hard-rejects it, so one unchecked shape fails every later turn's context preparation - the poison `provider.DropEmptyAssistantTurns` exists to remove.

## What to do instead

- Treat `StopDecision.History` as already containing the triggering message; never append it again.
- Return only the new host notice (user-role, bracket-labelled) when continuing.
- Keep `provider.DropEmptyAssistantTurns` at both host seams: the trim input (before preparation validates) and the history write-back (before persistence).
- Keep the pin tests green: `TestWriteBackSDKHistoryDropsEmptyAssistantMessage` (agent) and `TestFinishAgentTurn_EmptyResponseDoesNotPoisonNextTurnsPreparation` (chat).
