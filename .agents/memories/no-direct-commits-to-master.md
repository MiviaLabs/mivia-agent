---
id: no_direct_commits_to_master
title: Do not commit directly to master/dev without explicit request
content: Default to branch + PR; commit straight to the default branch only when the user explicitly asks for it in that message.
importance: high
tags: git, commits, workflow
---

Work in this repo lands via a branch and a pull request by default - this
mirrors the ADLC delivery pipeline's own behavior (`mivia workflow run`
opens a PR through `delivery.Deliver`) and this repo's live PR history
(#205-#211 and onward).

Commit straight to `master` or the current default branch (`dev`) ONLY when
the user explicitly asks for that in the message that triggers the commit.
That grant is one-time, not a standing preference - ask again (or default
back to branch+PR) on the next commit unless told otherwise again.

This reverses an earlier working assumption ("sole maintainer, PRs are
overhead, always commit to master directly") that no longer matches how
this repo is actually operated.
