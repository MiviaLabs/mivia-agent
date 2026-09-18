// Package chat is the interactive chat REPL: the session loop, slash
// commands, session and agent tools, and the plain and terminal rendering
// paths.
//
// It may import the runtime packages and the other internal/cli
// subpackages. internal/cli and cmd/mivia wire it at process start; it
// must not import internal/cli.
package chat
