package chatsync

import (
	"context"
	"fmt"
	"unicode"
	"unicode/utf8"
)

// AuthorUserIDProvider resolves the local CLI session's own authenticated
// principal id, so a remote input can be checked against it before it is
// ever handed to the UI as something to execute. nil (or an error return)
// means the id cannot be verified, which fails closed: see
// InputPoller.resolveAuthorUserID.
type AuthorUserIDProvider func(ctx context.Context) (string, error)

// allowedRemoteInputKinds is the SessionInput.Kind allowlist. "message"
// injects text as if the user typed it; "cancel" remotely stops the
// session's in-flight turn, mirroring local Ctrl+C/Esc. Both are the only
// kinds this client acts on; a server that starts sending a new kind needs a
// matching addition here before this client will act on it - silent
// pass-through would let an unreviewed instruction shape start executing as
// a local turn.
var allowedRemoteInputKinds = map[string]bool{
	"message": true,
	"cancel":  true,
}

// maxRemoteInputBodyBytes bounds a remote instruction's length. Generous
// enough for a real chat message, small enough that a malformed or hostile
// payload cannot be used to exhaust memory or paste something absurd into a
// local turn.
const maxRemoteInputBodyBytes = 8192

// validateRemoteInput checks a SessionInput already fetched from (and
// acknowledged to) the server against every local trust boundary: it must
// name the session this poller is actually attached to, declare a kind this
// client understands, carry a body shaped like ordinary text, and (when an
// identity provider is configured) be authored by the CLI's own logged-in
// principal. Returns the translated RemoteInput and an empty reason on
// success, or a zero RemoteInput and a human-readable reason on refusal.
//
// This runs on EVERY path that can place a SessionInput onto Inputs() -
// pollOnce's live delivery and recoverPendingInput's crash-recovery replay
// alike - because a persisted pending_input.json is exactly the same
// untrusted server response, just read from disk instead of the wire.
func (p *InputPoller) validateRemoteInput(ctx context.Context, in *SessionInput) (RemoteInput, string) {
	if in == nil {
		return RemoteInput{}, "empty input"
	}
	p.mu.Lock()
	sessID := p.sessionID
	p.mu.Unlock()
	if in.SessionID != sessID {
		return RemoteInput{}, fmt.Sprintf("session id mismatch (got %q, want %q)", in.SessionID, sessID)
	}
	if !allowedRemoteInputKinds[in.Kind] {
		return RemoteInput{}, fmt.Sprintf("unsupported kind %q", in.Kind)
	}
	// A "cancel" input has no meaningful body - it carries an instruction,
	// not text - so the empty-body rejection only applies to kinds that are
	// supposed to carry one.
	if in.Body == "" && in.Kind != "cancel" {
		return RemoteInput{}, "empty body"
	}
	if len(in.Body) > maxRemoteInputBodyBytes {
		return RemoteInput{}, fmt.Sprintf("body exceeds %d bytes", maxRemoteInputBodyBytes)
	}
	if !utf8.ValidString(in.Body) {
		return RemoteInput{}, "body is not valid UTF-8"
	}
	if hasDisallowedControlChar(in.Body) {
		return RemoteInput{}, "body contains a disallowed control character"
	}
	authorID, err := p.resolveAuthorUserID(ctx)
	if err != nil {
		return RemoteInput{}, fmt.Sprintf("author identity unverifiable: %v", err)
	}
	if authorID == "" || in.AuthorUserID != authorID {
		return RemoteInput{}, "author does not match the verified local principal"
	}
	return RemoteInput{
		ID:        in.ID,
		SessionID: in.SessionID,
		Kind:      in.Kind,
		Body:      in.Body,
	}, ""
}

// bidiOverrideChars are the Unicode explicit bidirectional formatting
// characters ("Trojan Source" CVE-2021-42574 class): they change how
// surrounding text DISPLAYS without changing its bytes. unicode.IsControl
// does not catch these - they are category Cf (format), not Cc (control) -
// so a body built from ordinary printable runes could still render
// completely differently than it reads, which matters here specifically
// because this text becomes real model input under whatever approval
// policy is already bound (very likely auto-approve, see
// internal/config/approvals_config.go). Zero-width joiner/non-joiner
// (U+200C/U+200D) are deliberately NOT in this set: they are legitimate in
// Persian, Arabic, and Indic script ligatures, and blocking them would
// reject ordinary text in those languages.
var bidiOverrideChars = map[rune]bool{
	'‪': true, // LEFT-TO-RIGHT EMBEDDING
	'‫': true, // RIGHT-TO-LEFT EMBEDDING
	'‬': true, // POP DIRECTIONAL FORMATTING
	'‭': true, // LEFT-TO-RIGHT OVERRIDE
	'‮': true, // RIGHT-TO-LEFT OVERRIDE
	'⁦': true, // LEFT-TO-RIGHT ISOLATE
	'⁧': true, // RIGHT-TO-LEFT ISOLATE
	'⁨': true, // FIRST STRONG ISOLATE
	'⁩': true, // POP DIRECTIONAL ISOLATE
}

// hasDisallowedControlChar reports whether s contains a control character
// other than tab, newline, and carriage return (the ones ordinary
// multi-line chat text legitimately carries), or a bidi override/isolate
// character - see bidiOverrideChars.
func hasDisallowedControlChar(s string) bool {
	for _, r := range s {
		if r == '\t' || r == '\n' || r == '\r' {
			continue
		}
		if unicode.IsControl(r) || bidiOverrideChars[r] {
			return true
		}
	}
	return false
}

// resolveAuthorUserID resolves and caches this poller's expected author id
// for its whole lifetime: the first call actually invokes the provider (a
// real network round trip for the production Whoami-backed provider), every
// later call - including from recoverPendingInput on the next input - reuses
// the cached result. A failed resolution is cached too: this poller does not
// retry Whoami on every subsequent input, it simply stays unable to verify
// anyone until the process restarts and builds a fresh poller.
func (p *InputPoller) resolveAuthorUserID(ctx context.Context) (string, error) {
	p.mu.Lock()
	if p.authorIDResolved {
		id, err := p.authorID, p.authorIDErr
		p.mu.Unlock()
		return id, err
	}
	p.mu.Unlock()

	var id string
	var err error
	if p.authorUserID == nil {
		err = fmt.Errorf("no author identity provider configured")
	} else {
		id, err = p.authorUserID(ctx)
	}

	p.mu.Lock()
	p.authorIDResolved = true
	p.authorID, p.authorIDErr = id, err
	p.mu.Unlock()
	return id, err
}
