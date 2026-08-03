package cli

import (
	"strings"
	"testing"
)

// paging_bench_test.go measures the transient garbage pageResponse allocates
// per page. PERF-1: the old boundary-slice + binary-search implementation
// materialized every rune boundary into a []int and re-marshalled the whole
// payload ~log2(page) times per page (~3 MB garbage per 32 KiB page). The
// single-pass walk must cut that to roughly payload + framing and one marshal.

const pagingBenchBodyLen = 100 << 10 // 100 KiB

func BenchmarkReadOutputPageResponse(b *testing.B) {
	tool := &readOutputTool{}
	content := strings.Repeat("A", pagingBenchBodyLen)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		offset := (i % 3) * (32 << 10) // page 0, 1, or 2 of the 100 KiB body
		if _, err := tool.pageResponse("ref:output:x", pagingBenchBodyLen, offset, 32<<10, content); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLedgerReadPageResponse(b *testing.B) {
	tool := &ledgerReadTool{}
	content := strings.Repeat("A", pagingBenchBodyLen)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		offset := (i % 3) * (32 << 10) // page 0, 1, or 2 of the 100 KiB body
		if _, err := tool.pageResponse("ref:x", "ledger", pagingBenchBodyLen, offset, 32<<10, content); err != nil {
			b.Fatal(err)
		}
	}
}
