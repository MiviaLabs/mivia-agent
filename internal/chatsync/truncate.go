package chatsync

import (
	"unicode/utf8"
)

const (
	BudgetPromptText    = 32 * 1024
	BudgetAssistantText = 32 * 1024
	BudgetToolOutput    = 16 * 1024
	BudgetDeltaText     = 8 * 1024
	BudgetToolInput     = 4 * 1024
	BudgetErrorMessage  = 2 * 1024
	BudgetShortField    = 200
)

// truncateString cuts s to fit within maxBytes on rune boundaries.
// It returns the kept string, kept byte count, original byte count, and whether truncation occurred.
func truncateString(s string, maxBytes int) (string, int, int, bool) {
	totalBytes := len(s)
	if totalBytes <= maxBytes {
		return s, totalBytes, totalBytes, false
	}
	if maxBytes <= 0 {
		return "", 0, totalBytes, true
	}

	// Back off across the rune at the CUT BOUNDARY only. Validating the whole
	// prefix (utf8.ValidString) would trim all the way back to the first
	// invalid byte anywhere in s, amputating content the budget allowed and
	// reporting it as an ordinary budget cut. It is also O(n^2).
	// utf8.DecodeLastRuneInString reports (RuneError, 1) for a byte that
	// cannot start a rune or for an incomplete trailing sequence; a real
	// U+FFFD decodes with size 3 and is kept.
	cut := s[:maxBytes]
	for len(cut) > 0 {
		r, size := utf8.DecodeLastRuneInString(cut)
		if r != utf8.RuneError || size > 1 {
			break
		}
		cut = cut[:len(cut)-1]
	}
	return cut, len(cut), totalBytes, true
}

func applyTruncation(env *Envelope, fieldName, value string, maxBytes int) string {
	keptStr, keptLen, totalLen, truncated := truncateString(value, maxBytes)
	if truncated {
		if env.Trunc == nil {
			env.Trunc = &Truncation{Fields: make(map[string]TruncField)}
		}
		env.Trunc.Fields[fieldName] = TruncField{
			Kept:  keptLen,
			Total: totalLen,
		}
	}
	return keptStr
}
