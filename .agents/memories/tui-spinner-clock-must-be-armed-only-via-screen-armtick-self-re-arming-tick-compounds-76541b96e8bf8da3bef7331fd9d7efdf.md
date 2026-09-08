---
id: tui_spinner_clock_must_be_armed_only_via_screen_armtick_self_re_arming_tick_compounds_76541b96e8bf8da3bef7331fd9d7efdf
title: 'TUI spinner clock must be armed only via Screen.armTick (self-re-arming tick compounds)'
content: 'statusline.TickMsg is self-re-arming (each tick returns the next TickCmd), so any unconditional statusline.TickCmd() starts an ADDITIONAL permanent clock rather than replacing one. Subagent progress events armed one per event, multiplying spinner speed and full-cockpit repaints. Fixed with Screen.armTick()/disarmTick() guarded by a pointer-shared tickArmed flag; armTick is now the only way to star'
importance: high
x-scope: project
x-verdict: good
tags: [tui, spinner, bubbletea, subagents, performance]
updated: 2026-09-08
---

# TUI spinner clock must be armed only via Screen.armTick (self-re-arming tick compounds)

## Summary
statusline.TickMsg is self-re-arming (each tick returns the next TickCmd), so any unconditional statusline.TickCmd() starts an ADDITIONAL permanent clock rather than replacing one. Subagent progress events armed one per event, multiplying spinner speed and full-cockpit repaints. Fixed with Screen.armTick()/disarmTick() guarded by a pointer-shared tickArmed flag; armTick is now the only way to star

## What worked
- none

## What did not work
- none

## Why
This defect class recurs: adding a plain statusline.TickCmd() at any new call site silently doubles the animation rate and the repaint load for the whole UI. The invariant (one surface, one clock, pointer-shared flag across Screen copies and the embedded thread screen) needs to be known before touching conversation-screen tick wiring.

## References
- internal/ui/screen/conversation/conversation.go
- internal/ui/screen/conversation/events.go
- internal/ui/component/statusline/statusline.go
