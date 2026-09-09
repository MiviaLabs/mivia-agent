package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
	sdktrace "github.com/MiviaLabs/mivia-ai-sdk/trace"

	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
)

// TestRecordSDKTurnTelemetryReadsTracerAndUsage pins Pass 7 finding 3:
// the Tracer and Usage adoption rows must have a reader. This asserts
// recordSDKTurnTelemetry actually reads back what adoptSDKTracer and
// adoptSDKUsage parked on the turn state, rather than leaving both
// write-only.
func TestRecordSDKTurnTelemetryReadsTracerAndUsage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "aud")
	t.Setenv(EnvProviderAuditDir, dir)

	turn := &sdkTurnState{}
	tracer := sdktrace.New()
	_, span := tracer.Start(context.Background(), "chat")
	span.End()
	turn.setTracer(tracer)

	acc := sdkadapter.NewAccumulator()
	if err := acc.Record("S1", sdkshape.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	turn.setUsage(acc)

	recordSDKTurnTelemetry(Options{SessionID: "S1"}, turn)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var path string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "sdktelemetry-") {
			path = filepath.Join(dir, e.Name())
		}
	}
	if path == "" {
		t.Fatalf("expected an sdktelemetry- file in %s, got %v", dir, entries)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var payload map[string]any
	line := strings.TrimSpace(strings.Split(string(raw), "\n")[0])
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		t.Fatalf("Unmarshal %q: %v", line, err)
	}
	if payload["session_id"] != "S1" {
		t.Errorf("session_id = %v, want S1", payload["session_id"])
	}
	if n, _ := payload["span_count"].(float64); n != 1 {
		t.Errorf("span_count = %v, want 1", payload["span_count"])
	}
	if n, _ := payload["usage_total_tokens"].(float64); n != 15 {
		t.Errorf("usage_total_tokens = %v, want 15", payload["usage_total_tokens"])
	}
}

// TestRecordSDKTurnTelemetryNoAuditDirIsNoop confirms the reader stays
// silent (no file written, no panic) when the operator never named an
// audit directory - it must not become a mandatory side channel.
func TestRecordSDKTurnTelemetryNoAuditDirIsNoop(t *testing.T) {
	t.Setenv(EnvProviderAuditDir, "")
	turn := &sdkTurnState{}
	turn.setTracer(sdktrace.New())
	recordSDKTurnTelemetry(Options{SessionID: "S2"}, turn)
	// No assertion beyond "did not panic": there is no directory to
	// inspect, which is the point.
}

// TestRecordSDKTurnTelemetryNilTurnIsNoop confirms a nil turn state
// (a turn that never adopted Tracer/Usage) is handled safely.
func TestRecordSDKTurnTelemetryNilTurnIsNoop(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "aud")
	t.Setenv(EnvProviderAuditDir, dir)
	recordSDKTurnTelemetry(Options{SessionID: "S3"}, nil)
	entries, err := os.ReadDir(dir)
	if err == nil && len(entries) != 0 {
		t.Fatalf("expected no files written for a nil turn, got %v", entries)
	}
}
