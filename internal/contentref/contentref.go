// Package contentref owns the single canonical minter for content references.
//
// Reference and Parse in this file are the ONE place where a
// "ref:<kind>:<digest>" string is produced or decoded. Every other site that
// needs a content reference must call Reference rather than formatting its own
// string, so that minting and parsing can never drift apart. The invariant this
// protects: "A reference handed to the model resolves, or it is not handed to
// the model."
//
// This package deliberately depends on nothing but the standard library. The
// minter has to be reachable from every layer that emits a reference -
// including internal/runtime, which cannot import internal/ledger without
// creating an import cycle through internal/storage's tests - so the format
// lives in a leaf package rather than alongside the store that persists it.
// internal/ledger re-exports this API as ledger.Reference/ledger.ParseReference
// for the callers that already depend on the ledger.
package contentref

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
)

// Reference kinds for content-addressed task results.
const (
	KindOutput = "output"
	KindError  = "error"
)

// ErrMalformed reports a reference that is not in canonical form.
var ErrMalformed = errors.New("malformed content reference")

// digestLen is the length of a hex-encoded SHA-256 digest.
const digestLen = 64

// Reference returns the canonical reference for data: "ref:<kind>:<64 hex>".
// It returns "" for empty data or an unrecognised kind. Minting a kind that
// Parse would reject would create an unparseable reference, so it is refused
// here rather than emitted.
func Reference(kind string, data []byte) string {
	if len(data) == 0 || !knownKind(kind) {
		return ""
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("ref:%s:%x", kind, sum)
}

// Parse splits a canonical reference into its kind and hex digest, returning
// ErrMalformed for any other shape. Surrounding whitespace is never trimmed:
// such a reference is malformed.
func Parse(ref string) (kind, digest string, err error) {
	parts := strings.Split(ref, ":")
	if len(parts) != 3 || parts[0] != "ref" || !knownKind(parts[1]) {
		return "", "", ErrMalformed
	}
	if !isLowerHexDigest(parts[2]) {
		return "", "", ErrMalformed
	}
	return parts[1], parts[2], nil
}

// knownKind reports whether kind is one this package will mint and parse.
func knownKind(kind string) bool {
	return kind == KindOutput || kind == KindError
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
