package contextmgr

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// fuzzEvidenceToolNames are the fixed tool names the random history generator
// draws from. Evidence content never uses these strings, so the content-free
// assertion cannot false-positive on a tool name.
var fuzzEvidenceToolNames = []string{"read_file", "write_file", "search_replace", "run_command", "list_dir"}

// fuzzEvidenceHistory builds a deterministic, tool-paired message history from
// the fuzz bytes. The generator never emits content equal to a tool name or to
// the evidence format, so the content-free assertions stay exact.
func fuzzEvidenceHistory(seed, contentSeed, variant int64) []provider.Message {
	seed ^= variant * 7919
	next := func() int64 {
		seed = seed*6364136223846793005 + 1442695040888963407
		return seed
	}
	mod := func(value, base int64) int { return int(((value % base) + base) % base) }
	history := []provider.Message{{Role: provider.RoleSystem, Content: "system"}}
	turns := mod(next(), 4) + 1
	for turn := 0; turn < turns; turn++ {
		history = append(history, provider.Message{
			Role:    provider.RoleUser,
			Content: fmt.Sprintf("body-%x-%d", uint64(next()^contentSeed), turn),
		})
		if mod(next(), 2) == 0 {
			call := provider.ToolCall{ID: fmt.Sprintf("call-%d-%x", turn, uint64(next())), Type: "function"}
			call.Function.Name = fuzzEvidenceToolNames[mod(next(), int64(len(fuzzEvidenceToolNames)))]
			call.Function.Arguments = fmt.Sprintf(`{"path":"arg-%x"}`, uint64(next()^contentSeed))
			history = append(history, provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call}})
			size := mod(next(), 4096) + 1
			history = append(history, provider.Message{Role: provider.RoleTool, ToolCallID: call.ID, Name: call.Function.Name, Content: strings.Repeat("body-", size/5+1)[:size]})
		}
		if mod(next(), 3) == 0 {
			history = append(history, provider.Message{Role: provider.RoleAssistant, Content: fmt.Sprintf("answer-%x", uint64(next()^contentSeed))})
		}
	}
	// Keep the latest user objective last, mirroring a real turn.
	history = append(history, provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("objective-%x", uint64(next()^contentSeed))})
	return history
}

// fuzzRetainedSubsequence derives a deterministic retained subsequence from
// the input: the system message, the latest user message, and every message
// whose index satisfies the seed predicate. Any subset in original order is a
// subsequence, so the diff stays well-defined.
func fuzzRetainedSubsequence(input []provider.Message, seed int64) []provider.Message {
	var retained []provider.Message
	for index, message := range input {
		if index == 0 || (seed+int64(index))%3 == 0 || (index == len(input)-1 && message.Role == provider.RoleUser) {
			retained = append(retained, message)
		}
	}
	return retained
}

// FuzzOmittedEvidence asserts the pure diff builder's documented
// postconditions on every input: no panic, output bounded (<= MaxSummaryItems
// items, each <= MaxSummaryFieldBytes, valid UTF-8, no control characters),
// byte-determinism for identical input, and content-freedom (no message
// Content substring, no tool Arguments substring). It is the one deterministic
// fuzz target in this slice: OmittedEvidence has no I/O and no provider
// dependency, so it fuzzes deterministically.
func FuzzOmittedEvidence(f *testing.F) {
	for _, history := range fuzzPlanHistories() {
		f.Add(int64(len(history)), int64(0), int64(0), int64(0))
	}
	f.Add(int64(0), int64(1), int64(2), int64(3))
	f.Add(int64(1), int64(65536), int64(99), int64(7))
	f.Fuzz(func(t *testing.T, historySeed, contentSeed, retainSeed, variant int64) {
		input := fuzzEvidenceHistory(historySeed, contentSeed, variant)
		retained := fuzzRetainedSubsequence(input, retainSeed)
		first := OmittedEvidence(input, retained)
		second := OmittedEvidence(input, retained)
		if !slices.Equal(first, second) {
			t.Fatal("OmittedEvidence is not byte-deterministic for identical input")
		}
		if len(first) > MaxSummaryItems {
			t.Fatalf("evidence items=%d exceed %d", len(first), MaxSummaryItems)
		}
		for _, item := range first {
			if len(item) > MaxSummaryFieldBytes {
				t.Fatalf("evidence item exceeds %d bytes: %d", MaxSummaryFieldBytes, len(item))
			}
			if !utf8.ValidString(item) {
				t.Fatalf("evidence item is not valid UTF-8: %q", item)
			}
			for _, r := range item {
				if unicode.IsControl(r) {
					t.Fatalf("evidence item contains a control character: %q", item)
				}
			}
			for _, message := range input {
				if message.Content != "" && strings.Contains(item, message.Content) {
					t.Fatalf("evidence item leaked message content %q in %q", message.Content, item)
				}
				for _, call := range message.ToolCalls {
					if call.Function.Arguments != "" && strings.Contains(item, call.Function.Arguments) {
						t.Fatalf("evidence item leaked tool arguments %q in %q", call.Function.Arguments, item)
					}
				}
			}
		}
	})
}
