package chatsync

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// errorBodySnippetLimit caps the raw-body fallback in an error message. A 400
// body can be a whole HTML page or a binary blob; without a cap one rejected
// append floods the log and the stop reason with it.
const errorBodySnippetLimit = 512

// errorBodySnippet builds the message text for a response that carried no
// usable error envelope.
//
// An empty body - or a read that failed outright - gets explicit wording, not
// a bare status line, so an operator can tell "the server said nothing" from
// "the server's answer was lost". A readable but non-envelope body becomes a
// bounded snippet: capped on a rune boundary, control characters removed, so
// the text stays valid to log, store, and show. A body that is all invalid
// text - the sanitizer strips every byte of it - gets the same explicit
// wording, because an empty snippet names nothing either.
func errorBodySnippet(body []byte, readErr error) string {
	// Sanitize before the cap: Map turns one invalid byte into a three-byte
	// replacement rune, so capping first could ship a snippet past the limit.
	// A snippet that holds nothing but replacement runes carries no usable
	// text - the body was binary, or the transport lost it - and gets the
	// explicit wording instead.
	empty := "the response body was empty or unreadable"
	snippet := truncateRuneBoundary(sanitizeErrorSnippet(string(body)), errorBodySnippetLimit)
	if strings.ContainsFunc(snippet, func(r rune) bool { return r != utf8.RuneError }) {
		return snippet
	}
	if readErr != nil {
		return fmt.Sprintf("%s: %v", empty, readErr)
	}
	return empty
}

// truncateRuneBoundary cuts s to at most max bytes without splitting a rune.
// A byte-index cut turned a truncated multi-byte rune into an invalid
// sequence, and invalid text must not travel on the wire or into a log.
func truncateRuneBoundary(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	for len(cut) > 0 {
		r, size := utf8.DecodeLastRuneInString(cut)
		if r != utf8.RuneError || size > 1 {
			break
		}
		cut = cut[:len(cut)-1]
	}
	return cut
}

// sanitizeErrorSnippet removes control characters a raw body can carry. A NUL
// alone rejects a whole batch at the store, and a bell or escape byte has no
// place in an operator-facing message. No fast path: strings.Map over clean
// text returns it unchanged, and a guard here is a branch no test can tell
// from the map itself.
func sanitizeErrorSnippet(s string) string {
	return strings.Map(func(r rune) rune {
		if isControlRune(r) {
			return -1
		}
		return r
	}, s)
}

func isControlRune(r rune) bool {
	return r < 0x20 || r == 0x7f
}
