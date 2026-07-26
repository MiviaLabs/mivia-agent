package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func makeBenchRow(name, detail, result string, done, failed bool, expanded bool) toolRow {
	return toolRow{
		Name:     name,
		Detail:   detail,
		Result:   result,
		Start:    time.Now().Add(-time.Second),
		End:      time.Now(),
		Done:     done,
		Failed:   failed,
		Expanded: expanded,
	}
}

func BenchmarkToolPanelCollapsed(b *testing.B) {
	rows := make([]toolRow, 8)
	for i := 0; i < 8; i++ {
		rows[i] = makeBenchRow(fmt.Sprintf("tool_%d", i), `{"path":"main.go"}`, "ok", true, false, false)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = renderToolPanel(rows, 80, time.Now(), -1)
	}
}

func BenchmarkToolPanelExpanded(b *testing.B) {
	rows := make([]toolRow, 8)
	for i := 0; i < 8; i++ {
		rows[i] = makeBenchRow(fmt.Sprintf("tool_%d", i), `{"path":"main.go"}`, strings.Repeat("output line\n", 15), true, false, true)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = renderToolPanel(rows, 80, time.Now(), -1)
	}
}

func BenchmarkToolPanelMixed(b *testing.B) {
	rows := make([]toolRow, 16)
	for i := 0; i < 16; i++ {
		rows[i] = makeBenchRow(fmt.Sprintf("tool_%d", i), `{"path":"main.go"}`, "result line", i%2 == 0, i%5 == 0, i%3 == 0)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = renderToolPanel(rows, 80, time.Now(), -1)
	}
}

func BenchmarkToolPanelLargeOutput(b *testing.B) {
	large := strings.Repeat("line of text\n", 100)
	rows := []toolRow{
		makeBenchRow("run_command", `{"argv":["go","test"]}`, large, true, false, true),
		makeBenchRow("read_file", `{"path":"huge.txt"}`, large, true, false, true),
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = renderToolPanel(rows, 80, time.Now(), -1)
	}
}

func BenchmarkToolPanelNoTools(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = renderToolPanel(nil, 80, time.Now(), -1)
	}
}

func BenchmarkToolPanelSingleTool(b *testing.B) {
	rows := []toolRow{
		makeBenchRow("read_file", `{"path":"main.go"}`, "package main\nfunc main() {}", true, false, true),
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = renderToolPanel(rows, 80, time.Now(), -1)
	}
}

func BenchmarkToolPanelManyCollapsed(b *testing.B) {
	rows := make([]toolRow, 50)
	for i := 0; i < 50; i++ {
		rows[i] = makeBenchRow(fmt.Sprintf("cmd_%d", i), fmt.Sprintf(`{"argv":["echo","%d"]}`, i), fmt.Sprintf("output %d", i), true, false, false)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = renderToolPanel(rows, 80, time.Now(), -1)
	}
}

func BenchmarkToolPanelExpandedAll(b *testing.B) {
	rows := make([]toolRow, 10)
	for i := 0; i < 10; i++ {
		rows[i] = makeBenchRow(fmt.Sprintf("tool_%d", i), `{"path":"test.txt"}`, "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8", true, false, true)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = renderToolPanel(rows, 80, time.Now(), 3) // with selection
	}
}

func BenchmarkToolPanelSelected(b *testing.B) {
	rows := make([]toolRow, 12)
	for i := 0; i < 12; i++ {
		rows[i] = makeBenchRow(fmt.Sprintf("step_%d", i), `{"key":"val"}`, "ok", true, i%4 == 0, false)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = renderToolPanel(rows, 80, time.Now(), 5)
	}
}
