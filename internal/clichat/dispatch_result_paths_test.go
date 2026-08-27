package clichat

// Result-encoding and registration edges: a schema violation must always reach
// the model with a schema field, a multi_step schema status must survive into a
// delegate payload, and a spool/tool registration that cannot complete must
// fail loudly rather than half-install.

import (
	"fmt"
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

func TestNewRemainderSpoolWithoutARepository(t *testing.T) {
	spool := newRemainderSpool(nil)
	if spool == nil {
		t.Fatal("newRemainderSpool(nil) returned no spool")
	}
	if ref := spool.Spool(t.Context(), "owner", []byte("body")); ref != "" {
		t.Fatalf("a storeless spool minted %q", ref)
	}
}

func TestRegisterLedgerToolsFailsWhenTheDispatcherAlreadyHasTheName(t *testing.T) {
	dispatcher := runtime.New(runtime.Policy{})
	// Occupy "ledger_read" on the dispatcher only, so the registry existence
	// check passes and the dispatcher registration is what fails.
	if err := dispatcher.RegisterTool(tools.NewRegistry(), &ledgerReadTool{}); err != nil {
		t.Fatal(err)
	}
	_, err := registerLedgerTools(dispatcher, tools.NewRegistry(), ledger.NewMemoryLedgerRepository(), 0, nil)
	if err == nil {
		t.Fatal("a dispatcher-level duplicate was accepted")
	}
	if want := "register execution history tool"; !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want it to name the failed registration", err)
	}
}

func TestEncodeDispatchResultAlwaysCarriesASchemaVerdict(t *testing.T) {
	result := subagents.Result{
		TaskID: "t1",
		Err:    fmt.Errorf("subagent: %w", subagents.ErrSchemaViolation),
	}
	tr := cliorchestrate.EncodeOneDispatchResult(result, nil, 0)
	if tr.Reason != "schema_violation" {
		t.Fatalf("reason = %q, want schema_violation", tr.Reason)
	}
	if tr.Schema != "violation" {
		t.Fatalf("schema = %q, want violation", tr.Schema)
	}
}
