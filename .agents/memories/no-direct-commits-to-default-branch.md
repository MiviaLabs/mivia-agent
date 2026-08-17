---
id: no_direct_commits_to_default_branch
title: Interactive "commit" = local commit on the current branch, no PR
content: When the user directly says "commit" in an interactive session, commit locally on the current branch and stop - do not open a PR. The branch+PR flow is what the ADLC agent-workflow delivery pipeline does on its own; it is not what "commit" means when the user is driving. There is no "master" branch in this repo (or its sibling repos) - GitHub's default branch here is `main`; `dev` is the active integration branch PRs target. Never assume a branch is named "master."
importance: high
tags: git, commits, workflow
---

Two distinct commit paths exist in this repo and they are not
interchangeable:

1. **Autonomous agent-workflow deliveries** (`mivia workflow run` /
   `workflow resume --allow-publish`) open a PR on their own via
   `delivery.Deliver` - that is the ADLC pipeline's own behavior, not
   something a human asked for turn by turn. This produced #205-#211 and
   onward, targeting `dev`.
2. **Interactive work**, where the user is in the loop telling the agent
   what to do turn by turn: when the user says "commit," commit locally on
   the current branch and stop there. Do not create a feature branch, do
   not open a PR, do not push unless separately asked. The current
   integration branch (`dev` here) is not the same risk as pushing straight
   to a release/production branch.

**Branch naming:** there is no `master` branch in this repo, or in its
sibling repos (`mivia-ai-sdk`, `mivia-agent-desktop`) - all use `main` as
GitHub's default branch (`mivia-agent` also keeps `dev` as the active
integration branch its PRs target; a `backup/pre-squash-master` branch is a
historical artifact, not a live branch). Never write "master" in code,
commit messages, or docs for these repos - say "the default branch" or name
the actual branch (`main`, `dev`).

**Why:** corrected 2026-08-18 after the agent (wrongly) branched, committed,
and attempted `gh pr create` in response to a plain "commit" - the user
rejected the PR step and clarified: "what do u mean open PR? merge/commit to
local dev." The agent then also used "master" in this very memory's title
alongside "dev," which the user corrected in the same session: this repo (and
its siblings) never had a `master` branch to begin with. Both corrections
land in one lesson: default to the smallest, most literal action and the
most literal, verified terminology - do not assume a heavier flow (PR) or a
conventional-but-wrong name (`master`) applies without checking.

**How to apply:** "commit" alone -> `git commit` on the current branch, no
push, no PR. "push" -> also `git push`. "open a PR" / "PR" -> only then
branch off and open one. Before naming a branch in prose, verify it exists
(`git branch -a`, `gh repo view --json defaultBranchRef`) rather than
defaulting to "master."
