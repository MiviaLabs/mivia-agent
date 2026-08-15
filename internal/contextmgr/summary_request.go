package contextmgr

import (
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

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
	// SourceExcerpts carries bounded quotes of the dropped messages for the
	// summarize REQUEST. BuildSummaryRequest sanitizes them and drops every
	// item the redaction policy flags; the envelope never sees them.
	SourceExcerpts []SourceExcerpt
	// Focus is an optional caller-supplied bias string telling the
	// summarizer what to prioritize (e.g. `/compact <focus instructions>`).
	// Empty is the default, unbiased behavior. See SummaryRequest.Focus.
	Focus string
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
		SourceExcerpts:    filterSourceExcerpts(input.SourceExcerpts, input.RedactionPolicy),
		Focus:             truncateExcerptText(input.Focus),
	}
	if err := request.Validate(); err != nil {
		return SummaryRequest{}, err
	}
	return request, nil
}

// filterSourceExcerpts sanitizes request excerpts and drops every item the
// redaction policy flags: the model never re-reads bytes the workspace
// policy refuses, and one flagged excerpt never fails the whole summary.
func filterSourceExcerpts(excerpts []SourceExcerpt, policy contextstate.RedactionPolicy) []SourceExcerpt {
	out := make([]SourceExcerpt, 0, len(excerpts))
	for _, excerpt := range excerpts {
		excerpt.Role = strings.TrimSpace(excerpt.Role)
		excerpt.Name = truncateExcerptTextTo(excerpt.Name, contextstate.MaxIdentifierBytes)
		excerpt.Text = truncateExcerptText(excerpt.Text)
		if excerpt.Text == "" {
			continue
		}
		if policy.Classify([]byte(excerpt.Text)) != nil {
			continue
		}
		out = append(out, excerpt)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// MaxSummaryExcerptTotalBytes caps the whole source-excerpt section of one
// summary request (about 4k tokens): large enough to carry a real dropped
// segment, small enough that the summarize call stays cheap on every model.
const MaxSummaryExcerptTotalBytes = 16 * 1024

// SourceExcerpt is one bounded, content-bearing quote of a message the
// compaction dropped. It rides the summary REQUEST only - never the sealed
// envelope, the durable metadata, or the injected message - so the persisted
// summary contract stays unchanged while the model reads real content.
type SourceExcerpt struct {
	Role string
	Name string
	Text string
}

// SourceExcerpts derives bounded excerpts of the dropped messages, newest
// first, so the summarizer reads the real conversation content instead of
// size labels. The FIRST dropped user message keeps a guaranteed opening
// slot: it frames the task of the summarized segment. Tool-call arguments
// and assistant reasoning never ride the request; tool results do. Item,
// per-field, and total-byte bounds cap the section.
func SourceExcerpts(input, retained []provider.Message) []SourceExcerpt {
	omitted := omittedMessageIndices(input, retained)
	if len(omitted) == 0 {
		return nil
	}
	firstUser := -1
	for _, index := range omitted {
		if input[index].Role == provider.RoleUser {
			firstUser = index
			break
		}
	}
	total := 0
	included := map[int]struct{}{}
	var out []SourceExcerpt
	add := func(index int) bool {
		if len(out) >= MaxSummaryItems {
			return false
		}
		excerpt, ok := excerptOf(input[index])
		if !ok {
			return true // nothing to add, but the walk continues
		}
		if total+len(excerpt.Text) > MaxSummaryExcerptTotalBytes {
			return false
		}
		total += len(excerpt.Text)
		included[index] = struct{}{}
		out = append(out, excerpt)
		return true
	}
	if firstUser != -1 {
		add(firstUser)
	}
	// The remaining dropped messages fill newest first: the tail carries the
	// state closest to what the model still holds.
	for i := len(omitted) - 1; i >= 0; i-- {
		index := omitted[i]
		if _, done := included[index]; done {
			continue
		}
		if !add(index) {
			break
		}
	}
	return out
}

// excerptOf renders one message as a bounded excerpt. Messages with no
// content-bearing text (an assistant tool-call carrier, an empty turn) are
// skipped.
func excerptOf(message provider.Message) (SourceExcerpt, bool) {
	text := truncateExcerptText(message.Content)
	if text == "" {
		return SourceExcerpt{}, false
	}
	excerpt := SourceExcerpt{Role: message.Role, Text: text}
	if message.Role == provider.RoleTool {
		excerpt.Name = truncateExcerptTextTo(message.Name, contextstate.MaxIdentifierBytes)
	}
	return excerpt, true
}

// truncateExcerptText sanitizes excerpt text (valid UTF-8, control
// characters become spaces) and truncates it to MaxSummaryFieldBytes on a
// rune boundary.
func truncateExcerptText(value string) string {
	return truncateExcerptTextTo(value, MaxSummaryFieldBytes)
}

// truncateExcerptTextTo is truncateExcerptText with a caller-chosen byte
// bound. excerpt.Name must use contextstate.MaxIdentifierBytes here, not
// MaxSummaryFieldBytes: SummaryRequest.Validate rejects any excerpt Name
// over MaxIdentifierBytes (128B), so truncating a tool name to the larger
// 2048B field bound let names in the 129-2048B range pass truncation only
// to fail validation afterward - silently discarding the whole summary
// (applyCompactSummary swallows that error) instead of just the name.
func truncateExcerptTextTo(value string, maxBytes int) string {
	value = sanitizeEvidenceText(value)
	if len(value) <= maxBytes {
		return strings.TrimSpace(value)
	}
	value = value[:maxBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return strings.TrimSpace(value)
}

func cloneSummaryExcerpts(excerpts []SourceExcerpt) []SourceExcerpt {
	if excerpts == nil {
		return nil
	}
	out := make([]SourceExcerpt, len(excerpts))
	copy(out, excerpts)
	return out
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
	for _, index := range omittedMessageIndices(input, retained) {
		if item := omittedEvidenceItem(input[index]); item != "" {
			evidence = append(evidence, item)
			if len(evidence) >= MaxSummaryItems {
				break
			}
		}
	}
	return evidence
}

// elisionNoticePrefix opens every tool-result elision notice the planner
// mints (see elisionNoticeWithRef).
const elisionNoticePrefix = "[context elided prior tool result;"

// omittedMessageIndices returns the input positions the retained
// subsequence does not cover, in order. The content-free evidence diff and
// the content-bearing excerpt diff share this one walk.
func omittedMessageIndices(input, retained []provider.Message) []int {
	var omitted []int
	retainedIndex := 0
	for index, message := range input {
		if retainedIndex < len(retained) && retainedCorresponds(message, retained[retainedIndex]) {
			retainedIndex++
			continue
		}
		omitted = append(omitted, index)
	}
	return omitted
}

// retainedCorresponds reports whether the retained message is the input
// message's surviving copy. DeepEqual covers the untouched case; the second
// tier covers planner elision, which rewrites the body (a tool-result notice
// or a reasoning marker) without touching structural identity. Without the
// second tier the walk stalls at the first rewritten message and classifies
// every later message - including the live objective - as dropped.
func retainedCorresponds(input, retained provider.Message) bool {
	if reflect.DeepEqual(input, retained) {
		return true
	}
	if input.Role != retained.Role || input.Name != retained.Name ||
		input.ToolCallID != retained.ToolCallID || !reflect.DeepEqual(input.ToolCalls, retained.ToolCalls) {
		return false
	}
	return strings.HasPrefix(retained.Content, elisionNoticePrefix) ||
		retained.ReasoningContent == reasoningElisionMarker
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
