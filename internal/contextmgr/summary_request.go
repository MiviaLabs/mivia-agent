package contextmgr

import (
	"reflect"
	"strings"
	"unicode"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// SummarySchemaVersion is the sealed summary envelope schema version minted by
// BuildSummaryRequest. ValidateSummary requires provider output to echo it.
const SummarySchemaVersion uint32 = 1

// SummaryBuildInput is the host-side accumulation BuildSummaryRequest converts
// into a validated SummaryRequest. Envelope lists are already bounded by the
// turn-state tracker or OmittedEvidence; the constructor re-validates every
// field through the same validators Summarizer.Summarize will apply.
type SummaryBuildInput struct {
	Version           uint32
	Objective         string
	State             string
	Decisions         []string
	Evidence          []string
	ChangedSurfaces   []string
	OpenWork          []string
	Risks             []string
	SourceRange       contextstate.SourceRange
	PolicyDigest      string
	Provider          string
	Model             string
	EndpointAllowlist []string
	RedactionPolicy   contextstate.RedactionPolicy
	Budget            int
	OutputLimit       int
}

// BuildSummaryRequest is the production constructor for a summary request: it
// seals the bounded envelope, fills the transport fields, and validates the
// whole request exactly as Summarizer.Summarize will re-validate it.
func BuildSummaryRequest(input SummaryBuildInput) (SummaryRequest, error) {
	envelope, err := NewSummaryEnvelope(input.Version, input.Objective, input.State, input.Decisions, input.Evidence, input.ChangedSurfaces, input.OpenWork, input.Risks, input.SourceRange, input.PolicyDigest)
	if err != nil {
		return SummaryRequest{}, err
	}
	request := SummaryRequest{
		Input:             envelope,
		Budget:            input.Budget,
		OutputLimit:       input.OutputLimit,
		SourceRange:       input.SourceRange,
		Provider:          input.Provider,
		Model:             input.Model,
		EndpointAllowlist: append([]string(nil), input.EndpointAllowlist...),
		RedactionPolicy:   cloneRedactionPolicy(input.RedactionPolicy),
	}
	if err := request.Validate(); err != nil {
		return SummaryRequest{}, err
	}
	return request, nil
}

func cloneRedactionPolicy(policy contextstate.RedactionPolicy) contextstate.RedactionPolicy {
	policy.Patterns = append([]string(nil), policy.Patterns...)
	policy.KeyNames = append([]string(nil), policy.KeyNames...)
	return policy
}

// OmittedEvidence derives content-free summary evidence from the difference
// between the pre-compaction history and the retained preparation. Each item
// records only the role, the tool name (for tool results), and the size bucket
// of one omitted message - never Content, Arguments, digests, or identifiers,
// mirroring elisionNotice. The result is capped at MaxSummaryItems items and
// byte-deterministic for identical inputs.
func OmittedEvidence(input, retained []provider.Message) []string {
	var evidence []string
	retainedIndex := 0
	for _, message := range input {
		if retainedIndex < len(retained) && reflect.DeepEqual(message, retained[retainedIndex]) {
			retainedIndex++
			continue
		}
		if item := omittedEvidenceItem(message); item != "" {
			evidence = append(evidence, item)
			if len(evidence) >= MaxSummaryItems {
				break
			}
		}
	}
	return evidence
}

// omittedEvidenceItem renders one content-free evidence item. The size bucket
// comes from the message body length; the tool name (a provider-supplied
// string) is sanitized so the item stays valid UTF-8, free of control
// characters, and well within MaxSummaryFieldBytes.
func omittedEvidenceItem(message provider.Message) string {
	if message.Role == provider.RoleTool {
		return "tool " + sanitizeEvidenceText(message.Name) + " result (~" + sizeBucketLabel(len(message.Content)) + ")"
	}
	return message.Role + " message (~" + sizeBucketLabel(len(message.Content)) + ")"
}

// sanitizeEvidenceText keeps evidence envelope-valid even when a
// provider-supplied tool name carries control characters or invalid UTF-8:
// control characters become spaces and invalid sequences are replaced.
func sanitizeEvidenceText(value string) string {
	value = strings.ToValidUTF8(value, "\uFFFD")
	var b strings.Builder
	for _, r := range value {
		if unicode.IsControl(r) {
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
