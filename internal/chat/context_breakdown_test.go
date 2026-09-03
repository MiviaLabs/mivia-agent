package chat

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
)

// TestSummaryMessageNameMatchesAgent pins the duplicated sentinel: this
// package classifies the compaction memo by Name, and agent writes that Name.
// A rename on either side silently reclassifies every memo as ordinary prose,
// which no assertion about totals would catch because the total is unchanged.
func TestSummaryMessageNameMatchesAgent(t *testing.T) {
	if summaryMessageName != agent.SummaryMessageName {
		t.Fatalf("summaryMessageName = %q, agent.SummaryMessageName = %q: the sentinel drifted",
			summaryMessageName, agent.SummaryMessageName)
	}
}

// breakdownMessages is one message of every class the classifier separates.
func breakdownMessages() []provider.Message {
	return []provider.Message{
		{Role: provider.RoleSystem, Content: strings.Repeat("s", 400)},
		{Role: provider.RoleUser, Name: MemoryContextMessageName, Content: strings.Repeat("m", 200)},
		{Role: provider.RoleUser, Name: summaryMessageName, Content: strings.Repeat("c", 120)},
		{Role: provider.RoleUser, Content: strings.Repeat("u", 800)},
		{Role: provider.RoleAssistant, Content: strings.Repeat("a", 240), ReasoningContent: strings.Repeat("r", 160)},
		{Role: provider.RoleTool, ToolCallID: "tc-1", Content: strings.Repeat("t", 2000)},
	}
}

func breakdownTools() []provider.ToolSpec {
	return []provider.ToolSpec{
		{"type": "function", "function": map[string]any{"name": "read_file", "description": strings.Repeat("d", 200)}},
		{"type": "function", "function": map[string]any{"name": "write_file", "description": strings.Repeat("d", 200)}},
		{"type": "function", "function": map[string]any{"name": "mcp__linear__issue", "description": strings.Repeat("d", 600)}},
	}
}

// breakdownExternal marks the one server-supplied tool in breakdownTools.
func breakdownExternal() map[string]string {
	return map[string]string{"mcp__linear__issue": "linear"}
}

// TestBreakdownSeparatesEveryClass: each kind of message lands in its own
// bucket. A classifier that fell through to Prose would still produce the
// right total, so only per-bucket assertions catch it.
func TestBreakdownSeparatesEveryClass(t *testing.T) {
	profile := provider.ContextAccountingProfile{}
	b, err := breakdown(breakdownMessages(), breakdownTools(), breakdownExternal(), profile)
	if err != nil {
		t.Fatalf("breakdown: %v", err)
	}
	cases := []struct {
		name string
		got  int
	}{
		{"System", b.System},
		{"ToolSchemas", b.ToolSchemas},
		{"ExternalSchemas", b.ExternalSchemas},
		{"Memory", b.Memory},
		{"Summary", b.Summary},
		{"Prose", b.Prose},
		{"ToolResults", b.ToolResults},
		{"Reasoning", b.Reasoning},
	}
	for _, c := range cases {
		if c.got <= 0 {
			t.Errorf("%s = %d, want a positive charge: its message class fell into another bucket", c.name, c.got)
		}
	}
	if b.ToolCount != 2 {
		t.Errorf("ToolCount = %d, want 2 compiled-in tools", b.ToolCount)
	}
	if b.ExternalToolCount != 1 {
		t.Errorf("ExternalToolCount = %d, want 1 server-supplied tool", b.ExternalToolCount)
	}
	// The server tool carries the longest description, so a classifier that
	// charged it as compiled-in would show up here and nowhere else.
	if b.ExternalSchemas <= b.ToolSchemas {
		t.Errorf("ExternalSchemas = %d, ToolSchemas = %d: the server schema is not being separated", b.ExternalSchemas, b.ToolSchemas)
	}
	// The tool result is the largest single message, so it must outweigh the
	// prose it would have been merged into had the role check been dropped.
	if b.ToolResults <= b.Prose {
		t.Errorf("ToolResults = %d, Prose = %d: the tool result is not being separated", b.ToolResults, b.Prose)
	}
}

// TestBreakdownTotalMatchesEstimatePromptCost is the discriminator for the
// buckets being the SAME arithmetic as the gauge: a bucket that charged a
// field twice, or dropped the request frame, would still look plausible on
// screen but would no longer equal the estimator's own total.
func TestBreakdownTotalMatchesEstimatePromptCost(t *testing.T) {
	profile := provider.ContextAccountingProfile{}
	messages, tools := breakdownMessages(), breakdownTools()
	b, err := breakdown(messages, tools, breakdownExternal(), profile)
	if err != nil {
		t.Fatalf("breakdown: %v", err)
	}
	want, err := provider.EstimatePromptCost(messages, tools, profile)
	if err != nil {
		t.Fatalf("EstimatePromptCost: %v", err)
	}
	if b.Total() != want {
		t.Errorf("breakdown total = %d, EstimatePromptCost = %d: the buckets are not the estimator's own arithmetic", b.Total(), want)
	}
	if b.Floor()+b.Conversation() != b.Total() {
		t.Errorf("Floor+Conversation = %d, Total = %d", b.Floor()+b.Conversation(), b.Total())
	}
}

// TestScaleToSumsExactlyToTheTotal: after calibration the rows must add up to
// the number beside them. Integer division alone leaves a gap of up to one
// token per bucket, which is exactly the visible "the parts do not sum" bug.
func TestScaleToSumsExactlyToTheTotal(t *testing.T) {
	raw := ContextBreakdown{System: 101, ToolSchemas: 303, ExternalSchemas: 88, Memory: 51, Summary: 27, Prose: 199, ToolResults: 777, Reasoning: 33, ToolCount: 7, ExternalToolCount: 2}
	for _, total := range []int{1, 7, 999, 1000, 1493, 4001, raw.Total()} {
		got := raw.scaleTo(total)
		if got.Total() != total {
			t.Errorf("scaleTo(%d).Total() = %d, want exactly %d", total, got.Total(), total)
		}
		if got.ToolCount != 7 || got.ExternalToolCount != 2 {
			t.Errorf("scaleTo(%d) changed the schema counts to %d/%d: a count is not a token cost",
				total, got.ToolCount, got.ExternalToolCount)
		}
	}
}

// TestScaleToHandlesAnEmptyEstimate: a session with nothing priced yet must
// not divide by zero, and must not invent buckets out of a nonzero total.
func TestScaleToHandlesAnEmptyEstimate(t *testing.T) {
	got := ContextBreakdown{ToolCount: 3}.scaleTo(500)
	if got.Total() != 0 {
		t.Errorf("empty breakdown scaled to %d, want 0", got.Total())
	}
	if got.ToolCount != 3 {
		t.Errorf("ToolCount = %d, want 3", got.ToolCount)
	}
}

// TestContextUsageBreakdownSumsToUsedTokens is the end-to-end contract the
// sidebar depends on: the rows it draws add up to the number in its header,
// including under a calibration ratio that moves the total.
func TestContextUsageBreakdownSumsToUsedTokens(t *testing.T) {
	newSession := func() *Session {
		s := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{out: "answer"})
		s.Messages = breakdownMessages()
		s.MaxContextTokens = 100_000
		return s
	}
	for _, tc := range []struct {
		name        string
		calibration contextmgr.Calibration
	}{
		{"uncalibrated", contextmgr.Calibration{}},
		{"over-counting corrected down", contextmgr.Calibration{Ratio: 0.5, Samples: 4}},
		{"under-counting corrected up", contextmgr.Calibration{Ratio: 1.7, Samples: 4}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSession()
			s.Calibration = tc.calibration
			usage := s.ContextUsage()
			if usage.UsedTokens <= 0 {
				t.Fatalf("used tokens = %d, want a positive estimate", usage.UsedTokens)
			}
			if got := usage.Breakdown.Total(); got != usage.UsedTokens {
				t.Errorf("breakdown total = %d, UsedTokens = %d: the rows contradict the header", got, usage.UsedTokens)
			}
		})
	}
}

// TestSkillInvocationsAreChargedApartFromProse: a skill's instruction
// body arrives as an ordinary-looking user message, so nothing but the
// frame distinguishes it. Merged into Prose it is invisible, and the
// commonest cause of a window filling in three turns - one large skill -
// reads as "you talked a lot".
func TestSkillInvocationsAreChargedApartFromProse(t *testing.T) {
	skillBody := skills.RenderNamedSkillSlashPrompt(
		"deep-review", strings.Repeat("review instruction line\n", 40), "the diff")
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: strings.Repeat("s", 400)},
		{Role: provider.RoleUser, Content: skillBody},
		{Role: provider.RoleUser, Content: strings.Repeat("u", 800)},
	}
	b, err := breakdown(messages, nil, nil, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatalf("breakdown: %v", err)
	}
	if b.SkillCount != 1 {
		t.Errorf("SkillCount = %d, want 1", b.SkillCount)
	}
	if b.Skills <= 0 {
		t.Fatal("the skill invocation was charged nothing; it fell into another bucket")
	}
	if b.Skills <= b.Prose {
		t.Errorf("Skills = %d, Prose = %d: the skill body is the larger message, so it is not being separated", b.Skills, b.Prose)
	}
	// The split must not change the total, or the rows stop summing to
	// the number in the header.
	want, err := provider.EstimatePromptCost(messages, nil, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatalf("EstimatePromptCost: %v", err)
	}
	if b.Total() != want {
		t.Errorf("total = %d, EstimatePromptCost = %d: the skills bucket changed the sum", b.Total(), want)
	}
}

// TestAnOrdinaryUserMessageIsNotASkill guards the other half: the frame
// is what marks a skill, and a user who pastes the words must not have
// their message reclassified.
func TestAnOrdinaryUserMessageIsNotASkill(t *testing.T) {
	messages := []provider.Message{
		{Role: provider.RoleUser, Content: "please run the skill-instructions for me"},
	}
	b, err := breakdown(messages, nil, nil, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatalf("breakdown: %v", err)
	}
	if b.SkillCount != 0 || b.Skills != 0 {
		t.Errorf("an ordinary message was charged as a skill: count=%d tokens=%d", b.SkillCount, b.Skills)
	}
}
