package clichat

import (
	"context"
	"encoding/json"
	"errors"
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestNewSessionDispatcherOptsBuildsDispatcher exercises the shipped options
// constructor: registration of multi_step/delegate and live budget wiring.
func TestNewSessionDispatcherOptsBuildsDispatcher(t *testing.T) {
	res := loadPickerConfig(t)
	sess := chat.NewSession(res, welcomeStubCompleter{})
	d, err := NewSessionDispatcher(SessionDispatcherOpts{
		Registry:         tools.NewRegistry(),
		Completer:        sess.Completer,
		Model:            sess.CurrentModel(),
		Config:           config.DefaultSubagentConfig,
		MaxContextTokens: sess.PromptBudget(),
		MaxTokens:        sess.MaxTokens,
		Budget:           sess.PromptBudget,
	})
	if err != nil {
		t.Fatalf("NewSessionDispatcher: %v", err)
	}
	defer d.Close()

	if !d.Has(runtime.Subagent, cliorchestrate.HandlerMultiStep) {
		t.Fatal("multi_step handler not registered")
	}
	if !d.Has(runtime.Subagent, cliorchestrate.HandlerDelegate) {
		t.Fatal("delegate handler not registered")
	}
	if !d.Has(runtime.Tool, "delegate") || !d.Has(runtime.Tool, "dispatch_tasks") {
		t.Fatal("delegation tools not registered")
	}

	if err := sess.SetPromptBudget(20); err != nil {
		t.Fatal(err)
	}
	result := d.Invoke(context.Background(), runtime.Request{
		ID: "opts-budget", Kind: runtime.Subagent, Name: cliorchestrate.HandlerOneshot,
		Input: json.RawMessage(`"nested prompt"`),
	})
	if !errors.Is(result.Err, agent.ErrPromptBudgetExceeded) {
		t.Fatalf("nested invocation error = %v, want %v", result.Err, agent.ErrPromptBudgetExceeded)
	}
}

// TestNewSessionDispatcherClosesOwnedStore asserts that when the constructor
// opens SQLite itself, Dispatcher.Close closes the owned store.
func TestNewSessionDispatcherClosesOwnedStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owned-close.db")
	cfg := config.SubagentConfig{
		StoreBackend:   "sqlite",
		StorePath:      path,
		DefaultTimeout: 60,
		MaxWorkers:     1,
	}
	d, err := NewSessionDispatcher(SessionDispatcherOpts{
		Registry:  tools.NewRegistry(),
		Completer: nullCompleter{},
		Model:     "test-model",
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("NewSessionDispatcher: %v", err)
	}
	repo := cliorchestrate.OrchestrationRepoForDispatcher(d)
	sr, ok := repo.(*ledger.StorageLedgerRepository)
	if !ok || sr == nil {
		t.Fatalf("expected StorageLedgerRepository, got %T", repo)
	}
	d.Close()
	if _, err := sr.ListRuns(context.Background()); err == nil {
		t.Fatal("expected error after dispatcher close closed the owned store")
	}
}

// TestNewSessionDispatcherUsesCallerRepo pins that a non-nil opts.Repo is
// used as-is (caller-owned) rather than re-opening a store from Config.
func TestNewSessionDispatcherUsesCallerRepo(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d, err := NewSessionDispatcher(SessionDispatcherOpts{
		Registry:  tools.NewRegistry(),
		Completer: nullCompleter{},
		Model:     "test-model",
		Config:    config.SubagentConfig{DefaultTimeout: 60, StoreBackend: "memory"},
		Repo:      repo,
	})
	if err != nil {
		t.Fatalf("NewSessionDispatcher: %v", err)
	}
	defer d.Close()
	got := cliorchestrate.OrchestrationRepoForDispatcher(d)
	if got != repo {
		t.Fatalf("dispatcher repo = %p, want caller repo %p", got, repo)
	}
}

// TestNewSessionDispatcherRegistersSkillReg pins SkillReg forwarding.
func TestNewSessionDispatcherRegistersSkillReg(t *testing.T) {
	skillReg := skills.NewRegistry()
	if err := skillReg.Register(skills.Definition{
		Name: "opts-skill",
	}); err != nil {
		t.Fatal(err)
	}
	d, err := NewSessionDispatcher(SessionDispatcherOpts{
		Registry:  tools.NewRegistry(),
		Completer: nullCompleter{},
		Model:     "test-model",
		Config:    config.DefaultSubagentConfig,
		SkillReg:  skillReg,
	})
	if err != nil {
		t.Fatalf("NewSessionDispatcher: %v", err)
	}
	defer d.Close()
	if !d.Has(runtime.Subagent, "opts-skill") {
		t.Fatal("SkillReg skill was not registered as a subagent")
	}
}
