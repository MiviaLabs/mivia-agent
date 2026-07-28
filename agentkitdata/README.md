# agentkitdata — Symlinks for go:embed

This directory uses symlinks so `//go:embed` can reach files at the repo root.
On Linux/macOS, symlinks are created automatically by the module.
On Windows, use mklink or the provided script.

Required symlinks:
- AGENTS.md → ../AGENTS.md
- ai → ../.ai

Run `go generate ./agentkitdata/...` to recreate symlinks if missing.
