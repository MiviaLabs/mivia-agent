package coordinator

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

func TestCoverageHelpersResultsFromSnapshots(t *testing.T) {
	results := ResultsFromSnapshots([]ledger.TaskSnapshot{
		{TaskID: "completed", Status: string(ledger.TaskStatusCompleted)},
		{TaskID: "failed", Status: string(ledger.TaskStatusFailed), ErrorRef: "content:error-1"},
		{TaskID: "timed-out", Status: string(ledger.TaskStatusTimedOut)},
	})
	if len(results) != 3 {
		t.Fatalf("result count = %d, want 3", len(results))
	}
	if results[0].Err != nil {
		t.Fatalf("completed result error = %v", results[0].Err)
	}
	if results[0].Provenance.Kind != "recovered" {
		t.Fatalf("completed provenance = %#v", results[0].Provenance)
	}
	if results[1].Err == nil || !strings.Contains(results[1].Err.Error(), "content:error-1") {
		t.Fatalf("failed result error = %v, want error reference", results[1].Err)
	}
	if results[2].Err == nil || !strings.Contains(results[2].Err.Error(), "no error content reference") {
		t.Fatalf("timed-out result error = %v, want missing-reference detail", results[2].Err)
	}
}

func TestCoverageHelpersDoneChannels(t *testing.T) {
	retry := NewRetryState("task", NoRetry)
	select {
	case <-retry.Done():
		t.Fatal("retry Done closed before Exhausted")
	default:
	}
	retry.Exhausted()
	select {
	case <-retry.Done():
	default:
		t.Fatal("retry Done did not close after Exhausted")
	}

	done := make(chan struct{})
	handle := &RunHandle{done: done}
	if handle.Done() != done {
		t.Fatal("RunHandle.Done did not return the handle channel")
	}
}
