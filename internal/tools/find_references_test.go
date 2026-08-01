package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

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
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"symbol":"os.File"}`))
	if err != nil {
		t.Fatalf("expected no error (error goes in output body), got: %v", err)
	}
	if out == "" {
		t.Fatal("expected output body with error message")
	}
	if !strings.Contains(out, "no analyzer available") {
		t.Errorf("expected 'no analyzer available' in output, got: %s", out)
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

// TestFindReferencesBudgetOnOversizedErrorSymbol confirms the fix for the bug
// audit finding that the error path did no budget check at all - a caller
// could request a huge symbol string that gets echoed verbatim into the
// error text, blowing past MaxResultBytes entirely.
func TestFindReferencesBudgetOnOversizedErrorSymbol(t *testing.T) {
	hugeSymbol := strings.Repeat("x", 500_000)
	fake := &fakeReferenceFinder{err: fmt.Errorf("symbol %q not found in workspace packages", hugeSymbol)}
	tool := &findReferencesTool{finder: fake, maxBytes: 1000, limit: 50}

	in, err := json.Marshal(map[string]string{"symbol": hugeSymbol})
	if err != nil {
		t.Fatal(err)
	}
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(out) > 1000 {
		t.Errorf("output len = %d, want <= 1000 (maxBytes); budget was not enforced on the error path", len(out))
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("output must be valid JSON even when budget-truncated: %v\noutput: %s", err, out)
	}
}

// TestFindReferencesBudgetOnOversizedSuccessSymbol confirms the fix for the
// bug audit finding that the success-path truncation loop gave up once
// Locations was empty, without re-checking the budget against an oversized
// Symbol field.
func TestFindReferencesBudgetOnOversizedSuccessSymbol(t *testing.T) {
	fake := &fakeReferenceFinder{
		result: codeintel.Result{
			Symbol:   strings.Repeat("y", 500_000),
			Complete: true,
		},
	}
	tool := &findReferencesTool{finder: fake, maxBytes: 1000, limit: 50}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"symbol":"q"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(out) > 1000 {
		t.Errorf("output len = %d, want <= 1000 (maxBytes); loop exited without meeting the budget", len(out))
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("output must be valid JSON even when budget-truncated: %v\noutput: %s", err, out)
	}
}

// TestFindReferencesBudgetConvergesWithManyLocations exercises the ordinary
// degrade-gracefully path: enough locations that dropping them one at a time
// must actually bring the payload under budget.
func TestFindReferencesBudgetConvergesWithManyLocations(t *testing.T) {
	// Large enough to make an O(n^2) truncation loop (re-marshaling the whole
	// remaining slice on every single dropped location) prohibitively slow -
	// this regressed to 73s at n=10000 before marshalBudgeted switched to a
	// binary search over the kept-prefix length (O(log n) marshals). A
	// generous per-test timeout would hide the regression; asserting real
	// wall-clock time here is the point of this test.
	const total = 10000
	locs := make([]codeintel.Location, 0, total)
	for i := 0; i < total; i++ {
		locs = append(locs, codeintel.Location{Path: fmt.Sprintf("file%d.go", i), Line: i, Symbol: "X", Role: codeintel.RoleCaller})
	}
	fake := &fakeReferenceFinder{result: codeintel.Result{Symbol: "X", Locations: locs, Complete: true}}
	tool := &findReferencesTool{finder: fake, maxBytes: 2000, limit: total}

	start := time.Now()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"symbol":"X"}`))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("Execute took %v for %d locations; the truncation loop is not converging in O(log n) marshals", elapsed, total)
	}
	if len(out) > 2000 {
		t.Errorf("output len = %d, want <= 2000 (maxBytes)", len(out))
	}
	var result codeintel.Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("output must be valid JSON: %v\noutput: %s", err, out)
	}
	if !result.Truncated {
		t.Error("expected Truncated=true when locations had to be dropped to fit the budget")
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
