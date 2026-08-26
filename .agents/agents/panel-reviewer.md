---
name: panel-reviewer
description: Review workspace changes with local read-only tools.
tools:
- read_file
- list_dir
- grep
- glob
- find_references
disallowed_tools:
- post_message
provider: llmproxycli
model: claude-sonnet-5
---
