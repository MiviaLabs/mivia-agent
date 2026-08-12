package contextmgr

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func omittedEvidenceFixture() ([]provider.Message, []provider.Message) {
	call := plannerToolCall("call-ev", "read_file", `{"path":"secret-arguments.json"}`)
	input := []provider.Message{
		{Role: provider.RoleSystem, Content: "system-secret-body"},
		{Role: provider.RoleUser, Content: "old objective secret-body"},
		{Role: provider.RoleAssistant, Content: "old answer secret-body"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call}},
		{Role: provider.RoleTool, ToolCallID: call.ID, Name: call.Function.Name, Content: strings.Repeat("tool-secret-body", 600)},
		{Role: provider.RoleUser, Content: "current objective"},
	}
	// Retained: system + latest user objective only (a structural compaction
	// keeps those mandatory and drops the rest).
	retained := []provider.Message{
		{Role: provider.RoleSystem, Content: "system-secret-body"},
		{Role: provider.RoleUser, Content: "current objective"},
	}
	return input, retained
}

// TestOmittedEvidenceIsContentFreeAndDeterministic drives the pure diff for
// both an elided history (some messages omitted) and a non-elided one (input
// equals retained).
func TestOmittedEvidenceIsContentFreeAndDeterministic(t *testing.T) {
	input, retained := omittedEvidenceFixture()
	evidence := OmittedEvidence(input, retained)
	if len(evidence) != 4 {
		t.Fatalf("evidence items=%d, want 4 (%v)", len(evidence), evidence)
	}
	for _, item := range evidence {
		if len(item) > MaxSummaryFieldBytes || !utf8.ValidString(item) {
			t.Fatalf("evidence item is oversized or invalid UTF-8: %q", item)
		}
		for _, r := range item {
			if unicode.IsControl(r) {
				t.Fatalf("evidence item contains a control character: %q", item)
			}
		}
		for _, message := range input {
			if message.Content != "" && strings.Contains(item, message.Content) {
				t.Fatalf("evidence item leaked message content: %q in %q", message.Content, item)
			}
			for _, call := range message.ToolCalls {
				if call.Function.Arguments != "" && strings.Contains(item, call.Function.Arguments) {
					t.Fatalf("evidence item leaked tool arguments: %q in %q", call.Function.Arguments, item)
				}
			}
		}
	}
	if !reflect.DeepEqual(evidence, OmittedEvidence(input, retained)) {
		t.Fatal("OmittedEvidence is not byte-deterministic for identical input")
	}

	// Non-elided diff: input equals retained, so nothing is omitted.
	if got := OmittedEvidence(input, input); len(got) != 0 {
		t.Fatalf("non-elided diff reported evidence: %v", got)
	}
	// Empty retained: every message is omitted, capped at 32.
	capped := OmittedEvidence(input, nil)
	if len(capped) > MaxSummaryItems {
		t.Fatalf("evidence exceeded the item cap: %d", len(capped))
	}
}

// TestOmittedEvidenceToolNameSanitized pins that a provider-supplied tool
// name carrying control characters or invalid UTF-8 stays envelope-valid.
func TestOmittedEvidenceToolNameSanitized(t *testing.T) {
	call := plannerToolCall("call-ctl", "read_file", `{}`)
	input := []provider.Message{
		{Role: provider.RoleUser, Content: "objective"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call}},
		{Role: provider.RoleTool, ToolCallID: call.ID, Name: "bad\x01name\xff", Content: "body"},
	}
	evidence := OmittedEvidence(input, []provider.Message{{Role: provider.RoleUser, Content: "objective"}})
	if len(evidence) != 2 {
		t.Fatalf("evidence items=%d, want 2 (%v)", len(evidence), evidence)
	}
	for _, item := range evidence {
		if !utf8.ValidString(item) {
			t.Fatalf("sanitized item is not valid UTF-8: %q", item)
		}
		for _, r := range item {
			if unicode.IsControl(r) {
				t.Fatalf("sanitized item contains a control character: %q", item)
			}
		}
	}
}

func summaryBuildInputFixture() SummaryBuildInput {
	source := contextstate.SourceID{SessionID: "summary-session", Sequence: 1}
	rangeValue, _ := contextstate.NewSourceRange(source, source)
	return SummaryBuildInput{
		Version: SummarySchemaVersion, Objective: "objective", State: "state",
		Evidence:    []string{"user message (~1 KiB)"},
		SourceRange: rangeValue, PolicyDigest: strings.Repeat("a", 64),
		Provider: "provider", Model: "model",
		EndpointAllowlist: []string{"https://summary.invalid"},
		RedactionPolicy:   contextstate.RedactionPolicy{Configured: true, Patterns: []string{"never-match"}},
		Budget:            100, OutputLimit: 128,
	}
}

// TestBuildSummaryRequestSealsAndValidates drives the production constructor
// through a success case: the envelope is sealed, provider/model/transport
// fields are filled, and the whole request validates.
func TestBuildSummaryRequestSealsAndValidates(t *testing.T) {
	input := summaryBuildInputFixture()
	request, err := BuildSummaryRequest(input)
	if err != nil {
		t.Fatal(err)
	}
	if request.Input.Objective != "objective" || request.Input.State != "state" {
		t.Fatalf("envelope fields not copied: %+v", request.Input)
	}
	if !request.Input.sealed {
		t.Fatal("envelope is not host-sealed")
	}
	if request.Provider != "provider" || request.Model != "model" || request.Budget != 100 || request.OutputLimit != 128 {
		t.Fatalf("transport fields not filled: %+v", request)
	}
	if request.Input.SourceRange != request.SourceRange || request.Input.SourceRange != input.SourceRange {
		t.Fatalf("source range mismatch: %+v vs %+v", request.Input.SourceRange, request.SourceRange)
	}
	if !reflect.DeepEqual(request.EndpointAllowlist, input.EndpointAllowlist) {
		t.Fatalf("endpoint allowlist not copied: %v", request.EndpointAllowlist)
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("constructed request rejected: %v", err)
	}
}

// TestBuildSummaryRequestNegativeCases drives every rejection the constructor
// must surface before any provider call.
func TestBuildSummaryRequestNegativeCases(t *testing.T) {
	base := summaryBuildInputFixture()
	cases := []struct {
		name  string
		mut   func(*SummaryBuildInput)
		check func(error) bool
	}{
		{"bad policy digest", func(in *SummaryBuildInput) { in.PolicyDigest = "short" }, func(err error) bool { return errors.Is(err, contextstate.ErrInvalidDTO) }},
		{"zero source range", func(in *SummaryBuildInput) { in.SourceRange = contextstate.SourceRange{} }, func(err error) bool { return errors.Is(err, contextstate.ErrInvalidDTO) }},
		{"inverted source range", func(in *SummaryBuildInput) {
			in.SourceRange.Start.Sequence = in.SourceRange.End.Sequence + 1
		}, func(err error) bool { return errors.Is(err, contextstate.ErrInvalidDTO) }},
		{"empty provider", func(in *SummaryBuildInput) { in.Provider = " " }, func(err error) bool { return errors.Is(err, contextstate.ErrInvalidDTO) }},
		{"empty model", func(in *SummaryBuildInput) { in.Model = "" }, func(err error) bool { return errors.Is(err, contextstate.ErrInvalidDTO) }},
		{"oversized provider", func(in *SummaryBuildInput) { in.Provider = strings.Repeat("p", contextstate.MaxIdentifierBytes+1) }, func(err error) bool { return errors.Is(err, contextstate.ErrInvalidDTO) }},
		{"oversized model", func(in *SummaryBuildInput) { in.Model = strings.Repeat("m", contextstate.MaxIdentifierBytes+1) }, func(err error) bool { return errors.Is(err, contextstate.ErrInvalidDTO) }},
		{"blank endpoint", func(in *SummaryBuildInput) { in.EndpointAllowlist = []string{" "} }, func(err error) bool { return errors.Is(err, contextstate.ErrInvalidDTO) }},
		{"duplicate endpoint", func(in *SummaryBuildInput) {
			in.EndpointAllowlist = []string{"https://summary.invalid", "https://summary.invalid"}
		}, func(err error) bool { return errors.Is(err, contextstate.ErrInvalidDTO) }},
		{"oversized envelope field", func(in *SummaryBuildInput) { in.State = strings.Repeat("x", MaxSummaryFieldBytes+1) }, func(err error) bool { return errors.Is(err, contextstate.ErrInvalidDTO) }},
		{"duplicate envelope list item", func(in *SummaryBuildInput) { in.Decisions = []string{"same", "same"} }, func(err error) bool { return errors.Is(err, contextstate.ErrInvalidDTO) }},
		{"control char envelope field", func(in *SummaryBuildInput) { in.Objective = "obj\x01ective" }, func(err error) bool { return errors.Is(err, contextstate.ErrInvalidDTO) }},
		{"zero budget", func(in *SummaryBuildInput) { in.Budget = 0 }, func(err error) bool { return errors.Is(err, contextstate.ErrInvalidDTO) }},
		{"zero output limit", func(in *SummaryBuildInput) { in.OutputLimit = 0 }, func(err error) bool { return errors.Is(err, contextstate.ErrInvalidDTO) }},
		{"oversized output limit", func(in *SummaryBuildInput) { in.OutputLimit = 4096 }, func(err error) bool { return errors.Is(err, contextstate.ErrInvalidDTO) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := base
			tc.mut(&input)
			if _, err := BuildSummaryRequest(input); !tc.check(err) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

// TestBuildSummaryRequestRedactionClassifiedField pins that an envelope field
// rejected by the configured redaction classifier fails construction, so a
// secret never reaches a provider through BuildSummaryRequest.
func TestBuildSummaryRequestRedactionClassifiedField(t *testing.T) {
	input := summaryBuildInputFixture()
	input.RedactionPolicy = contextstate.RedactionPolicy{
		Configured: true,
		Classifier: func(data []byte) error {
			if strings.Contains(string(data), "sensitive-sentinel") {
				return contextstate.ErrInvalidDTO
			}
			return nil
		},
	}
	input.State = "contains sensitive-sentinel"
	if _, err := BuildSummaryRequest(input); !errors.Is(err, contextstate.ErrInvalidDTO) {
		t.Fatalf("redaction-classified field error = %v, want ErrInvalidDTO", err)
	}
}
