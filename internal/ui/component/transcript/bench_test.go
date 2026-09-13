package transcript

import (
	"fmt"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// benchModel builds a transcript at the MaxTranscriptLines bound with a
// mixed conversation: user turns, markdown answers, tool calls with
// output, reasoning, and notices, cycling in the order a real session
// produces them. Blocks arrive through HandleEvent so the model holds
// exactly what the live path would.
func benchModel(b *testing.B, width, height int) Model {
	b.Helper()
	m := New(loadTheme(b), theme.TierTrueColor)
	m.SetSize(width, height)
	const want = uikitconfig.MaxTranscriptLines
	for i := 0; len(m.blocks) < want; i++ {
		id := fmt.Sprintf("call-%d", i)
		events := []uievent.Event{
			{Kind: uievent.KindTurnStart, Body: uievent.TurnStartBody{Input: fmt.Sprintf("question %d about retries", i)}},
			{Kind: uievent.KindReasoning, Body: uievent.ReasoningDeltaBody{Text: "weigh the cap against the jitter", WordCount: 6}},
			{Kind: uievent.KindToolStart, Body: uievent.ToolStartBody{ToolCallID: id, Name: "read_file", Args: map[string]any{"path": fmt.Sprintf("internal/retry/%d.go", i)}}},
			{Kind: uievent.KindToolOutput, Body: uievent.ToolOutputBody{ToolCallID: id, Chunk: "package retry\n\nfunc Backoff() {}\n"}},
			{Kind: uievent.KindToolEnd, Body: uievent.ToolEndBody{ToolCallID: id, Name: "read_file", OK: true}},
			{Kind: uievent.KindTextEnd, Body: uievent.TextEndBody{Text: "Retries back off with **jitter** and a cap of `5s`.\n\n- one\n- two\n"}},
			{Kind: uievent.KindNotice, Body: uievent.NoticeBody{Text: "prompt cache: 97% hit"}},
		}
		for _, ev := range events {
			m, _ = m.HandleEvent(ev)
		}
	}
	if got := len(m.blocks); got != want {
		b.Fatalf("model holds %d blocks, want %d", got, want)
	}
	return m
}

// BenchmarkLayout is the Phase 0 baseline for one repaint of the
// transcript at the block bound: layout() over 2,000 mixed blocks plus
// styling the visible slice at 80x24, following the tail.
func BenchmarkLayout(b *testing.B) {
	m := benchModel(b, 80, 24)
	m = m.ScrollToBottom()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if rows := m.Rows(); len(rows) != 24 {
			b.Fatalf("Rows() returned %d rows, want 24", len(rows))
		}
	}
}
