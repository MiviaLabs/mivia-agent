package agent

// Tests for the SDK-loop audit sink in audit_dump.go.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// decodeOneDumpLine reads the single sdkloop line written for a
// session and returns it as a map.
func decodeOneDumpLine(t *testing.T, dir, sessionID string) map[string]any {
	t.Helper()
	lines := readDumpLinesRaw(t, dir, "sdkloop-", sessionID)
	if len(lines) != 1 {
		t.Fatalf("want 1 dump line, got %d", len(lines))
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &out); err != nil {
		t.Fatalf("decode dump line: %v", err)
	}
	return out
}

// TestSDKLoopAuditDumpTotalTokens pins the reported-total field: a
// provider that reports a total must have it recorded, and a provider
// that reports none must not gain a zero that reads as a real total.
func TestSDKLoopAuditDumpTotalTokens(t *testing.T) {
	record := func(total int) sdkagentloop.AuditRecord {
		return sdkagentloop.AuditRecord{
			Kind:      sdkagentloop.AuditKindCompletion,
			Iteration: 1,
			Request:   sdkshape.Request{Model: "m1"},
			Response: sdkshape.Response{
				FinishReason: "stop",
				Usage:        sdkshape.Usage{PromptTokens: 3, CompletionTokens: 4, TotalTokens: total},
			},
		}
	}

	reported := filepath.Join(t.TempDir(), "aud")
	t.Setenv(EnvProviderAuditDir, reported)
	newSDKLoopAuditDump("T1")(record(7))
	line := decodeOneDumpLine(t, reported, "T1")
	if got, ok := line["total_tokens"]; !ok || got != float64(7) {
		t.Fatalf("total_tokens = %v (present %v), want 7", got, ok)
	}

	absent := filepath.Join(t.TempDir(), "aud")
	t.Setenv(EnvProviderAuditDir, absent)
	newSDKLoopAuditDump("T2")(record(0))
	line = decodeOneDumpLine(t, absent, "T2")
	if _, ok := line["total_tokens"]; ok {
		t.Fatalf("an unreported total must be omitted, got %v", line["total_tokens"])
	}
}

// TestSDKLoopAuditDumpUnknownKindFallsBackToCompletion pins the
// switch default: a record kind this host does not know must still be
// written with the completion projection, not dropped.
func TestSDKLoopAuditDumpUnknownKindFallsBackToCompletion(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "aud")
	t.Setenv(EnvProviderAuditDir, dir)
	newSDKLoopAuditDump("T3")(sdkagentloop.AuditRecord{
		Kind:      sdkagentloop.AuditKind("future_kind"),
		Iteration: 5,
		Request:   sdkshape.Request{Model: "m9"},
		Response:  sdkshape.Response{FinishReason: "length"},
	})
	line := decodeOneDumpLine(t, dir, "T3")
	if line["kind"] != "future_kind" {
		t.Fatalf("kind not preserved: %v", line["kind"])
	}
	if line["model"] != "m9" || line["finish_reason"] != "length" {
		t.Fatalf("unknown kind lost the completion facts: %v", line)
	}
	if line["iteration"] != float64(5) {
		t.Fatalf("iteration not preserved: %v", line["iteration"])
	}
}

// TestSDKLoopAuditDumpHonorsTheDisabledLatch pins the process-wide
// latch: once a dump target failed, the sink must write nothing more.
// The SDK turns an Audit error into a hard run failure, so a retrying
// sink would keep paying for a target already known to be broken.
func TestSDKLoopAuditDumpHonorsTheDisabledLatch(t *testing.T) {
	auditDumpDisabled.Store(true)
	t.Cleanup(func() { auditDumpDisabled.Store(false) })
	dir := t.TempDir()
	t.Setenv(EnvProviderAuditDir, dir)
	newSDKLoopAuditDump("T4")(sdkagentloop.AuditRecord{
		Kind:    sdkagentloop.AuditKindCompletion,
		Request: sdkshape.Request{Model: "m1"},
	})
	path := filepath.Join(dir, "sdkloop-"+auditDumpFileName("T4"))
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a latched-off dump must write no file, stat err = %v", err)
	}
}

// TestSDKLoopAuditDumpLatchesOnWriteFailure pins the failure half:
// an unwritable target must latch the sink off for the process rather
// than retrying mkdir and open on every iteration of every turn.
func TestSDKLoopAuditDumpLatchesOnWriteFailure(t *testing.T) {
	auditDumpDisabled.Store(false)
	t.Cleanup(func() { auditDumpDisabled.Store(false) })
	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	t.Setenv(EnvProviderAuditDir, filepath.Join(blocked, "sub"))
	dump := newSDKLoopAuditDump("T5")
	dump(sdkagentloop.AuditRecord{
		Kind:    sdkagentloop.AuditKindCompletion,
		Request: sdkshape.Request{Model: "m1"},
	})
	if !auditDumpDisabled.Load() {
		t.Fatal("a failed dump target must latch the sink off")
	}
}
