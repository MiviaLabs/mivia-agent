package ledger

import (
	"math"
	"testing"
)

func TestParseSuffixNum(t *testing.T) {
	tests := []struct {
		name, s, prefix string
		want            uint64
	}{
		{"normal match", "se-42", "se-", 42},
		{"zero value", "se-0", "se-", 0},
		{"large number", "run-999999999999", "run-", 999999999999},
		{"max uint64", "run-18446744073709551615", "run-", math.MaxUint64},
		{"empty string", "", "se-", 0}, {"no prefix", "42", "se-", 0},
		{"non-numeric", "se-abc", "se-", 0}, {"overflow", "se-99999999999999999999", "se-", 0},
		{"prefix not at start", "xse-42", "se-", 0}, {"empty prefix content", "hello", "", 0},
		{"empty prefix empty", "", "", 0}, {"negative", "se--5", "se-", 0},
		{"partial prefix", "senior-42", "se-", 0}, {"trailing text", "se-123abc", "se-", 0},
		{"just prefix", "se-", "se-", 0}, {"dots", "run-3.14", "run-", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseSuffixNum(tt.s, tt.prefix); got != tt.want {
				t.Errorf("parseSuffixNum(%q, %q) = %d, want %d", tt.s, tt.prefix, got, tt.want)
			}
		})
	}
}
