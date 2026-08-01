package contextstate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

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
	if len(data) > MaxSourceEventBytes {
		return SanitizedPayload{}, invalid("payload", "exceeds source payload limit")
	}
	if !utf8.Valid(data) {
		return SanitizedPayload{}, invalid("payload", "is not valid UTF-8")
	}
	for _, pattern := range policy.Patterns {
		if strings.TrimSpace(pattern) == "" {
			return SanitizedPayload{}, invalid("redaction.patterns", "must not contain empty patterns")
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return SanitizedPayload{}, invalid("redaction.patterns", "contains an invalid classifier")
		}
		if re.Match(data) {
			return SanitizedPayload{}, invalid("payload", "rejected by the configured classifier")
		}
	}
	text := strings.ToLower(string(data))
	for _, key := range policy.KeyNames {
		key = strings.TrimSpace(strings.ToLower(key))
		if key == "" {
			return SanitizedPayload{}, invalid("redaction.key_names", "must not contain empty names")
		}
		if strings.Contains(text, key) {
			return SanitizedPayload{}, invalid("payload", "rejected by the configured key classifier")
		}
	}
	if policy.Classifier != nil {
		if err := policy.Classifier(data); err != nil {
			if errors.Is(err, ErrInvalidDTO) {
				return SanitizedPayload{}, err
			}
			return SanitizedPayload{}, fmt.Errorf("%w: host classifier rejected payload", ErrInvalidDTO)
		}
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

func policyConfigured(policy RedactionPolicy) bool {
	return policy.Configured && (len(policy.Patterns) > 0 || len(policy.KeyNames) > 0 || policy.Classifier != nil)
}

func newContentRef(principal Principal, data []byte) ContentRef {
	digest := sha256.Sum256(data)
	hexDigest := hex.EncodeToString(digest[:])
	return ContentRef{
		Ref: "ctxp_" + hexDigest, Namespace: Namespace, SHA256: hexDigest,
		WorkspaceID: principal.WorkspaceID, SessionID: principal.SessionID,
		SubjectID: principal.SubjectID, Size: len(data),
	}
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
