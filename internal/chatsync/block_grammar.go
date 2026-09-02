package chatsync

import (
	"regexp"
	"strconv"
)

// ProseBlockPattern is the grammar of a prose block id: a STREAM id - one of
// `<turn>:assistant`, `<turn>:thinking`, `<turn>:<task>:assistant`,
// `<turn>:<task>:thinking` - followed by `:<step>`. It is anchored on the
// stream suffix, not on `.+`: a looser `.+:\d+` would also accept a
// tool_call_id, which ends in digits under any id scheme, and a consumer
// keying on a parsed step must never mistake a tool row for prose.
//
// A reset names the bare stream with no step, so it deliberately does not
// match; nor does the legacy stepless id older producers wrote.
var ProseBlockPattern = regexp.MustCompile(`^(.+:(?:assistant|thinking)):(\d+)$`)

// proseBlock builds a prose block id. The separator here and the pattern
// above are the two halves of one grammar; TestProseBlockMatchesTheRecordedGrammar
// holds them together.
func proseBlock(stream string, segment int) string {
	return stream + ":" + strconv.Itoa(segment)
}

// BlockGrammar is the recorded grammar of the envelope's block field, the
// values api/contracts/chat-sessions.v1.json publishes under blockGrammar.
// TestBlockGrammarMatchesContractSnapshot holds the two together, so the
// consumer's vendored copy cannot drift looser than what this producer
// asserts.
type BlockGrammar struct {
	Prose                  string   `json:"prose"`
	Streams                []string `json:"streams"`
	ToolBlockField         string   `json:"toolBlockField"`
	ResetBlockIsStreamOnly bool     `json:"resetBlockIsStreamOnly"`
	StepIsDense            bool     `json:"stepIsDense"`
	StepReuse              string   `json:"stepReuse"`
}

// RecordedBlockGrammar returns the grammar this producer implements.
func RecordedBlockGrammar() BlockGrammar {
	return BlockGrammar{
		Prose:                  ProseBlockPattern.String(),
		Streams:                []string{"<turn>:assistant", "<turn>:thinking", "<turn>:<task>:assistant", "<turn>:<task>:thinking"},
		ToolBlockField:         "tool_call_id",
		ResetBlockIsStreamOnly: true,
		StepIsDense:            false,
		StepReuse:              "never within a producer session",
	}
}

// parseProseBlock splits a prose block id into its stream and step. ok is
// false for a tool id, a reset's bare stream id, or a legacy stepless id.
func parseProseBlock(block string) (stream string, step int, ok bool) {
	m := ProseBlockPattern.FindStringSubmatch(block)
	if m == nil {
		return "", 0, false
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return "", 0, false
	}
	return m[1], n, true
}
