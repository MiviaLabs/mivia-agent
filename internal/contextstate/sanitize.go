package contextstate

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// contentRefDomain separates payload keys from every other digest this package
// mints, and versions the derivation itself.
const contentRefDomain = "mivia.context.payload.ref.v2"

// RedactionPolicy is host-owned classifier configuration. A policy with no
// configured classifier is intentionally treated as unconfigured.
type RedactionPolicy struct {
	Configured bool               `json:"configured"`
	Patterns   []string           `json:"patterns,omitempty"`
	KeyNames   []string           `json:"key_names,omitempty"`
	Classifier func([]byte) error `json:"-"`
	// Redactor replaces sensitive spans in a source payload. It is supplied by
	// the host so this package keeps one redaction implementation rather than
	// growing a second that drifts from it. Without one, a flagged payload is
	// stored as metadata only - never refused.
	Redactor func([]byte) []byte `json:"-"`
}

// SanitizeSourcePayload is the host boundary before any context bytes are
// persisted. Unconfigured policies deliberately produce metadata only.
func SanitizeSourcePayload(ctx context.Context, principal Principal, data []byte, policy RedactionPolicy) (SanitizedPayload, error) {
	if err := contextError(ctx); err != nil {
		return SanitizedPayload{}, err
	}
	if err := principal.Validate(); err != nil {
		return SanitizedPayload{}, err
	}
	if !principal.IsBound() {
		return SanitizedPayload{}, fmt.Errorf("%w: owner capability is not bound", ErrPrincipalMismatch)
	}
	// Whole-payload size is uncapped: large source events persist via ordered
	// chunks (PayloadChunkSize). Per-chunk invariants are enforced at storage.
	if !utf8.Valid(data) {
		return SanitizedPayload{}, invalid("payload", "is not valid UTF-8")
	}
	// A privacy rule may change WHAT is stored; it may never destroy a turn the
	// agent already finished. Classification used to return an error here, and
	// because publication is one transaction that refused the whole turn and
	// wedged the session (INV-AG-35). A flagged payload is redacted when the
	// host supplied a redactor, and degrades to metadata otherwise - the
	// unconfigured behaviour, which stores nothing and so leaks nothing.
	//
	// Summaries keep the opposite treatment on purpose: they are host-generated
	// and regenerable, so refusing one keeps model output carrying a secret out
	// of storage and off the wire at no cost to the user's conversation.
	stored, storable := redactSourcePayload(data, policy)
	ref := newContentRef(principal, stored)
	result := SanitizedPayload{Ref: ref, Retention: RetentionSession}
	if !policyConfigured(policy) || !storable {
		result.HashOnly = true
		return result, nil
	}
	result.Bytes = append([]byte(nil), stored...)
	result.Dereferenceable = true
	return result, nil
}

// redactSourcePayload returns the bytes to describe and whether they are safe
// to store. A clean payload is storable as-is. A flagged one is cleaned by the
// host's redactor and re-classified, because a redactor that missed something
// must not be trusted to have cleaned it; anything still flagged - or flagged
// with no redactor configured - reports the ORIGINAL bytes as unstorable, so
// the reference still describes the real message while nothing is written.
func redactSourcePayload(data []byte, policy RedactionPolicy) ([]byte, bool) {
	if !policyConfigured(policy) {
		return data, false
	}
	if policy.Classify(data) == nil {
		return data, true
	}
	if policy.Redactor == nil {
		return data, false
	}
	cleaned := policy.Redactor(data)
	if !utf8.Valid(cleaned) || policy.Classify(cleaned) != nil {
		return data, false
	}
	return cleaned, true
}

// Classify applies the host-owned redaction rules without minting a content
// reference. Summary validation uses the same classifier before model output
// can be persisted or sent to a provider.
func (policy RedactionPolicy) Classify(data []byte) error {
	for _, pattern := range policy.Patterns {
		if strings.TrimSpace(pattern) == "" {
			return invalid("redaction.patterns", "must not contain empty patterns")
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return invalid("redaction.patterns", "contains an invalid classifier")
		}
		if re.Match(data) {
			return invalid("payload", "rejected by the configured classifier")
		}
	}
	text := strings.ToLower(string(data))
	for _, key := range policy.KeyNames {
		key = strings.TrimSpace(strings.ToLower(key))
		if key == "" {
			return invalid("redaction.key_names", "must not contain empty names")
		}
		if strings.Contains(text, key) {
			return invalid("payload", "rejected by the configured key classifier")
		}
	}
	if policy.Classifier != nil {
		if err := policy.Classifier(data); err != nil {
			if errors.Is(err, ErrInvalidDTO) {
				return err
			}
			return fmt.Errorf("%w: host classifier rejected payload", ErrInvalidDTO)
		}
	}
	return nil
}

func policyConfigured(policy RedactionPolicy) bool {
	return policy.Configured && (len(policy.Patterns) > 0 || len(policy.KeyNames) > 0 || policy.Classifier != nil)
}

func newContentRef(principal Principal, data []byte) ContentRef {
	digest := sha256.Sum256(data)
	hexDigest := hex.EncodeToString(digest[:])
	return ContentRef{
		Ref: contentRefID(principal, hexDigest), Namespace: Namespace, SHA256: hexDigest,
		WorkspaceID: principal.WorkspaceID, SessionID: principal.SessionID,
		SubjectID: principal.SubjectID, Size: len(data),
	}
}

// contentRefID is the payload's durable primary key, scoped to its owner.
//
// It used to be the bare content digest, while a payload row is stored under a
// GLOBAL primary key whose owner columns must equal the writing principal. The
// first session in a workspace store to persist a byte sequence therefore owned
// that key forever: any later session that produced identical content - the
// same message retyped into a second `mivia chat` run - had its ENTIRE commit
// transaction rejected as a checkpoint conflict, leaving no source events, no
// checkpoint and no operation row, and losing the turn from durable history.
// Sharing the row instead is not the alternative: reads gate on the row's owner
// columns, so one session resolving another's payload is exactly the boundary
// this package exists to hold.
//
// The owner tuple is length-prefixed rather than concatenated, so principals
// that differ only in where their field boundaries fall cannot collapse onto
// one key. Content that repeats within ONE owner still yields one key, which is
// the deduplication the global digest did provide.
func contentRefID(principal Principal, contentDigest string) string {
	scope := sha256.New()
	for _, part := range []string{contentRefDomain, principal.WorkspaceID, principal.SessionID, principal.SubjectID, contentDigest} {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(part)))
		_, _ = scope.Write(length[:])
		_, _ = scope.Write([]byte(part))
	}
	return "ctxp_" + hex.EncodeToString(scope.Sum(nil))
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
