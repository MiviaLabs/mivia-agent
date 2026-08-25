package contextmgr

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// fakeSummaryCompleter records every provider.Request it receives and returns
// one canned result. It never touches the network.
type fakeSummaryCompleter struct {
	err         error
	response    provider.Response
	nilResponse bool
	calls       int
	requests    []provider.Request
}

func (f *fakeSummaryCompleter) Name() string { return "fake-summary" }

func (f *fakeSummaryCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	response, err := f.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return response.Content, nil
}

func (f *fakeSummaryCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	response, err := f.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return response.Content, nil
}

func (f *fakeSummaryCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	f.calls++
	f.requests = append(f.requests, req)
	if f.err != nil {
		return nil, f.err
	}
	if f.nilResponse {
		return nil, nil
	}
	response := f.response
	return &response, nil
}

// llmSummaryRequestFixture builds one valid request with every envelope list
// populated, so prompt assertions cover all fields.
func llmSummaryRequestFixture(t *testing.T) SummaryRequest {
	t.Helper()
	start := contextstate.SourceID{SessionID: "session-alpha", Sequence: 4}
	end := contextstate.SourceID{SessionID: "session-alpha", Sequence: 9}
	sourceRange, err := contextstate.NewSourceRange(start, end)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := NewSummaryEnvelope(
		SummarySchemaVersion,
		"Ship the rating feature",
		"Two files changed and the suite passes",
		[]string{"Store scores as integers only"},
		[]string{"user message (~1 KiB)"},
		[]string{"api/ratings.md"},
		[]string{"Add the pending migration"},
		[]string{"The cache key may collide"},
		sourceRange,
		strings.Repeat("d", 64),
	)
	if err != nil {
		t.Fatal(err)
	}
	return SummaryRequest{
		Input: envelope, Budget: 400, OutputLimit: 256, SourceRange: sourceRange,
		Provider: "provider", Model: "model-x",
		EndpointAllowlist: []string{"https://summary.invalid"},
		RedactionPolicy:   contextstate.RedactionPolicy{Configured: true, Patterns: []string{"never-match"}},
	}
}

func llmSummaryReplyFixture(request SummaryRequest) Summary {
	return Summary{
		Version:         request.Input.Version,
		Objective:       "Ship the rating feature",
		State:           "Two files changed and the suite passes",
		Decisions:       []string{"Store scores as integers only"},
		Evidence:        []string{"user message (~1 KiB)"},
		ChangedSurfaces: []string{"api/ratings.md"},
		OpenWork:        []string{"Add the pending migration"},
		Risks:           []string{"The cache key may collide"},
		SourceRange:     request.SourceRange,
	}
}

func llmSummaryReplyJSON(t *testing.T, summary Summary) string {
	t.Helper()
	data, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestLLMSummaryProviderHappyPath drives one valid reply end to end. It pins
// the request shape: the bound model, temperature zero, no tool fields, and
// an output cap equal to OutputLimit plus the bounded JSON headroom.
func TestLLMSummaryProviderHappyPath(t *testing.T) {
	request := llmSummaryRequestFixture(t)
	want := llmSummaryReplyFixture(request)
	completer := &fakeSummaryCompleter{response: provider.Response{Content: llmSummaryReplyJSON(t, want)}}
	adapter, err := NewLLMSummaryProvider(completer, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := adapter.Summarize(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("summary = %+v, want %+v", got, want)
	}
	if completer.calls != 1 {
		t.Fatalf("completer calls = %d, want 1", completer.calls)
	}
	sent := completer.requests[0]
	if sent.Model != "model-x" {
		t.Fatalf("request model = %q, want model-x", sent.Model)
	}
	if sent.Stream {
		t.Fatal("summary request must not stream")
	}
	if len(sent.Tools) != 0 || sent.ToolChoice != "" {
		t.Fatalf("summary request carries tool fields: tools=%d tool_choice=%q", len(sent.Tools), sent.ToolChoice)
	}
	if sent.Temperature == nil || *sent.Temperature != 0 {
		t.Fatalf("temperature = %v, want a pointer to 0", sent.Temperature)
	}
	// The wire cap is the OutputLimit itself, NOT OutputLimit+headroom. The
	// headroom now pads the ACCEPT bound instead: asking for more than
	// ValidateSummary would accept meant paying for a compliant reply and
	// then discarding it, which cost a real session its whole task context.
	if sent.MaxTokens == nil || *sent.MaxTokens != summaryWireCap(request.OutputLimit) {
		t.Fatalf("max tokens = %v, want %d", sent.MaxTokens, summaryWireCap(request.OutputLimit))
	}
	if summaryAcceptBound(request.OutputLimit) < *sent.MaxTokens {
		t.Fatalf("accept bound %d is below the wire cap %d", summaryAcceptBound(request.OutputLimit), *sent.MaxTokens)
	}
	if len(sent.Messages) != 2 ||
		sent.Messages[0].Role != provider.RoleSystem ||
		sent.Messages[1].Role != provider.RoleUser {
		t.Fatalf("messages = %+v, want one system and one user message", sent.Messages)
	}
}

// TestLLMSummaryProviderPromptCarriesEnvelope asserts the rendered prompt on
// substrings: every schema field name, every envelope value, the version to
// echo, and the source range values to echo. It also bans project and
// language terms, because the prompt must fit any conversation.
func TestLLMSummaryProviderPromptCarriesEnvelope(t *testing.T) {
	request := llmSummaryRequestFixture(t)
	completer := &fakeSummaryCompleter{response: provider.Response{Content: llmSummaryReplyJSON(t, llmSummaryReplyFixture(request))}}
	adapter, err := NewLLMSummaryProvider(completer, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Summarize(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	var prompt strings.Builder
	for _, message := range completer.requests[0].Messages {
		prompt.WriteString(message.Content)
		prompt.WriteString("\n")
	}
	text := prompt.String()
	required := []string{
		"objective", "state", "decisions", "evidence", "changed_surfaces", "open_work", "risks",
		"version", "source_range", "session_id", "sequence",
		"Ship the rating feature",
		"Two files changed and the suite passes",
		"Store scores as integers only",
		"user message (~1 KiB)",
		"api/ratings.md",
		"Add the pending migration",
		"The cache key may collide",
		"session-alpha",
		"version: " + strconv.FormatUint(uint64(request.Input.Version), 10),
		`"sequence":4`, `"sequence":9`,
	}
	for _, want := range required {
		if !strings.Contains(text, want) {
			t.Errorf("prompt omits %q", want)
		}
	}
	lower := strings.ToLower(text)
	for _, banned := range []string{" go ", "golang", "mivia", "cmd/mivia", "go test", "go build", "go.mod", "*.go", " git "} {
		if strings.Contains(lower, banned) {
			t.Errorf("prompt contains banned project or language term %q", banned)
		}
	}
}

// TestLLMSummaryProviderPromptRendersFocusOnlyWhenSet pins the /compact
// [focus instructions] wiring: a non-empty Focus must appear in the rendered
// prompt as an explicit line, and an empty Focus (the common, unbiased case)
// must not add a stray "focus:" line the model has nothing to read from.
func TestLLMSummaryProviderPromptRendersFocusOnlyWhenSet(t *testing.T) {
	unfocused := llmSummaryRequestFixture(t)
	if got := summaryUserPrompt(unfocused); strings.Contains(got, "focus:") {
		t.Fatalf("prompt with no focus contains a stray focus line: %q", got)
	}

	focused := llmSummaryRequestFixture(t)
	focused.Focus = "keep the auth discussion"
	got := summaryUserPrompt(focused)
	if !strings.Contains(got, "focus: keep the auth discussion") {
		t.Fatalf("prompt does not render the focus line: %q", got)
	}
}

// TestLLMSummaryProviderDecodesAndRejectsReplies is the decode table: fences
// are tolerated; malformed, empty, trailing, unknown, and mismatched replies
// fail. Every case makes exactly one completer call, because the adapter
// never retries.
func TestLLMSummaryProviderDecodesAndRejectsReplies(t *testing.T) {
	request := llmSummaryRequestFixture(t)
	valid := llmSummaryReplyJSON(t, llmSummaryReplyFixture(request))
	wrongVersion := llmSummaryReplyFixture(request)
	wrongVersion.Version = 99
	wrongRange := llmSummaryReplyFixture(request)
	wrongRange.SourceRange.End.Sequence = 10

	var fields map[string]any
	if err := json.Unmarshal([]byte(valid), &fields); err != nil {
		t.Fatal(err)
	}
	fields["note"] = "extra field"
	unknownField, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	delete(fields, "note")
	delete(fields, "version")
	delete(fields, "decisions")
	delete(fields, "evidence")
	delete(fields, "changed_surfaces")
	delete(fields, "open_work")
	delete(fields, "risks")
	missingVersion, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		content string
		wantErr bool
	}{
		{"plain json object", valid, false},
		{"json fenced with language", "```json\n" + valid + "\n```", false},
		{"json fenced without language", "```\n" + valid + "\n```", false},
		{"fenced json with padding", "```json\n  " + valid + "  \n```  ", false},
		{"empty content", "", true},
		{"whitespace only", "   \n\t  ", true},
		{"malformed json", `{"version":1,`, true},
		{"fence without json", "```\nnot json\n```", true},
		{"trailing garbage after object", valid + "\nextra text", true},
		{"second object after object", valid + valid, true},
		{"unknown field", string(unknownField), true},
		{"missing version echoes zero", string(missingVersion), true},
		{"wrong version echo", llmSummaryReplyJSON(t, wrongVersion), true},
		{"wrong source range echo", llmSummaryReplyJSON(t, wrongRange), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			completer := &fakeSummaryCompleter{response: provider.Response{Content: tc.content}}
			adapter, err := NewLLMSummaryProvider(completer, "")
			if err != nil {
				t.Fatal(err)
			}
			_, err = adapter.Summarize(context.Background(), request)
			if tc.wantErr && err == nil {
				t.Fatal("expected an error, got none")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if completer.calls != 1 {
				t.Fatalf("completer calls = %d, want 1", completer.calls)
			}
		})
	}
}

// TestLLMSummaryProviderRefusesNilCompleter pins the constructor guard and
// the in-call guard on the zero value, mirroring NewSummarizer.
func TestLLMSummaryProviderRefusesNilCompleter(t *testing.T) {
	if _, err := NewLLMSummaryProvider(nil, ""); !errors.Is(err, contextstate.ErrSummaryUnavailable) {
		t.Fatalf("nil completer error = %v, want ErrSummaryUnavailable", err)
	}
	var zero LLMSummaryProvider
	if _, err := zero.Summarize(context.Background(), llmSummaryRequestFixture(t)); !errors.Is(err, contextstate.ErrSummaryUnavailable) {
		t.Fatalf("zero-value provider error = %v, want ErrSummaryUnavailable", err)
	}
}

// TestLLMSummaryProviderReturnsTransportError pins the wrap of the completer
// error and the single-call contract on failure.
func TestLLMSummaryProviderReturnsTransportError(t *testing.T) {
	transportErr := errors.New("connection refused")
	completer := &fakeSummaryCompleter{err: transportErr}
	adapter, err := NewLLMSummaryProvider(completer, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.Summarize(context.Background(), llmSummaryRequestFixture(t))
	if !errors.Is(err, transportErr) {
		t.Fatalf("error = %v, want the wrapped transport error", err)
	}
	if completer.calls != 1 {
		t.Fatalf("completer calls = %d, want 1: the adapter never retries", completer.calls)
	}
}

// TestLLMSummaryProviderRejectsInvalidRequestBeforeCall pins that a request
// which fails SummaryRequest.Validate never reaches the completer.
func TestLLMSummaryProviderRejectsInvalidRequestBeforeCall(t *testing.T) {
	request := llmSummaryRequestFixture(t)
	request.Budget = 0
	completer := &fakeSummaryCompleter{response: provider.Response{Content: "{}"}}
	adapter, err := NewLLMSummaryProvider(completer, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Summarize(context.Background(), request); !errors.Is(err, contextstate.ErrInvalidDTO) {
		t.Fatalf("invalid request error = %v, want ErrInvalidDTO", err)
	}
	if completer.calls != 0 {
		t.Fatalf("completer calls = %d, want 0", completer.calls)
	}
}

// TestLLMSummaryProviderIgnoresBlankedRedactionPolicy pins the transport
// contract with Summarizer.Summarize: the host blanks RedactionPolicy before
// the provider call, so the adapter must not depend on it.
func TestLLMSummaryProviderIgnoresBlankedRedactionPolicy(t *testing.T) {
	request := llmSummaryRequestFixture(t)
	request.RedactionPolicy = contextstate.RedactionPolicy{}
	want := llmSummaryReplyFixture(request)
	completer := &fakeSummaryCompleter{response: provider.Response{Content: llmSummaryReplyJSON(t, want)}}
	adapter, err := NewLLMSummaryProvider(completer, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := adapter.Summarize(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("summary = %+v, want %+v", got, want)
	}
	if completer.calls != 1 {
		t.Fatalf("completer calls = %d, want 1", completer.calls)
	}
}

// TestLLMSummaryProviderRefusesMissingResponse pins the nil-response guard:
// a broken completer that returns no error and no response still fails the
// adapter instead of panicking.
func TestLLMSummaryProviderRefusesMissingResponse(t *testing.T) {
	completer := &fakeSummaryCompleter{nilResponse: true}
	adapter, err := NewLLMSummaryProvider(completer, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Summarize(context.Background(), llmSummaryRequestFixture(t)); !errors.Is(err, contextstate.ErrInvalidDTO) {
		t.Fatalf("missing response error = %v, want ErrInvalidDTO", err)
	}
	if completer.calls != 1 {
		t.Fatalf("completer calls = %d, want 1", completer.calls)
	}
}
