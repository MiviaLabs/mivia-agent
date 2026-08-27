package tools

import (
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/diff"
)

const searchReplaceResultMaxBytes = 4096
const overwriteDiffMaxBytes = 512 << 10

// formatSearchReplaceResultAt renders an edit result inside budget bytes. The
// budget is a hard bound on the whole return value - header, diff, and the "…"
// marker that reports the cut - so the declared ResultBudgetBytes is honest
// rather than an estimate of a typical diff.
func formatSearchReplaceResultAt(path string, n int, oldStr, newStr, fullOld, fullNew string, oldLine, budget int) string {
	result, err := diff.Compute(oldStr, newStr, diff.Options{MaxInputBytes: 512 << 10, Timeout: 100 * time.Millisecond})
	noun := "replacement"
	if n != 1 {
		noun = "replacements"
	}
	insertions, deletions := 0, 0
	if err == nil {
		insertions, deletions = diff.Stats(result)
	}
	header := fmt.Sprintf("updated %s (%d %s, +%d −%d)", path, n, noun, insertions, deletions)
	dump := generateUnifiedDiffAt(path, fullOld, fullNew, oldLine)
	if err != nil {
		dump = fmt.Sprintf("--- a/%s\n+++ b/%s\n(diff omitted: %v)", path, path, err)
	}
	return clampEditResult(header, dump, budget)
}

// clampEditResult joins an edit result's header and diff and cuts the whole
// thing to budget, paying for the elision marker out of the budget so the
// declared ResultBudgetBytes bounds the ENTIRE return value. The header is
// preserved where it fits: a truncated diff with intact "+N −M" stats still
// tells the model what happened, while a cut header tells it nothing.
func clampEditResult(header, dump string, budget int) string {
	if budget <= 0 {
		budget = searchReplaceResultMaxBytes
	}
	out := header + "\n" + dump
	if len(out) <= budget {
		return out
	}
	// Keeping the header costs its own bytes plus the newline and the marker;
	// only take that branch when all three actually fit.
	if len(header)+1+len("…") <= budget {
		bodyBudget := budget - len(header) - 1 - len("…")
		// TruncateUTF8 treats a non-positive bound as "no bound" and returns
		// the input whole, so a budget that leaves no room for the diff must
		// drop it here rather than hand the string to the truncator.
		if bodyBudget <= 0 {
			dump = ""
		} else if len(dump) > bodyBudget {
			dump = diff.TruncateUTF8(dump, bodyBudget)
		}
		return header + "\n" + dump + "…"
	}
	if budget > len("...") {
		return diff.TruncateUTF8(out, budget-3) + "..."
	}
	// Budget too small to carry even the elision marker; cut hard rather than
	// return a result that overruns the declaration.
	return diff.TruncateUTF8(out, budget)
}

func generateUnifiedDiffAt(path, oldStr, newStr string, oldLine int) string {
	result, err := diff.Compute(oldStr, newStr, diff.Options{MaxInputBytes: 512 << 10, Timeout: 100 * time.Millisecond})
	if err != nil {
		return fmt.Sprintf("--- a/%s\n+++ b/%s\n(diff omitted: %v)", path, path, err)
	}
	return diff.FormatUnifiedAt(path, result, oldLine, oldLine, 3)
}
