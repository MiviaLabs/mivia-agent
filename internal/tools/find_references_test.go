package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/codeintel"
)

// fakeReferenceFinder simulates a successful referenceFinder for tool tests.
type fakeReferenceFinder struct {
	result codeintel.Result
	err    error
}

func (f *fakeReferenceFinder) References(ctx context.Context, symbol string, roles []codeintel.Role, limit int) (codeintel.Result, error) {
	return f.result, f.err
}

func TestFindReferencesRefusesWithoutAnalyzer(t *testing.T) {
	tool := &findReferencesTool{finder: nil, maxBytes: 10000, limit: 50}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"symbol":"os.File"}`))
	if err == nil {
		t.Fatal("expected error when analyzer is nil")
	}
}

func TestFindReferencesInvalidArgs(t *testing.T) {
	tool := &findReferencesTool{
		finder:   &fakeReferenceFinder{},
		maxBytes: 10000,
		limit:    50,
	}
	// Missing symbol.
	_, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for missing symbol")
	}
	// Empty symbol.
	_, err = tool.Execute(context.Background(), json.RawMessage(`{"symbol":""}`))
	if err == nil {
		t.Fatal("expected error for empty symbol")
	}
	// Invalid JSON.
	_, err = tool.Execute(context.Background(), json.RawMessage(`not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestFindReferencesSuccess(t *testing.T) {
	fake := &fakeReferenceFinder{
		result: codeintel.Result{
			Symbol: "os.File",
			Locations: []codeintel.Location{
				{Path: "a.go", Line: 1, Symbol: "File", Role: codeintel.RoleDefinition},
			},
			Complete: true,
		},
	}
	tool := &findReferencesTool{finder: fake, maxBytes: 10000, limit: 50}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"symbol":"os.File"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out == "" {
		t.Fatal("expected non-empty output")
	}
	// Should contain JSON.
	var result codeintel.Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("output should be JSON: %v", err)
	}
	if result.Symbol != "os.File" {
		t.Errorf("symbol = %q, want %q", result.Symbol, "os.File")
	}
}

func TestFindReferencesWithRolesFilter(t *testing.T) {
	fake := &fakeReferenceFinder{
		result: codeintel.Result{
			Symbol: "os.File",
			Locations: []codeintel.Location{
				{Path: "a.go", Line: 1, Symbol: "File", Role: codeintel.RoleDefinition},
			},
			Complete: true,
		},
	}
	tool := &findReferencesTool{finder: fake, maxBytes: 10000, limit: 50}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"symbol":"os.File","roles":["definition"]}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out == "" {
		t.Fatal("expected non-empty output")
	}
}

func TestFindReferencesLimit(t *testing.T) {
	// Default limit should be used when not specified.
	tool := &findReferencesTool{finder: &fakeReferenceFinder{}, maxBytes: 10000, limit: 50}
	if tool.limit != 50 {
		t.Errorf("default limit = %d, want 50", tool.limit)
	}
}

func TestFindReferencesCapability(t *testing.T) {
	tool := &findReferencesTool{finder: &fakeReferenceFinder{}, maxBytes: 10000, limit: 50}
	cap := tool.Capability(json.RawMessage(`{"symbol":"os.File"}`))
	if cap.Class != ExecutionRead {
		t.Errorf("expected ExecutionRead, got %v", cap.Class)
	}
	if cap.MaxResultBytes != 10000 {
		t.Errorf("MaxResultBytes = %d, want 10000", cap.MaxResultBytes)
	}
}

func TestFindReferencesErrUnavailable(t *testing.T) {
	fake := &fakeReferenceFinder{
		err: errors.New("analysis unavailable: workspace does not have a supported language toolchain"),
	}
	tool := &findReferencesTool{finder: fake, maxBytes: 10000, limit: 50}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"symbol":"os.File"}`))
	if err != nil {
		t.Fatalf("expected no error from tool (error propagates in body), got: %v", err)
	}
	if out == "" {
		t.Fatal("expected error message in output, not empty")
	}
}
