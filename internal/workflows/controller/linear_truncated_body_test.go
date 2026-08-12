package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestPersistentlyTruncatedBodyRerunsStep pins the DC-8 contract end to end:
// a provider whose response body stays cut past its own per-call retry budget
// returns a TransientError (the call never delivered an answer), so the
// step-level retry (runStepWithTransientRetry) re-runs the step instead of
// failing the whole run on the first attempt's bare JSON syntax error.
func TestPersistentlyTruncatedBodyRerunsStep(t *testing.T) {
	orig := stepTransientRetryBackoff
	stepTransientRetryBackoff = func(int) time.Duration { return 0 }
	t.Cleanup(func() { stepTransientRetryBackoff = orig })

	// A provider that always truncates the body: every per-call retry is cut,
	// so Chat returns the exhausted-budget error a workflow step would see.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"mess`))
	}))
	defer srv.Close()
	client := provider.NewOpenAICompatWithOptions(provider.CompatOptions{Name: "trunc", BaseURL: srv.URL, APIKey: "k"})
	_, exhaustedErr := client.Chat(context.Background(), provider.Request{
		Model: "m", Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
	})
	if exhaustedErr == nil {
		t.Fatal("a persistently truncated body must exhaust the provider's per-call budget")
	}

	runner := &transientPromptRunner{failures: 100, err: exhaustedErr}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, linearWorkflow(t), map[string]StepRuntime{
		"first":  {Agent: agents.ResolvedAgent{Name: "one"}, Template: "task={{inputs.task}}"},
		"second": {Agent: agents.ResolvedAgent{Name: "two"}},
	}, map[string]any{"task": "build"}, "wfr-trunc-body", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ctrl.Run(context.Background()); err == nil {
		t.Fatal("run = nil error, want the persistent truncation to fail the run")
	}

	runner.mu.Lock()
	defer runner.mu.Unlock()
	firstCalls := 0
	for _, call := range runner.calls {
		if call.StepID == "first" {
			firstCalls++
		}
	}
	if firstCalls < 2 {
		t.Fatalf("first step ran %d times, want > 1: a persistently cut body must re-run the step via the transient retry", firstCalls)
	}
	if firstCalls != 1+defaultMaxTransientStepRetries {
		t.Fatalf("first step ran %d times, want %d (initial call + step-level transient retries)", firstCalls, 1+defaultMaxTransientStepRetries)
	}
}
