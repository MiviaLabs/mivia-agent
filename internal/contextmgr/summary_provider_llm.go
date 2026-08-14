package contextmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// summaryOutputHeadroomTokens pads the wire output cap over OutputLimit.
// ValidateSummary measures the summary bytes after the call and enforces
// OutputLimit itself. JSON field names, brackets, and one code fence add
// model tokens but no summary bytes. A small fixed pad stops a bounded
// summary from truncation at the transport. The pad never raises the bound.
const summaryOutputHeadroomTokens = 256

// summaryReplySkeleton is the literal reply schema shown to the model. The
// field names are the wire contract: the model must echo them and add none.
const summaryReplySkeleton = `{
  "version": <the version integer given below>,
  "objective": "<one short sentence>",
  "state": "<one short sentence>",
  "decisions": ["<decision>"],
  "evidence": ["<evidence item>"],
  "changed_surfaces": ["<changed surface>"],
  "open_work": ["<open item>"],
  "risks": ["<risk>"],
  "source_range": {"start": {"session_id": "<id>", "sequence": <integer>}, "end": {"session_id": "<id>", "sequence": <integer>}}
}`

// LLMSummaryProvider adapts one provider.Completer to the SummaryProvider
// contract. It renders a host-authored prompt from the sealed envelope, calls
// the completer once, and decodes one strict JSON reply. It never logs and
// never retries. Any error lets the caller degrade to structural-only
// compaction.
type LLMSummaryProvider struct {
	completer provider.Completer
}

// The assertion pins the contract from contracts.go. No production wiring
// binds this type yet, so the compiler is the only guard against drift.
var _ SummaryProvider = LLMSummaryProvider{}

// NewLLMSummaryProvider binds one completer. A nil completer is refused the
// same way NewSummarizer refuses a nil provider.
func NewLLMSummaryProvider(completer provider.Completer) (LLMSummaryProvider, error) {
	if completer == nil {
		return LLMSummaryProvider{}, fmt.Errorf("%w: summary completer is missing", contextstate.ErrSummaryUnavailable)
	}
	return LLMSummaryProvider{completer: completer}, nil
}

// Summarize runs one non-stream completion and decodes the reply. It checks
// the two mandated echoes before it returns. ValidateSummary in
// Summarizer.Summarize owns the full field, redaction, and size validation.
// The adapter does not read request.RedactionPolicy: the host blanks it
// before the call, because classifier configuration is host policy.
func (p LLMSummaryProvider) Summarize(ctx context.Context, request SummaryRequest) (Summary, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if p.completer == nil {
		return Summary{}, fmt.Errorf("%w: summary completer is missing", contextstate.ErrSummaryUnavailable)
	}
	if err := request.Validate(); err != nil {
		return Summary{}, err
	}
	temperature := 0.0
	outputCap := request.OutputLimit + summaryOutputHeadroomTokens
	reply, err := p.completer.ChatTurn(ctx, provider.Request{
		Model: request.Model, Messages: summaryMessages(request),
		Temperature: &temperature, MaxTokens: &outputCap, Stream: false,
	})
	if err != nil {
		return Summary{}, fmt.Errorf("summary completer call: %w", err)
	}
	if reply == nil {
		return Summary{}, fmt.Errorf("%w: summary completer returned no response", contextstate.ErrInvalidDTO)
	}
	summary, err := decodeSummaryReply(reply.Content)
	if err != nil {
		return Summary{}, err
	}
	if summary.Version != request.Input.Version {
		return Summary{}, fmt.Errorf("%w: summary reply version %d does not echo %d", contextstate.ErrInvalidDTO, summary.Version, request.Input.Version)
	}
	if summary.SourceRange != request.SourceRange {
		return Summary{}, fmt.Errorf("%w: summary reply source range does not echo the request source range", contextstate.ErrInvalidDTO)
	}
	return summary, nil
}

// summaryMessages builds the two-message prompt: a system message with the
// task and the schema, and a user message with the envelope values.
func summaryMessages(request SummaryRequest) []provider.Message {
	return []provider.Message{
		{Role: provider.RoleSystem, Content: summarySystemPrompt()},
		{Role: provider.RoleUser, Content: summaryUserPrompt(request)},
	}
}

// summarySystemPrompt states the task and the reply contract. The text stays
// project- and language-generic: it summarizes any conversation.
func summarySystemPrompt() string {
	var b strings.Builder
	b.WriteString("You summarize an earlier part of a conversation. A later assistant uses your summary as its only record of that part.\n\n")
	b.WriteString("Read the input data in the user message. Keep the facts a later turn needs. Drop the rest.\n\n")
	b.WriteString("The source_excerpts section holds the real content of the messages compaction dropped. Build your summary from it; the other fields are host-side framing.\n\n")
	b.WriteString("Reply with one JSON object and nothing else. Do not add prose before or after the object. Do not wrap it in a code fence.\n\n")
	b.WriteString("Use exactly this schema:\n")
	b.WriteString(summaryReplySkeleton)
	b.WriteString("\n\nRules:\n")
	b.WriteString("- Copy the given version value into version. Do not change it.\n")
	b.WriteString("- Copy the given source_range value into source_range. Do not change it.\n")
	b.WriteString("- Write objective and state as single short sentences.\n")
	b.WriteString("- Use the input list items that help a later turn. Drop the rest.\n")
	b.WriteString("- Use an empty array for a list with no content.\n")
	b.WriteString("- Use plain UTF-8 text. Do not use control characters.\n")
	return b.String()
}

// summaryUserPrompt renders the sealed envelope values and the two echo
// mandates. Envelope values are untrusted conversation content. The strict
// decode and the downstream ValidateSummary bound what they can do.
func summaryUserPrompt(request SummaryRequest) string {
	sourceRange, err := json.Marshal(request.SourceRange)
	if err != nil {
		// The struct holds plain strings and integers, so marshal cannot
		// fail today. The fallback keeps the builder total if the type
		// grows; the echo self-check rejects the wrong value.
		sourceRange = []byte("{}")
	}
	var b strings.Builder
	b.WriteString("Input data from the conversation to summarize:\n\n")
	b.WriteString("objective: " + request.Input.Objective + "\n")
	b.WriteString("state: " + request.Input.State + "\n")
	writeSummaryList(&b, "decisions", request.Input.Decisions)
	writeSummaryList(&b, "evidence", request.Input.Evidence)
	writeSummaryList(&b, "changed_surfaces", request.Input.ChangedSurfaces)
	writeSummaryList(&b, "open_work", request.Input.OpenWork)
	writeSummaryList(&b, "risks", request.Input.Risks)
	writeSourceExcerpts(&b, request.SourceExcerpts)
	b.WriteString("\nEcho these values without any change:\n")
	b.WriteString("version: " + strconv.FormatUint(uint64(request.Input.Version), 10) + "\n")
	b.WriteString("source_range: " + string(sourceRange) + "\n")
	b.WriteString("\nWrite the summary JSON now.\n")
	return b.String()
}

// writeSummaryList renders one envelope list. An empty list prints (none), so
// the model still sees the field and can answer an empty array.
func writeSummaryList(b *strings.Builder, name string, values []string) {
	b.WriteString(name + ":\n")
	if len(values) == 0 {
		b.WriteString("  (none)\n")
		return
	}
	for _, value := range values {
		b.WriteString("  - " + value + "\n")
	}
}

// writeSourceExcerpts renders the dropped-message quotes. The first line is
// always the segment's first user message; the rest are newest first.
func writeSourceExcerpts(b *strings.Builder, excerpts []SourceExcerpt) {
	b.WriteString("source_excerpts (real content of the dropped messages; first line is the segment's opening user message, the rest newest first):\n")
	if len(excerpts) == 0 {
		b.WriteString("  (none)\n")
		return
	}
	for _, excerpt := range excerpts {
		label := "[" + excerpt.Role + "]"
		if excerpt.Name != "" {
			label = "[tool " + excerpt.Name + "]"
		}
		b.WriteString("  - " + label + " " + excerpt.Text + "\n")
	}
}

// decodeSummaryReply decodes one JSON object from the reply text. It accepts
// one markdown code fence around the object. It rejects empty text, malformed
// JSON, unknown fields, and any trailing bytes.
func decodeSummaryReply(content string) (Summary, error) {
	payload := stripCodeFence(content)
	if payload == "" {
		return Summary{}, fmt.Errorf("%w: summary reply is empty", contextstate.ErrInvalidDTO)
	}
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.DisallowUnknownFields()
	var summary Summary
	if err := decoder.Decode(&summary); err != nil {
		return Summary{}, fmt.Errorf("%w: summary reply is not valid summary JSON: %v", contextstate.ErrInvalidDTO, err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return Summary{}, fmt.Errorf("%w: summary reply has trailing content", contextstate.ErrInvalidDTO)
	}
	return summary, nil
}

// stripCodeFence removes at most one markdown code fence around the JSON
// object. The opening fence line may name a language. A closing fence may
// follow the object. A JSON object never starts with a backtick, so a fenced
// reply never loses JSON bytes.
func stripCodeFence(content string) string {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}
	newline := strings.IndexByte(trimmed, '\n')
	if newline < 0 {
		return trimmed
	}
	trimmed = strings.TrimSpace(trimmed[newline+1:])
	trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, "```"))
	return trimmed
}
