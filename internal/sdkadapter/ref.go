package sdkadapter

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

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

// Reference kinds for content-addressed task results and agent messages.
// The bridge owns the CLI kind vocabulary: every package that needs to
// emit or recognise a "ref:<kind>:<digest>" string imports these
// constants from internal/sdkadapter instead of minting its own. Four
// kinds cover every content reference today: tool output, tool error,
// agent-to-agent message bodies, and a subagent's recorded tool-call
// step trace (operator-facing only - never handed to the model).
const (
	KindOutput    = "output"
	KindError     = "error"
	KindMessage   = "message"
	KindToolCalls = "tool_calls"
)

// ErrMalformedReference is the bridge's fail-closed response when Parse
// sees a string that is neither a CLI reference nor an SDK reference.
var ErrMalformedReference = errors.New("sdkadapter: malformed content reference")

// digestLen is the length of a hex-encoded SHA-256 digest. The CLI shape
// and the SDK shape share the digest width; only the prefix differs.
const digestLen = 64

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
	if k, d, ok := parseCLI(ref); ok {
		return k, d, nil
	}
	return "", "", ErrMalformedReference
}

// parseCLI splits a canonical CLI reference into its kind and digest.
// It returns ok=false for any other shape. Surrounding whitespace is
// never trimmed: such a reference is malformed.
func parseCLI(ref string) (kind, digest string, ok bool) {
	parts := strings.Split(ref, ":")
	if len(parts) != 3 || parts[0] != "ref" || !knownKind(parts[1]) {
		return "", "", false
	}
	if !isLowerHexDigest(parts[2]) {
		return "", "", false
	}
	return parts[1], parts[2], true
}

// knownKind reports whether kind is one the bridge will mint and parse.
func knownKind(kind string) bool {
	return kind == KindOutput || kind == KindError || kind == KindMessage || kind == KindToolCalls
}

// isLowerHexDigest reports whether s is exactly a lowercase hex SHA-256 digest.
func isLowerHexDigest(s string) bool {
	if len(s) != digestLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Mint returns a content reference for data. The shape depends on kind:
// a non-empty kind produces the CLI "ref:<kind>:<hex>" shape by inlining
// the SDK digest and prefixing with "ref:<kind>:"; an empty kind produces
// the SDK "sha256:<hex>" shape.
//
// The CLI shape is computed inline here rather than delegated to a
// second package, so this file is the one canonical minter for the CLI
// shape. The SDK shape is delegated to contextstate.Mint. The bridge
// never invents a hybrid; callers pick the shape they need by passing
// or omitting kind.
func Mint(kind string, data []byte) string {
	if kind == "" {
		return sdkref.Mint(data)
	}
	if !knownKind(kind) || len(data) == 0 {
		return ""
	}
	return fmt.Sprintf("ref:%s:%x", kind, sha256.Sum256(data))
}
