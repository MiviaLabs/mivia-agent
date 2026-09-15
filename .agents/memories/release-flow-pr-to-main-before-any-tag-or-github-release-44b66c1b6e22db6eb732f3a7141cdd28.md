---
id: release_flow_pr_to_main_before_any_tag_or_github_release_44b66c1b6e22db6eb732f3a7141cdd28
title: 'Release flow: PR to main before any tag or GitHub release'
content: 'Release process for MiviaLabs repos: push release branch, open PR to main first; do NOT push tags or create GitHub releases until the PR is merged and the user explicitly asks.'
importance: medium
x-scope: project
x-verdict: bad
tags: [release-process, github, git, process]
updated: 2026-09-15
---

# Release flow: PR to main before any tag or GitHub release

## Summary
Release process for MiviaLabs repos: push release branch, open PR to main first; do NOT push tags or create GitHub releases until the PR is merged and the user explicitly asks.

## What worked
["Branch pushed, then PR to main created (mivia-ai-sdk#19); tag/release only after merge and explicit go-ahead"]

## What did not work
["Pushed the v0.7.0 tag and created a GitHub release directly off the release branch before the PR existed - user deleted the release and corrected the process"]

## Why
Releases must go through review/merge on main first; cutting a release from an unmerged release branch skips the PR gate and was explicitly rejected by the user.

## References
- none
