package controller

import (
	"context"
	"unicode/utf8"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// maxErrorTextBytes bounds stored failure detail for one attempt.
const maxErrorTextBytes = 4096

// storeErrorText persists a bounded, tail-truncated error message
// content-addressed and returns its reference. It returns "" when the cause
// is nil or persistence fails: a failed attempt must still complete, so
// missing detail never fails the attempt CAS.
func storeErrorText(ctx context.Context, repo workflowledger.Repository, cause error) string {
	if cause == nil {
		return ""
	}
	text := truncateTail(cause.Error(), maxErrorTextBytes)
	ref := "sha256:" + workflowledger.DigestHex([]byte(text))
	if err := repo.StoreContent(ctx, ref, []byte(text)); err != nil {
		return ""
	}
	return ref
}

// truncateTail keeps the last max bytes of s without splitting a UTF-8 rune.
// Error chains read wrapper-first and root cause last, so the tail preserves
// the root cause. Truncation is deterministic: identical input yields
// identical output and therefore an identical content reference.
func truncateTail(s string, max int) string {
	if len(s) <= max {
		return s
	}
	start := len(s) - max
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}
