---
id: root_cause_no_live_progress_when_resume_attaches_to_automation_session_e2d4a826560b73b05f38ef5cb1f8dcb3
title: 'Root cause: no live progress when /resume attaches to automation session'
content: 'Automation sessions whose chat.Session lacks the tool surface (AgentTurnEnabled()==false) run the plain turn path, which emits no content events; the live view never repaints history on live turn.end, so /resume shows nothing until a tab switch. Toolless sessions are silent because background spawns must not write the tool-scope notice slot.'
importance: high
x-scope: project
x-verdict: neutral
tags: [automation, live-view, tui, root-cause, turn-events]
updated: 2026-09-12
---

# Root cause: no live progress when /resume attaches to automation session

## Summary
Automation sessions whose chat.Session lacks the tool surface (AgentTurnEnabled()==false) run the plain turn path, which emits no content events; the live view never repaints history on live turn.end, so /resume shows nothing until a tab switch. Toolless sessions are silent because background spawns must not write the tool-scope notice slot.

## What worked
- none

## What did not work
- none

## Why
Reproduced end to end with the production wiring (internal/newtui/repro_live_resume_test.go): the live tee (SubscribeLive fan-out) and the screen's live view are both correct when events exist. The divergence is upstream: sendPlain publishes only turn.start/turn.end; Conversation.Send passes io.Discard as the reply writer; and handleLiveEvent's turn.end path never reloads history (only handleLiveStale does). Also noted: SendUserWithTurnOptions returns the reply text, which Conversation stamps as the terminal event's TurnID - turn.end carries the full assistant reply as TurnID in both paths.

## References
- internal/newtui/repro_live_resume_test.go
- internal/chat/session.go
- internal/uiadapter/session_pool_worktree.go
- internal/uiadapter/conversation.go
- internal/ui/screen/conversation/live_view.go
