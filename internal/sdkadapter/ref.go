package sdkadapter

import (
	"errors"

	"github.com/MiviaLabs/mivia-agent/internal/contentref"
	sdkref "github.com/MiviaLabs/mivia-ai-sdk/contextstate"
)

// migrationWindow is the documented cut-off for the dual-parse behaviour.
//
// The bridge accepts both "ref:<kind>:<hex>" (CLI shape) and
// "sha256:<hex>" (SDK shape) for the foreseeable future. Once every CLI
// consumer has been migrated to SDK-shaped references, the CLI format
// branch goes away - per the user-confirmed disposition for B.0.5.
//
// This constant is informational only. No code reads it: a runtime
// time.Now().After(...) gate would break existing CLI consumers the day
// it expired, and the dual-parse path is the safe default. The
// constant's purpose is to anchor the deprecation in code so a future
// reader finds the migration path documented next to the parser, not
// buried in a thread or a memory entry.
const migrationWindow = "2026-12-31"

// ErrMalformedReference is the bridge's fail-closed response when Parse
// sees a string that is neither a CLI reference nor an SDK reference.
// The CLI contentref.ErrMalformed covers the CLI-only path; this
// bridges the union shape.
var ErrMalformedReference = errors.New("sdkadapter: malformed content reference")

// Parse splits a content reference into its (kind, digest) pair. The
// returned kind is the CLI kind (output, error, message) when the input
// is the CLI "ref:<kind>:<hex>" shape; it is empty when the input is the
// SDK "sha256:<hex>" shape, because the SDK shape carries no kind.
//
// Both formats are accepted forever; see migrationWindow.
func Parse(ref string) (kind, digest string, err error) {
	if ref == "" {
		return "", "", ErrMalformedReference
	}
	// First try the SDK shape: "sha256:<hex>". The SDK's
	// contextstate.IsRef reports whether a string is the canonical
	// SDK shape without allocating.
	if isSDKRef := sdkref.IsRef(ref); isSDKRef {
		return "", ref[len(sdkref.HashPrefix):], nil
	}
	// Then try the CLI shape: "ref:<kind>:<hex>".
	if k, d, perr := contentref.Parse(ref); perr == nil {
		return k, d, nil
	}
	return "", "", ErrMalformedReference
}

// Mint returns a content reference for data. The shape depends on kind:
// a non-empty kind produces the CLI "ref:<kind>:<hex>" shape; an empty
// kind produces the SDK "sha256:<hex>" shape.
//
// The CLI shape is produced by contentref.Reference; the SDK shape is
// produced by contextstate.Mint. The bridge never invents a hybrid;
// callers pick the shape they need by passing or omitting kind.
func Mint(kind string, data []byte) string {
	if kind == "" {
		return sdkref.Mint(data)
	}
	return contentref.Reference(kind, data)
}
