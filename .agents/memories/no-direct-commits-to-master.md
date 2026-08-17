---
id: no_direct_commits_to_master
title: Interactive "commit" = local commit on dev, no PR; PRs are for autonomous workflow deliveries
content: When the user directly says "commit" in an interactive session, commit locally on the current branch (dev) and stop - do not open a PR. The branch+PR flow is what the ADLC agent-workflow delivery pipeline does on its own; it is not what "commit" means when the user is driving.
importance: high
tags: git, commits, workflow
---

Two distinct commit paths exist in this repo and they are not
interchangeable:

1. **Autonomous agent-workflow deliveries** (`mivia workflow run` /
   `workflow resume --allow-publish`) open a PR on their own via
   `delivery.Deliver` - that is the ADLC pipeline's own behavior, not
   something a human asked for turn by turn. This produced #205-#211 and
   onward.
2. **Interactive work**, where the user is in the loop telling the agent
   what to do turn by turn: when the user says "commit," commit locally on
   the current branch and stop there. Do not create a feature branch, do
   not open a PR, do not push unless separately asked. `dev` is not
   production `master` - a local commit on `dev` is not the same risk as
   pushing straight to `master`.

**Why:** corrected 2026-08-18 after the agent (wrongly) branched, committed,
and attempted `gh pr create` in response to a plain "commit" - the user
rejected the PR step and clarified: "what do u mean open PR? merge/commit to
local dev." This itself reverses an EARLIER memory on the same topic ("sole
maintainer, always commit straight to master, PRs are overhead") that also
turned out wrong - so the durable rule is: default to the smallest, most
local action (commit on the current branch, no push, no PR) and let the user
explicitly ask for anything bigger (push, PR, commit to master), rather than
guessing which "bigger" flow applies.

**How to apply:** "commit" alone -> `git commit` on the current branch, no
push, no PR. "push" -> also `git push`. "open a PR" / "PR" -> only then
branch off and open one. If already mid-way through creating a branch/PR for
something the user just said "commit" for, stop, merge back down, and ask
before continuing that heavier flow.
