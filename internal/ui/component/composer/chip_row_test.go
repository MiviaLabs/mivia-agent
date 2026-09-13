package composer

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

func TestChipRow_TruncateWhenWiderThanInner(t *testing.T) {
	m := New(theme.Theme{}, theme.TierASCII, 80)
	m.chips = []string{"long-chip-label-one", "long-chip-label-two"}
	// Inner width smaller than row width
	row := m.chipRow(10)
	if row == "" {
		t.Fatal("expected non-empty chip row")
	}
}
