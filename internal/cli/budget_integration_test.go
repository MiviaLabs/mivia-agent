package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
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

func TestIntegrationBudgetChangeAffectsNestedSubagentInvocation(t *testing.T) {
	res := loadPickerConfig(t)
	sess := chat.NewSession(res, welcomeStubCompleter{})
	d, err := NewSessionDispatcherWithBudgetProvider(
		tools.NewRegistry(), sess.Completer, sess.CurrentModel(), config.DefaultSubagentConfig,
		0, sess.PromptBudget(), sess.MaxTokens, sess.PromptBudget,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := sess.SetPromptBudget(20); err != nil {
		t.Fatal(err)
	}
	result := d.Invoke(context.Background(), runtime.Request{
		ID: "budget-change", Kind: runtime.Subagent, Name: "oneshot",
		Input: json.RawMessage(`"nested prompt"`),
	})
	if !errors.Is(result.Err, agent.ErrPromptBudgetExceeded) {
		t.Fatalf("nested invocation error = %v, want %v", result.Err, agent.ErrPromptBudgetExceeded)
	}
}
