package cli

// Result-encoding and registration edges: a schema violation must always reach
// the model with a schema field, a multi_step schema status must survive into a
// delegate payload, and a spool/tool registration that cannot complete must
// fail loudly rather than half-install.

import (
	"fmt"
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
	tr := encodeOneDispatchResult(result, nil, 0)
	if tr.Reason != "schema_violation" {
		t.Fatalf("reason = %q, want schema_violation", tr.Reason)
	}
	if tr.Schema != "violation" {
		t.Fatalf("schema = %q, want violation", tr.Schema)
	}
}

func TestMergeOutputFieldsSurfacesTheSchemaStatus(t *testing.T) {
	payload := map[string]any{}
	mergeOutputFields(payload, []byte(`{"schema":"ok","result":{"a":1}}`), "", 0)
	if payload["schema"] != "ok" {
		t.Fatalf("schema = %v, want the envelope's status", payload["schema"])
	}

	// A payload that is not a schema envelope contributes no schema field.
	plain := map[string]any{}
	mergeOutputFields(plain, []byte(`{"result":{"a":1}}`), "", 0)
	if _, ok := plain["schema"]; ok {
		t.Fatalf("a plain envelope invented a schema field: %v", plain)
	}
	nonJSON := map[string]any{}
	mergeOutputFields(nonJSON, []byte("plain text"), "", 0)
	if _, ok := nonJSON["schema"]; ok {
		t.Fatalf("non-JSON output invented a schema field: %v", nonJSON)
	}
}
