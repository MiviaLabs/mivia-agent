package contextmgr

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func oneToolCall() []provider.ToolCall {
	var call provider.ToolCall
	call.Function.Name = "run_command"
	call.Function.Arguments = `{"command":"deploy --token=xyz"}`
	return []provider.ToolCall{call}
}

func summaryExcerptRange(t *testing.T) contextstate.SourceRange {
	t.Helper()
	sourceRange, err := contextstate.NewSourceRange(
		contextstate.SourceID{SessionID: "sess-excerpt", Sequence: 1},
		contextstate.SourceID{SessionID: "sess-excerpt", Sequence: 2})
	if err != nil {
		t.Fatal(err)
	}
	return sourceRange
}

func excerptFixtureMessages() ([]provider.Message, []provider.Message) {
	input := []provider.Message{
		{Role: provider.RoleUser, Content: "refactor the auth module to use tokens"},
		{Role: provider.RoleAssistant, Content: "I moved jwt.go into internal/auth and added rotation."},
		{Role: provider.RoleTool, Name: "grep", Content: "internal/auth/jwt.go:42: func rotate()"},
		{Role: provider.RoleUser, Content: "latest question"},
	}
	retained := []provider.Message{input[3]}
	return input, retained
}

// TestSourceExcerptsCarriesDroppedContent pins the core contract: the dropped
// messages' real content rides the request, newest first, with the first
// dropped user message guaranteed the opening slot.
func TestSourceExcerptsCarriesDroppedContent(t *testing.T) {
	input, retained := excerptFixtureMessages()
	excerpts := SourceExcerpts(input, retained)
	if len(excerpts) != 3 {
		t.Fatalf("excerpts = %d items, want the three dropped messages", len(excerpts))
	}
	if excerpts[0].Role != "user" || excerpts[0].Text != "refactor the auth module to use tokens" {
		t.Fatalf("slot 0 = %+v, want the first dropped user message", excerpts[0])
	}
	if excerpts[1].Role != "tool" || excerpts[1].Name != "grep" || excerpts[1].Text != "internal/auth/jwt.go:42: func rotate()" {
		t.Fatalf("newest-first tool excerpt = %+v", excerpts[1])
	}
	if excerpts[2].Role != "assistant" || excerpts[2].Text != "I moved jwt.go into internal/auth and added rotation." {
		t.Fatalf("assistant excerpt = %+v", excerpts[2])
	}
}

// TestSourceExcerptsBounds pins the size contract: at most MaxSummaryItems
// items, each at most MaxSummaryFieldBytes, and the whole section at most
// MaxSummaryExcerptTotalBytes even when every dropped message is maximal.
func TestSourceExcerptsBounds(t *testing.T) {
	var input []provider.Message
	for i := 0; i < 48; i++ {
		input = append(input, provider.Message{
			Role:    provider.RoleAssistant,
			Content: strings.Repeat("a", MaxSummaryFieldBytes+512),
		})
	}
	excerpts := SourceExcerpts(input, nil)
	if len(excerpts) > MaxSummaryItems {
		t.Fatalf("excerpt count = %d, want at most %d", len(excerpts), MaxSummaryItems)
	}
	total := 0
	for _, excerpt := range excerpts {
		if len(excerpt.Text) > MaxSummaryFieldBytes {
			t.Fatalf("excerpt of %d bytes exceeds the %d field bound", len(excerpt.Text), MaxSummaryFieldBytes)
		}
		total += len(excerpt.Text)
	}
	if total > MaxSummaryExcerptTotalBytes {
		t.Fatalf("excerpt total = %d bytes, want at most %d", total, MaxSummaryExcerptTotalBytes)
	}
	// Newest first: after the guaranteed first-user slot (absent here), the
	// leading excerpt must come from the last dropped message.
	if !strings.HasPrefix(excerpts[0].Text, strings.Repeat("a", 64)) || len(excerpts) < 2 {
		t.Fatal("excerpts are not newest first")
	}
}

// TestSourceExcerptsExcludesHiddenContent pins the exclusions: tool-call
// arguments and assistant reasoning never ride the request; only result and
// conversation text does.
func TestSourceExcerptsExcludesHiddenContent(t *testing.T) {
	input := []provider.Message{
		{Role: provider.RoleUser, Content: "run the deploy"},
		{Role: provider.RoleAssistant, Content: "", ReasoningContent: "secret plan", ToolCalls: oneToolCall()},
		{Role: provider.RoleTool, Name: "run_command", Content: "deployed"},
	}
	excerpts := SourceExcerpts(input, nil)
	for _, excerpt := range excerpts {
		if strings.Contains(excerpt.Text, "secret plan") || strings.Contains(excerpt.Text, "--token") {
			t.Fatalf("hidden content rode the request: %+v", excerpt)
		}
	}
	if len(excerpts) != 2 {
		t.Fatalf("excerpts = %+v, want user plus tool result only", excerpts)
	}
}

// TestSourceExcerptsTruncatesOnRuneBoundary pins multibyte safety: a cut
// never splits a rune.
func TestSourceExcerptsTruncatesOnRuneBoundary(t *testing.T) {
	input := []provider.Message{{Role: provider.RoleUser, Content: strings.Repeat("日", MaxSummaryFieldBytes)}}
	excerpts := SourceExcerpts(input, nil)
	if len(excerpts) != 1 {
		t.Fatalf("excerpts = %d, want one", len(excerpts))
	}
	if len(excerpts[0].Text) > MaxSummaryFieldBytes || !utf8.ValidString(excerpts[0].Text) {
		t.Fatalf("truncated text is %d bytes, valid=%v", len(excerpts[0].Text), utf8.ValidString(excerpts[0].Text))
	}
}

// TestBuildSummaryRequestFiltersFlaggedExcerpts pins the privacy contract: a
// [privacy]-flagged excerpt is dropped, clean excerpts survive.
func TestBuildSummaryRequestFiltersFlaggedExcerpts(t *testing.T) {
	request, err := BuildSummaryRequest(SummaryBuildInput{
		Version: SummarySchemaVersion, Objective: "objective",
		SourceRange:  summaryExcerptRange(t),
		PolicyDigest: strings.Repeat("a", 64),
		Provider:     "fake", Model: "model",
		EndpointAllowlist: []string{"https://x.invalid"},
		RedactionPolicy:   contextstate.RedactionPolicy{Configured: true, Patterns: []string{`token\s*=\s*\S+`}},
		Budget:            4096, OutputLimit: 512,
		SourceExcerpts: []SourceExcerpt{
			{Role: "user", Text: "clean text"},
			{Role: "assistant", Text: "the token = abc123 leaked"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(request.SourceExcerpts) != 1 || request.SourceExcerpts[0].Text != "clean text" {
		t.Fatalf("filtered excerpts = %+v, want only the clean item", request.SourceExcerpts)
	}
}

// TestSummaryRequestValidateRejectsBadExcerpts pins the request validation:
// unknown roles, oversized text, and control characters are refused.
func TestSummaryRequestValidateRejectsBadExcerpts(t *testing.T) {
	base := func() SummaryRequest {
		request, err := BuildSummaryRequest(SummaryBuildInput{
			Version: SummarySchemaVersion, Objective: "objective",
			SourceRange: summaryExcerptRange(t), PolicyDigest: strings.Repeat("a", 64),
			Provider: "fake", Model: "model", EndpointAllowlist: []string{"https://x.invalid"},
			RedactionPolicy: contextstate.RedactionPolicy{Configured: true, Patterns: []string{"never-match"}},
			Budget:          4096, OutputLimit: 512,
			SourceExcerpts: []SourceExcerpt{{Role: "user", Text: "fine"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return request
	}
	cases := []struct {
		name    string
		mutate  func(*SummaryRequest)
		wantErr bool
	}{
		{"valid", func(*SummaryRequest) {}, false},
		{"unknown role", func(r *SummaryRequest) { r.SourceExcerpts[0].Role = "system" }, true},
		{"oversized text", func(r *SummaryRequest) { r.SourceExcerpts[0].Text = strings.Repeat("x", MaxSummaryFieldBytes+1) }, true},
		{"control character", func(r *SummaryRequest) { r.SourceExcerpts[0].Text = "bad\x00text" }, true},
		{"too many items", func(r *SummaryRequest) {
			r.SourceExcerpts = make([]SourceExcerpt, MaxSummaryItems+1)
			for i := range r.SourceExcerpts {
				r.SourceExcerpts[i] = SourceExcerpt{Role: "user", Text: "x"}
			}
		}, true},
	}
	for _, c := range cases {
		request := base()
		c.mutate(&request)
		err := request.Validate()
		if (err != nil) != c.wantErr {
			t.Fatalf("%s: Validate err = %v, want error = %v", c.name, err, c.wantErr)
		}
	}
}

// TestSummaryPromptRendersSourceExcerpts pins the provider rendering: the
// excerpt section appears between the envelope lists and the echo block,
// labeled by role and tool name, and the system prompt tells the model what
// the section is.
func TestSummaryPromptRendersSourceExcerpts(t *testing.T) {
	request, err := BuildSummaryRequest(SummaryBuildInput{
		Version: SummarySchemaVersion, Objective: "objective", State: "state",
		SourceRange:  summaryExcerptRange(t),
		PolicyDigest: strings.Repeat("a", 64),
		Provider:     "fake", Model: "model",
		EndpointAllowlist: []string{"https://x.invalid"},
		RedactionPolicy:   contextstate.RedactionPolicy{Configured: true, Patterns: []string{"never-match"}},
		Budget:            4096, OutputLimit: 512,
		SourceExcerpts: []SourceExcerpt{
			{Role: "user", Text: "task framing"},
			{Role: "tool", Name: "grep", Text: "internal/auth/jwt.go:42"},
			{Role: "assistant", Text: "rotation added"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	messages := summaryMessages(request)
	userPrompt := messages[len(messages)-1].Content
	systemPrompt := messages[0].Content
	if !strings.Contains(userPrompt, "source_excerpts") {
		t.Fatal("user prompt has no source_excerpts section")
	}
	if !strings.Contains(userPrompt, "[user] task framing") || !strings.Contains(userPrompt, "[tool grep] internal/auth/jwt.go:42") || !strings.Contains(userPrompt, "[assistant] rotation added") {
		t.Fatalf("excerpt rendering = %q", userPrompt)
	}
	echoAt := strings.Index(userPrompt, "Echo these values")
	excerptAt := strings.Index(userPrompt, "source_excerpts")
	if excerptAt == -1 || echoAt == -1 || excerptAt > echoAt {
		t.Fatal("excerpt section must precede the echo block")
	}
	if !strings.Contains(systemPrompt, "source_excerpts") {
		t.Fatal("system prompt does not explain the excerpt section")
	}
}

// TestSourceExcerptsTolerateElidedRetainedMessages pins the diff walk against
// planner elision: a retained message whose body was rewritten to an elision
// notice or reasoning marker is still RETAINED, never classified as dropped.
// Without this, the walk stalls at the first rewritten message and drops the
// classification for everything after it, including the live objective.
func TestSourceExcerptsTolerateElidedRetainedMessages(t *testing.T) {
	input := []provider.Message{
		{Role: provider.RoleUser, Content: "earlier task"},
		{Role: provider.RoleAssistant, Content: "did work", ReasoningContent: "chain of thought"},
		{Role: provider.RoleTool, Name: "grep", Content: strings.Repeat("hit\n", 600)},
		{Role: provider.RoleUser, Content: "current objective"},
	}
	retained := []provider.Message{
		input[0],
		{Role: provider.RoleAssistant, Content: "did work", ReasoningContent: "[reasoning elided by context compaction]"},
		{Role: provider.RoleTool, Name: "grep", Content: "[context elided prior tool result; original size about 4 KiB]"},
		input[3],
	}
	if excerpts := SourceExcerpts(input, retained); len(excerpts) != 0 {
		t.Fatalf("elision-rewritten retained messages misclassified as dropped: %+v", excerpts)
	}
	// When the head is ALSO genuinely dropped, only it may ride the request.
	if excerpts := SourceExcerpts(input, retained[1:]); len(excerpts) != 1 || excerpts[0].Text != "earlier task" {
		t.Fatalf("excerpts = %+v, want only the genuinely dropped first user message", excerpts)
	}
	// The content-free evidence diff shares the walk; it must agree.
	if evidence := OmittedEvidence(input, retained); len(evidence) != 0 {
		t.Fatalf("evidence diff misclassified elided retained messages: %v", evidence)
	}
}

// TestOmittedEvidenceIsUnique pins the contract the summary envelope
// validator requires: evidence items must be distinct. Items are content-free
// (role plus size bucket, e.g. "user message (~2 KiB)"), so any two dropped
// messages of the same role landing in the same bucket produce the SAME
// string - the normal case, not an edge case, once a compaction drops several
// similar messages.
//
// Duplicates made BuildSummaryRequest fail with "summary evidence contains
// duplicate items", and every summary caller degrades silently on a build
// error, so automatic compaction produced no summary at all while reporting
// success. Only the manual /compact path deduped, in its own helper.
func TestOmittedEvidenceIsUnique(t *testing.T) {
	same := strings.Repeat("x", 400)
	input := []provider.Message{
		{Role: provider.RoleUser, Content: same},
		{Role: provider.RoleAssistant, Content: same},
		{Role: provider.RoleUser, Content: same},
		{Role: provider.RoleAssistant, Content: same},
		{Role: provider.RoleUser, Content: same},
		{Role: provider.RoleUser, Content: "kept"},
	}
	retained := []provider.Message{{Role: provider.RoleUser, Content: "kept"}}

	evidence := OmittedEvidence(input, retained)
	if len(evidence) == 0 {
		t.Fatal("no evidence derived from five dropped messages")
	}
	seen := map[string]bool{}
	for _, item := range evidence {
		if seen[item] {
			t.Fatalf("OmittedEvidence returned duplicate item %q in %v", item, evidence)
		}
		seen[item] = true
	}

	// The whole point of uniqueness: the envelope must build.
	if _, err := BuildSummaryRequest(SummaryBuildInput{
		Version:           SummarySchemaVersion,
		Objective:         "objective",
		Evidence:          evidence,
		SourceRange:       contextstate.SourceRange{Start: contextstate.SourceID{SessionID: "s", Sequence: 1}, End: contextstate.SourceID{SessionID: "s", Sequence: 2}},
		PolicyDigest:      strings.Repeat("a", 64),
		Provider:          "fake",
		Model:             "model",
		EndpointAllowlist: []string{"https://summary.invalid"},
		RedactionPolicy:   contextstate.RedactionPolicy{Configured: true, Patterns: []string{"never-match"}},
		Budget:            4000,
		OutputLimit:       512,
	}); err != nil {
		t.Fatalf("BuildSummaryRequest rejected OmittedEvidence output: %v", err)
	}
}

// TestOmittedEvidenceCapCountsDistinctItems pins that the MaxSummaryItems cap
// bounds DISTINCT items. Capping before dedup let a run of identical items
// consume the whole budget and report one fact.
func TestOmittedEvidenceCapCountsDistinctItems(t *testing.T) {
	var input []provider.Message
	for i := 0; i < MaxSummaryItems*3; i++ {
		input = append(input, provider.Message{Role: provider.RoleUser, Content: strings.Repeat("x", 400)})
	}
	input = append(input,
		provider.Message{Role: provider.RoleAssistant, Content: strings.Repeat("y", 400)},
		provider.Message{Role: provider.RoleUser, Content: "kept"})
	retained := []provider.Message{{Role: provider.RoleUser, Content: "kept"}}

	evidence := OmittedEvidence(input, retained)
	if len(evidence) > MaxSummaryItems {
		t.Fatalf("evidence exceeded the cap: %d > %d", len(evidence), MaxSummaryItems)
	}
	// The assistant item sits past a long run of identical user items; a cap
	// applied before dedup would have dropped it.
	var sawAssistant bool
	for _, item := range evidence {
		if strings.HasPrefix(item, provider.RoleAssistant) {
			sawAssistant = true
		}
	}
	if !sawAssistant {
		t.Fatalf("distinct assistant evidence lost behind duplicate user items: %v", evidence)
	}
}
