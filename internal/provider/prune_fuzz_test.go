package provider

// Deterministic fuzz target for PruneMessagesKeepTurns (native Go fuzz, repo
// convention FuzzXxx(f *testing.F) with a seed corpus, as FuzzReadStreamReceived
// in this package). Generates structured message histories - roles, tool-call
// IDs, results, and budgets including 0/negative/huge - and asserts the pruning
// invariants:
//
//   - no panic; the pruned history never grows;
//   - ValidateToolPairing(pruned) is nil whenever the input was validly paired
//     (an exchange is never split);
//   - MessagesTokens(pruned) <= budget when the header plus the newest exchange
//     fit, and an over-budget result holds no droppable exchange (fail-closed);
//   - pruning is deterministic;
//   - a system message, when present, stays at index 0.
//
// The fix that made pruning linear (pruneWithinTurn is one pass, O(n)) is what
// keeps the bounded -fuzztime gate tractable on histories the quadratic
// per-drop loop blew through.

import (
	"strconv"
	"strings"
	"testing"
)

func FuzzPruneMessagesKeepTurns(f *testing.F) {
	seeds := []string{
		"",
		"100\n",
		"100\ns|sys\n",
		"100\ns|sys\nu|hi\n",
		"50\ns|sys\nu|go\nc|c1|read_file|{}|\nt|c1|read_file|data\n",
		"50\ns|sys\nu|go\nc|c1|read_file|{}|\nt|c1|read_file|data\nc|c2|read_file|{}|\nt|c2|read_file|more\n",
		"50\ns|sys\nu|go\na|plain answer\n",
		"50\ns|sys\nu|go\nc|c1|read_file|{}|\nt|c1|read_file|data\na|closing note\n",
		"10\nu|go\nc|c1|read_file|{}|\nt|c1|read_file|data\n",
		"0\ns|sys\nu|go\nc|c1|read_file|{}|\nt|c1|read_file|data\n",
		"-5\ns|sys\nu|go\n",
		"999999999\ns|sys\nu|go\nc|c1|read_file|{}|\nt|c1|read_file|data\n",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		budget, msgs, ok := parsePruneFuzzInput(data)
		if !ok {
			t.Skip()
		}
		valid := ValidateToolPairing(msgs) == nil

		pruned := PruneMessagesKeepTurns(msgs, budget)

		if len(pruned) > len(msgs) {
			t.Fatalf("pruned history grew: %d -> %d", len(msgs), len(pruned))
		}
		if len(msgs) > 0 && msgs[0].Role == RoleSystem {
			if len(pruned) == 0 || pruned[0].Role != RoleSystem {
				t.Fatal("system message no longer at index 0")
			}
		}
		if valid {
			if err := ValidateToolPairing(pruned); err != nil {
				t.Fatalf("pruned a valid history into an invalid one: %v", err)
			}
		}

		// Determinism: the same input must prune to the same history.
		again := PruneMessagesKeepTurns(msgs, budget)
		if !samePrunedMessages(pruned, again) {
			t.Fatal("pruning is not deterministic")
		}

		// Budget, fail-closed direction: with a positive budget, a pruned
		// history that is still over budget must hold no removable exchange -
		// dropping more was impossible (only the turn header and plain replies
		// are left). For valid histories an assistant tool_call with results
		// is always a removable exchange, so such a result proves the pruner
		// stopped early.
		if valid && budget > 0 && MessagesTokens(pruned) > budget {
			for _, m := range pruned {
				if m.Role == RoleAssistant && len(m.ToolCalls) > 0 {
					t.Fatalf("over-budget pruned history (%d > %d) still holds a droppable exchange", MessagesTokens(pruned), budget)
				}
			}
		}

		// Budget, must-fit direction: for a valid single-turn history (one user
		// message), the newest exchange is the last assistant tool_call block
		// plus its consecutive results, and the never-dropped part (the header)
		// is everything outside that block - the user message, plain assistant
		// messages before it, and any plain assistant messages after it. If the
		// header plus the newest exchange fits the budget, the pruned history
		// must fit too.
		if valid && budget > 0 && userMessageCount(msgs) == 1 {
			if start, end, ok := newestExchangeRange(msgs); ok {
				header := MessagesTokens(msgs[:start]) + MessagesTokens(msgs[end:])
				newest := MessagesTokens(msgs[start:end])
				if header+newest <= budget && MessagesTokens(pruned) > budget {
					t.Fatalf("pruned history (%d) exceeds budget %d although header+newest exchange fit (%d)", MessagesTokens(pruned), budget, header+newest)
				}
			}
		}
	})
}

// parsePruneFuzzInput decodes the structured fuzz input: the first line is the
// token budget (0/negative/huge are all legal), and each following line is one
// message encoded as role|field|...:
//
//	s|<content>                   system
//	u|<content>                   user
//	a|<content>                   assistant (plain)
//	c|<id>|<name>|<args>|<content>  assistant announcing one tool call
//	t|<id>|<name>|<content>       tool result answering <id>
//
// ok is false when the input cannot be decoded (the fuzz harness skips it).
func parsePruneFuzzInput(data []byte) (budget int, msgs []Message, ok bool) {
	lines := strings.Split(string(data), "\n")
	budget, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		return 0, nil, false
	}
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 5)
		switch parts[0] {
		case "s":
			msgs = append(msgs, Message{Role: RoleSystem, Content: pruneFuzzField(parts, 1)})
		case "u":
			msgs = append(msgs, Message{Role: RoleUser, Content: pruneFuzzField(parts, 1)})
		case "a":
			msgs = append(msgs, Message{Role: RoleAssistant, Content: pruneFuzzField(parts, 1)})
		case "c":
			if len(parts) < 4 {
				return 0, nil, false
			}
			call := ToolCall{ID: parts[1], Type: "function"}
			call.Function.Name = parts[2]
			call.Function.Arguments = parts[3]
			msgs = append(msgs, Message{Role: RoleAssistant, ToolCalls: []ToolCall{call}, Content: pruneFuzzField(parts, 4)})
		case "t":
			if len(parts) < 3 {
				return 0, nil, false
			}
			msgs = append(msgs, Message{Role: RoleTool, ToolCallID: parts[1], Name: parts[2], Content: pruneFuzzField(parts, 3)})
		default:
			return 0, nil, false
		}
	}
	return budget, msgs, true
}

func pruneFuzzField(parts []string, index int) string {
	if index < len(parts) {
		return parts[index]
	}
	return ""
}

func userMessageCount(msgs []Message) int {
	count := 0
	for _, m := range msgs {
		if m.Role == RoleUser {
			count++
		}
	}
	return count
}

// newestExchangeRange returns the index range of the newest removable exchange
// block: the last assistant tool_call message plus the consecutive tool results
// answering it. ok is false when the history holds no assistant tool_call
// message. For validly paired histories the results answering a call are
// exactly the consecutive tool messages following it, so the range is exact.
func newestExchangeRange(msgs []Message) (start, end int, ok bool) {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == RoleAssistant && len(msgs[i].ToolCalls) > 0 {
			end := i + 1
			for end < len(msgs) && msgs[end].Role == RoleTool {
				end++
			}
			return i, end, true
		}
	}
	return 0, 0, false
}

// samePrunedMessages compares two pruned histories field by field. CreatedAt is
// not part of the fuzz encoding (inputs always carry the zero value) and is
// deliberately not compared.
func samePrunedMessages(a, b []Message) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Role != b[i].Role || a[i].Content != b[i].Content ||
			a[i].ToolCallID != b[i].ToolCallID || a[i].Name != b[i].Name ||
			a[i].ReasoningContent != b[i].ReasoningContent || len(a[i].ToolCalls) != len(b[i].ToolCalls) {
			return false
		}
		for j := range a[i].ToolCalls {
			if a[i].ToolCalls[j].ID != b[i].ToolCalls[j].ID ||
				a[i].ToolCalls[j].Type != b[i].ToolCalls[j].Type ||
				a[i].ToolCalls[j].Function.Name != b[i].ToolCalls[j].Function.Name ||
				a[i].ToolCalls[j].Function.Arguments != b[i].ToolCalls[j].Function.Arguments {
				return false
			}
		}
	}
	return true
}
