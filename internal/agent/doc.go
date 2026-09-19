// Package agent runs the SDK-backed agent turn loop: it builds the loop's
// options, drives each turn, translates SDK events into the CLI's event
// stream, and shapes tool results against the turn budget.
//
// It may import internal/provider, internal/provider/reasoning, internal/tools,
// internal/sdkadapter, internal/runtime, and the other runtime packages below
// it. The cli, cli/chat, and composition packages may import it.
package agent
