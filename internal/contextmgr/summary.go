package contextmgr

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

const (
	maxSummaryFieldBytes = 2 * 1024
	maxSummaryItems      = 32
)

// UntrustedSummary is a validated data-only result from a summarizer. The
// private seal and private payload prevent callers from manufacturing an
// authority-bearing summary with a struct literal.
type UntrustedSummary struct {
	value  Summary
	sealed bool
}

// Value returns a defensive copy for host framing. The returned Summary is
// data only; it has no tool, policy, credential, or dispatcher fields.
func (s UntrustedSummary) Value() Summary {
	return cloneSummary(s.value)
}

func (s UntrustedSummary) Validate() error {
	if !s.sealed {
		return fmt.Errorf("%w: summary is not host-validated", contextstate.ErrInvalidDTO)
	}
	return validateSummaryValue(s.value, s.value.SourceRange)
}

// ValidateSummary validates provider output against the exact request that
// produced it and seals it as untrusted state data.
func ValidateSummary(summary Summary, request SummaryRequest) (UntrustedSummary, error) {
	if err := request.Validate(); err != nil {
		return UntrustedSummary{}, err
	}
	if summary.Version != request.Input.Version {
		return UntrustedSummary{}, fmt.Errorf("%w: summary schema version differs from request", contextstate.ErrInvalidDTO)
	}
	if summary.SourceRange != request.SourceRange {
		return UntrustedSummary{}, fmt.Errorf("%w: summary source range differs from request", contextstate.ErrInvalidDTO)
	}
	if err := validateSummaryValue(summary, request.SourceRange); err != nil {
		return UntrustedSummary{}, err
	}
	if err := validateSummaryPolicy(summary, request.RedactionPolicy); err != nil {
		return UntrustedSummary{}, err
	}
	encoded, err := contextstate.MarshalCanonical(summary)
	if err != nil {
		return UntrustedSummary{}, err
	}
	if summaryTokenEstimate(len(encoded)) > request.OutputLimit {
		return UntrustedSummary{}, fmt.Errorf("%w: summary output exceeds %d tokens", contextstate.ErrInvalidDTO, request.OutputLimit)
	}
	return UntrustedSummary{value: cloneSummary(summary), sealed: true}, nil
}

func validateSummaryEnvelopePolicy(envelope SummaryEnvelope, policy contextstate.RedactionPolicy) error {
	values := append([]string{envelope.Objective, envelope.State}, envelope.Decisions...)
	values = append(values, envelope.Evidence...)
	values = append(values, envelope.ChangedSurfaces...)
	values = append(values, envelope.OpenWork...)
	values = append(values, envelope.Risks...)
	for _, value := range values {
		if err := policy.Classify([]byte(value)); err != nil {
			return fmt.Errorf("%w: summary input rejected by redaction policy", contextstate.ErrInvalidDTO)
		}
	}
	return nil
}

func validateSummaryPolicy(summary Summary, policy contextstate.RedactionPolicy) error {
	values := append([]string{summary.Objective, summary.State}, summary.Decisions...)
	values = append(values, summary.Evidence...)
	values = append(values, summary.ChangedSurfaces...)
	values = append(values, summary.OpenWork...)
	values = append(values, summary.Risks...)
	for _, value := range values {
		if err := policy.Classify([]byte(value)); err != nil {
			return fmt.Errorf("%w: summary output rejected by redaction policy", contextstate.ErrInvalidDTO)
		}
	}
	return nil
}

func validateSummaryValue(summary Summary, sourceRange contextstate.SourceRange) error {
	if summary.Version == 0 {
		return fmt.Errorf("%w: summary version must be positive", contextstate.ErrInvalidDTO)
	}
	if err := summary.SourceRange.Validate(); err != nil {
		return err
	}
	if summary.SourceRange != sourceRange {
		return fmt.Errorf("%w: summary source range mismatch", contextstate.ErrInvalidDTO)
	}
	if err := validateSummaryText("objective", summary.Objective, true); err != nil {
		return err
	}
	if err := validateSummaryText("state", summary.State, true); err != nil {
		return err
	}
	for name, values := range map[string][]string{
		"decisions":        summary.Decisions,
		"evidence":         summary.Evidence,
		"changed_surfaces": summary.ChangedSurfaces,
		"open_work":        summary.OpenWork,
		"risks":            summary.Risks,
	} {
		if err := validateSummaryList(name, values); err != nil {
			return err
		}
	}
	encoded, err := contextstate.MarshalCanonical(summary)
	if err != nil {
		return err
	}
	if len(encoded) > contextstate.MaxSummaryMetadata {
		return fmt.Errorf("%w: summary metadata exceeds %d bytes", contextstate.ErrInvalidDTO, contextstate.MaxSummaryMetadata)
	}
	return nil
}

func summaryTokenEstimate(bytes int) int {
	if bytes == 0 {
		return 0
	}
	estimate := bytes / 4
	if estimate < 1 {
		return 1
	}
	return estimate
}

func validateSummaryText(field, value string, allowEmpty bool) error {
	if !utf8.ValidString(value) || len(value) > maxSummaryFieldBytes {
		return fmt.Errorf("%w: summary %s is invalid or oversized", contextstate.ErrInvalidDTO, field)
	}
	if !allowEmpty && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: summary %s is empty", contextstate.ErrInvalidDTO, field)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: summary %s contains control characters", contextstate.ErrInvalidDTO, field)
		}
	}
	return nil
}

func validateSummaryList(field string, values []string) error {
	if len(values) > maxSummaryItems {
		return fmt.Errorf("%w: summary %s has too many items", contextstate.ErrInvalidDTO, field)
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := validateSummaryText(field, value, false); err != nil {
			return err
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%w: summary %s contains duplicate items", contextstate.ErrInvalidDTO, field)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func cloneSummary(summary Summary) Summary {
	summary.Decisions = append([]string(nil), summary.Decisions...)
	summary.Evidence = append([]string(nil), summary.Evidence...)
	summary.ChangedSurfaces = append([]string(nil), summary.ChangedSurfaces...)
	summary.OpenWork = append([]string(nil), summary.OpenWork...)
	summary.Risks = append([]string(nil), summary.Risks...)
	return summary
}

type summaryMetadata struct {
	Version         uint32                   `json:"version"`
	SourceRange     contextstate.SourceRange `json:"source_range"`
	RedactionStatus string                   `json:"redaction_status"`
	ContentSHA256   string                   `json:"content_sha256"`
	Summary         *Summary                 `json:"summary,omitempty"`
}

// Metadata returns bounded persistence bytes. Without an explicitly
// configured redaction policy, only structural metadata and a digest survive;
// summary content remains ephemeral.
func (s UntrustedSummary) Metadata(redactionConfigured bool) ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	content, err := contextstate.MarshalCanonical(s.value)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(content)
	metadata := summaryMetadata{
		Version: s.value.Version, SourceRange: s.value.SourceRange,
		RedactionStatus: "structural-only", ContentSHA256: hex.EncodeToString(digest[:]),
	}
	if redactionConfigured {
		metadata.RedactionStatus = "host-redacted"
		value := cloneSummary(s.value)
		metadata.Summary = &value
	}
	encoded, err := contextstate.MarshalCanonical(metadata)
	if err != nil {
		return nil, err
	}
	if len(encoded) > contextstate.MaxSummaryMetadata {
		return nil, fmt.Errorf("%w: persisted summary metadata exceeds limit", contextstate.ErrInvalidDTO)
	}
	return encoded, nil
}
