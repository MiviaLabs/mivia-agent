package cliorchestrate

// AR-1: the "tool_calls" array inside a dispatchTaskResult is the wire
// contract between this package (producer, loadToolCallSummaries) and
// uiadapter (consumer, encodedTaskResult). Only comments connected the two
// type sets before. The checked-in golden fixture
// testdata/tool_calls_contract.json pins the exact marshal bytes; the
// uiadapter side reads the same file. If a tag name, tag option, or field
// type drifts on either side, one of the two tests fails.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// wireContractFixturePath is relative to this package directory: go test
// sets the working directory to the package under test.
const wireContractFixturePath = "testdata/tool_calls_contract.json"

// canonicalToolCallRows derives the three canonical rows through the REAL
// production path - raw ledger steps in, loadToolCallSummaries merge out -
// so the pinned bytes belong to the actual encoder pipeline, not to a hand
// mirror of it:
//
//   - call_grep_01  start+end pair with input and output   (complete row)
//   - call_read_02  start+end pair with empty input/output (proves all three
//     omitempty tags stay off the wire)
//   - call_bash_03  start with no end                      (incomplete=true tail)
func canonicalToolCallRows(t *testing.T) []toolCallSummary {
	t.Helper()
	repo := ledger.NewMemoryLedgerRepository()
	steps := []subagents.ToolCallStep{
		{ToolCallID: "call_grep_01", Name: "grep", Kind: "start", Input: `{"pattern":"func load"}`},
		{ToolCallID: "call_grep_01", Name: "grep", Kind: "end", Output: "3 matches in 2 files"},
		{ToolCallID: "call_read_02", Name: "read", Kind: "start"},
		{ToolCallID: "call_read_02", Name: "read", Kind: "end"},
		{ToolCallID: "call_bash_03", Name: "bash", Kind: "start", Input: `{"cmd":"go build ./..."}`},
	}
	ref := storeToolCallSteps(t, repo, steps)
	return loadToolCallSummaries(context.Background(), repo, ref)
}

// TestToolCallsWireContractGoldenBytes pins the encoder output to the
// committed golden fixture byte for byte. On mismatch the failure prints the
// full produced line; regeneration is copy that printed line into the
// fixture file (same compact format, no trailing newline).
func TestToolCallsWireContractGoldenBytes(t *testing.T) {
	rows := canonicalToolCallRows(t)
	if len(rows) != 3 {
		t.Fatalf("merge pipeline returned %d rows, want 3 (fixture assumes grep/read/bash order)", len(rows))
	}

	got, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("marshal tool_call summaries: %v", err)
	}
	want, err := os.ReadFile(wireContractFixturePath)
	if err != nil {
		t.Fatalf("read golden fixture %s: %v", wireContractFixturePath, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("tool_calls wire bytes drifted from golden fixture\n  fixture: %s\n  produced: %s", want, got)
	}

	// omitempty stays part of the contract: a zero-value row must produce no
	// empty-string or false keys on the wire. call_read_02 covers it.
	for _, banned := range []string{`"input":""`, `"output":""`, `"incomplete":false`} {
		if strings.Contains(string(got), banned) {
			t.Errorf("produced bytes contain %s; omitempty behavior drifted", banned)
		}
	}
}
