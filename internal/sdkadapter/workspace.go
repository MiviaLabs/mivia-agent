// Package sdkadapter — workspace bridge.
//
// The CLI's internal/workspace/ package is the full lifecycle host:
// the mivia-specific namespacing helpers (AgentsPath, SkillsDir,
// SessionsDir, WorktreesDir, and ContextStorePath) in
// namespace.go, the os.Root-based sandbox primitives in root.go,
// and the longpath platform handling in longpath_unix.go /
// longpath_windows.go. None of these move to the SDK; they are
// CLI product code (the namespacing) or product-specific
// primitives (the longpath handling).
//
// This file re-exports the SDK's workspace.Workspace, Options,
// and the four sentinels (ErrEscape, ErrInvalidLimit, ErrSecretPath,
// ErrTooLarge) so future code that wants the canonical SDK
// sandbox shape reaches it through the bridge without importing
// the SDK directly. The CLI's internal/workspace/ continues to
// be where the namespacing and platform handling live; the
// bridge is the entry point, not a replacement.
package sdkadapter

import (
	sdkws "github.com/MiviaLabs/mivia-ai-sdk/workspace"
)

// Workspace re-exports the SDK's workspace.Workspace. The CLI
// reaches the bridge, never the SDK directly, so the SDK
// dependency stays inside internal/sdkadapter. The bridge is a
// type alias on purpose: the local code that has *sdkws.Workspace
// via the SDK's own wiring (B.2 #8, when it lands) shares the
// same pointer the bridge returns, and methods dispatched through
// either name reach the same Open/ReadFile/WriteFile/List/Stat
// implementation without a wrapper allocation.
type Workspace = sdkws.Workspace

// Options re-exports the SDK's workspace.Options.
type Options = sdkws.Options

// Open re-exports the SDK's Open (opens a *Workspace rooted at
// root, with default options).
func Open(root string) (*Workspace, error) {
	return sdkws.Open(root)
}

// OpenWith re-exports the SDK's OpenWith (opens a *Workspace
// with caller-supplied options including MaxReadBytes and Deny).
func OpenWith(opts Options) (*Workspace, error) {
	return sdkws.OpenWith(opts)
}

// DefaultMaxReadBytes re-exports the SDK's DefaultMaxReadBytes
// (10 MiB) so CLI callers can reference the constant through
// the bridge without an extra SDK import.
const DefaultMaxReadBytes = sdkws.DefaultMaxReadBytes

// Unbounded re-exports the SDK's Unbounded sentinel (-1) that
// signals "no read-size cap" to ReadFileLimit.
const Unbounded = sdkws.Unbounded

// Re-exported sentinels so CLI callers can errors.Is against
// sdkadapter.ErrEscape etc. without an extra SDK import.
var (
	ErrEscape       = sdkws.ErrEscape
	ErrInvalidLimit = sdkws.ErrInvalidLimit
	ErrSecretPath   = sdkws.ErrSecretPath
	ErrTooLarge     = sdkws.ErrTooLarge
)
