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
	if exceedsLimit(len(data), CurrentLimits().SourceEventBytes) {
		return SanitizedPayload{}, invalid("payload", "exceeds source payload limit")
	}
	if !utf8.Valid(data) {
		return SanitizedPayload{}, invalid("payload", "is not valid UTF-8")
	}
	if err := policy.Classify(data); err != nil {
		return SanitizedPayload{}, err
	}
	ref := newContentRef(principal, data)
	result := SanitizedPayload{Ref: ref, Retention: RetentionSession}
	if !policyConfigured(policy) {
		result.HashOnly = true
		return result, nil
	}
	result.Bytes = append([]byte(nil), data...)
	result.Dereferenceable = true
	return result, nil
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
