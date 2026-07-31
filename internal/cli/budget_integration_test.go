package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
)

func TestIntegrationModelBindingBudgetCommandParityAcrossREPLAndTUI(t *testing.T) {
	res := loadPickerConfig(t)
	replSession := chat.NewSession(res, welcomeStubCompleter{})
	tuiSession := chat.NewSession(res, welcomeStubCompleter{})
	termOutput := new(bytes.Buffer)
	term := &Terminal{out: termOutput}

	if _, _, err := handleSlash("/budget 100", replSession, res, false, term); err != nil {
		t.Fatal(err)
	}
	tui := newTUIModel(tuiSession, res, false)
	tui.handleSlash("/budget 100")
	if replSession.PromptBudget() != 100 || tuiSession.PromptBudget() != 100 {
		t.Fatalf("REPL/TUI budgets = %d/%d, want 100", replSession.PromptBudget(), tuiSession.PromptBudget())
	}

	overCap := fmt.Sprintf("/budget %d", res.MaxContextTokens+1)
	if _, _, err := handleSlash(overCap, replSession, res, false, term); err != nil {
		t.Fatal(err)
	}
	tui.handleSlash(overCap)
	if replSession.PromptBudget() != 100 || tuiSession.PromptBudget() != 100 {
		t.Fatalf("over-cap command mutated budgets: %d/%d", replSession.PromptBudget(), tuiSession.PromptBudget())
	}

	if _, _, err := handleSlash("/budget 100x", replSession, res, false, term); err != nil {
		t.Fatal(err)
	}
	tui.handleSlash("/budget 100x")
	if replSession.PromptBudget() != 100 || tuiSession.PromptBudget() != 100 {
		t.Fatalf("malformed command mutated budgets: %d/%d", replSession.PromptBudget(), tuiSession.PromptBudget())
	}

	if _, _, err := handleSlash("/budget 0", replSession, res, false, term); err != nil {
		t.Fatal(err)
	}
	tui.handleSlash("/budget 0")
	if replSession.PromptBudget() != res.MaxContextTokens || tuiSession.PromptBudget() != res.MaxContextTokens {
		t.Fatalf("cleared budgets = %d/%d, want %d", replSession.PromptBudget(), tuiSession.PromptBudget(), res.MaxContextTokens)
	}
	if !strings.Contains(termOutput.String(), "invalid budget") {
		t.Fatalf("REPL did not report invalid budget: %q", termOutput.String())
	}
}
