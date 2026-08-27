package render

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// Header and Wrap run on every repaint of every block, so a regression
// here is a regression in the whole transcript. Each budget below is the
// measured baseline plus a small margin, not a round number: a round
// number hides a constant few-allocation regression.

func BenchmarkHeader(b *testing.B) {
	th := benchTheme(b)
	spec := HeaderSpec{
		Marker: "v", Label: "run_command",
		Detail: "command=go test ./internal/storage/...",
		Meta:   "4100ms", State: "failed", StateRole: theme.RoleDanger,
	}
	b.ReportAllocs()
	for b.Loop() {
		Header(th, theme.TierTrueColor, 80, spec)
	}
}

// TestHeaderAllocationBudget pins the allocation count. Measured
// baseline on 2026-08-19: 43 allocations at truecolour, 80 columns, for
// the four-column spec below. The budget adds a margin of 5. That margin
// is tight on purpose: styling one more segment costs about 4
// allocations, so a new column or a new intermediate string trips it,
// while ordinary noise does not.
func TestHeaderAllocationBudget(t *testing.T) {
	th := loadTheme(t)
	spec := HeaderSpec{
		Marker: "v", Label: "run_command",
		Detail: "command=go test ./internal/storage/...",
		Meta:   "4100ms", State: "failed", StateRole: theme.RoleDanger,
	}
	const budget = 48
	got := testing.AllocsPerRun(200, func() {
		Header(th, theme.TierTrueColor, 80, spec)
	})
	if got > budget {
		t.Errorf("Header allocates %.0f times, budget is %d", got, budget)
	}
}

func BenchmarkWrap(b *testing.B) {
	text := strings.Repeat("the quick brown fox jumps over the lazy dog ", 8)
	b.ReportAllocs()
	for b.Loop() {
		Wrap(text, 76)
	}
}

// TestWrapAllocationBudget pins the wrap cost. Measured baseline on
// 2026-08-19: 79 allocations for a 352-column paragraph at a 76-column
// measure, which is 5 output rows over 72 words. The budget adds a
// margin of 9. Wrap degrades by allocating per WORD rather than per row,
// and 72 words would blow past this immediately, so the margin catches
// the realistic regression without failing on noise.
func TestWrapAllocationBudget(t *testing.T) {
	text := strings.Repeat("the quick brown fox jumps over the lazy dog ", 8)
	const budget = 88
	got := testing.AllocsPerRun(200, func() {
		Wrap(text, 76)
	})
	if got > budget {
		t.Errorf("Wrap allocates %.0f times, budget is %d", got, budget)
	}
}

func benchTheme(b *testing.B) theme.Theme {
	b.Helper()
	themes, err := theme.Embedded()
	if err != nil {
		b.Fatal(err)
	}
	for _, th := range themes {
		if th.Name == "mivia-dark" {
			return th
		}
	}
	b.Fatal("mivia-dark theme not found")
	return theme.Theme{}
}
