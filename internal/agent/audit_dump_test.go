package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkprovider "github.com/MiviaLabs/mivia-ai-sdk/provider"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
)

// completionRecord is one audited completion carrying every shape the dump
// is supposed to preserve: history with a tool-call turn, an advertised tool
// list, and a response that answered with a tool call.
func completionRecord() sdkagentloop.AuditRecord {
	return sdkagentloop.AuditRecord{
		Iteration: 2,
		Kind:      sdkagentloop.AuditKindCompletion,
		Request: sdkprovider.Request{
			Model:            "glm-5.3-flash",
			Stream:           true,
			ToolChoice:       "auto",
			ReasoningEffort:  "max",
			ReasoningDialect: "thinking_preserved",
			Tools: []sdkprovider.ToolDefinition{
				{Name: "read_file"}, {Name: "dispatch_tasks"},
			},
			Messages: []sdkprovider.Message{
				{Role: sdkprovider.RoleUser, Content: "continue"},
				{
					Role:             sdkprovider.RoleAssistant,
					ReasoningContent: "prior thinking",
					ToolCalls: []sdkprovider.ToolCall{
						{ID: "call_1", Name: "read_file", Arguments: []byte(`{"path":"go.mod"}`)},
					},
				},
				{Role: sdkprovider.RoleTool, ToolCallID: "call_1", Content: "module x"},
			},
		},
		Response: sdkprovider.Response{
			FinishReason: "tool_calls",
			Message: sdkprovider.Message{
				Role:             sdkprovider.RoleAssistant,
				Content:          "spawning agents",
				ReasoningContent: "fresh thinking",
			},
			ToolCalls: []sdkprovider.ToolCall{
				{ID: "call_2", Name: "dispatch_tasks", Arguments: []byte(`{"tasks":[]}`)},
			},
			Usage:      sdkprovider.Usage{PromptTokens: 39206, CompletionTokens: 0, TotalTokens: 39206},
			CacheUsage: sdkprovider.CacheUsage{Reported: true, CachedInputTokens: 10176, CacheWriteTokens: 7},
		},
	}
}

// readDumpLines returns the parsed JSONL lines written for a session.
func readDumpLines(t *testing.T, dir, sessionID string) []auditDumpEntry {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, auditDumpFileName(sessionID)))
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	var out []auditDumpEntry
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var entry auditDumpEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("decode dump line %q: %v", line, err)
		}
		out = append(out, entry)
	}
	return out
}

// TestProviderAuditDumpDisabledByDefault pins the cost of the seam when no
// operator asked for it: no hook at all, so the SDK never calls into this
// package.
func TestProviderAuditDumpDisabledByDefault(t *testing.T) {
	t.Setenv(EnvProviderAuditDir, "")
	if hook := newProviderAuditDump("S1"); hook != nil {
		t.Fatal("expected no audit hook when MIVIA_PROVIDER_AUDIT_DIR is unset")
	}
}

// TestProviderAuditDumpCapturesTheWire is the whole point of the file: the
// request this host built and the response the provider returned both have
// to be recoverable after the fact. The empty-completion case that motivated
// it (finish reason plus zero completion tokens) must survive as data.
func TestProviderAuditDumpCapturesTheWire(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	t.Setenv(EnvProviderAuditDir, dir)
	hook := newProviderAuditDump("S1")
	if hook == nil {
		t.Fatal("expected an audit hook")
	}
	if err := hook(context.Background(), completionRecord()); err != nil {
		t.Fatalf("hook returned an error: %v", err)
	}
	lines := readDumpLines(t, dir, "S1")
	if len(lines) != 1 {
		t.Fatalf("want 1 dump line, got %d", len(lines))
	}
	got := lines[0]
	if got.Iteration != 2 || got.SessionID != "S1" {
		t.Fatalf("iteration/session not preserved: %+v", got)
	}
	if got.Request.Model != "glm-5.3-flash" || got.Request.ReasoningDialect != "thinking_preserved" {
		t.Fatalf("request shape not preserved: %+v", got.Request)
	}
	if got.Request.ToolChoice != "auto" || len(got.Request.ToolNames) != 2 {
		t.Fatalf("advertised tools not preserved: %+v", got.Request)
	}
	if got.Request.MessageCount != 3 || len(got.Messages) != 3 {
		t.Fatalf("history not preserved: %+v", got.Messages)
	}
	if got.Messages[1].Reasoning != "prior thinking" || len(got.Messages[1].ToolCalls) != 1 {
		t.Fatalf("replayed assistant turn not preserved: %+v", got.Messages[1])
	}
	if got.Response.FinishReason != "tool_calls" || got.Response.Content != "spawning agents" {
		t.Fatalf("response not preserved: %+v", got.Response)
	}
	if len(got.Response.ToolCalls) != 1 || got.Response.ToolCalls[0].Name != "dispatch_tasks" {
		t.Fatalf("response tool calls not preserved: %+v", got.Response.ToolCalls)
	}
	if got.Usage.PromptTokens != 39206 || got.Usage.CompletionTokens != 0 {
		t.Fatalf("usage not preserved: %+v", got.Usage)
	}
	if got.Cache == nil || got.Cache.CachedInput != 10176 {
		t.Fatalf("cache usage not preserved: %+v", got.Cache)
	}
}

// TestProviderAuditDumpAppends pins that a second iteration does not
// overwrite the first: a turn is only readable as a sequence.
func TestProviderAuditDumpAppends(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvProviderAuditDir, dir)
	hook := newProviderAuditDump("S2")
	for i := 1; i <= 3; i++ {
		rec := completionRecord()
		rec.Iteration = i
		if err := hook(context.Background(), rec); err != nil {
			t.Fatalf("hook returned an error: %v", err)
		}
	}
	lines := readDumpLines(t, dir, "S2")
	if len(lines) != 3 {
		t.Fatalf("want 3 dump lines, got %d", len(lines))
	}
	for i, line := range lines {
		if line.Iteration != i+1 {
			t.Fatalf("line %d has iteration %d", i, line.Iteration)
		}
	}
}

// TestProviderAuditDumpSkipsToolCallRecords pins the completion-only scope:
// tool results already have their own persisted trace, and duplicating every
// tool body here would make the dump unreadable.
func TestProviderAuditDumpSkipsToolCallRecords(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvProviderAuditDir, dir)
	hook := newProviderAuditDump("S3")
	err := hook(context.Background(), sdkagentloop.AuditRecord{
		Iteration: 1,
		Kind:      sdkagentloop.AuditKindToolCall,
		ToolCall:  sdkprovider.ToolCall{ID: "call_1", Name: "read_file"},
	})
	if err != nil {
		t.Fatalf("hook returned an error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, auditDumpFileName("S3"))); !os.IsNotExist(err) {
		t.Fatalf("tool-call record should write no dump file, stat err = %v", err)
	}
}

// TestProviderAuditDumpRedacts pins that the process-wide redaction policy
// runs before anything reaches the file. A debugging aid that wrote secrets
// to disk would be worse than no aid at all.
func TestProviderAuditDumpRedacts(t *testing.T) {
	policy, err := redact.Compile([]string{`sk-[A-Za-z0-9]+`}, nil, "[redacted]")
	if err != nil {
		t.Fatalf("compile policy: %v", err)
	}
	previous := redact.Current()
	redact.SetPolicy(policy)
	t.Cleanup(func() { redact.SetPolicy(previous) })

	dir := t.TempDir()
	t.Setenv(EnvProviderAuditDir, dir)
	rec := completionRecord()
	rec.Response.Message.Content = "the key is sk-abc123DEF"
	rec.Request.Messages[0].Content = "use sk-abc123DEF please"
	if err := newProviderAuditDump("S4")(context.Background(), rec); err != nil {
		t.Fatalf("hook returned an error: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, auditDumpFileName("S4")))
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	if strings.Contains(string(raw), "sk-abc123DEF") {
		t.Fatalf("secret reached the dump file: %s", raw)
	}
	if !strings.Contains(string(raw), "[redacted]") {
		t.Fatalf("redaction placeholder missing: %s", raw)
	}
}

// TestProviderAuditDumpNeverFailsTheRun is the load-bearing safety property:
// the SDK turns an Audit error into a hard run failure, so an unwritable
// dump target must stay silent rather than kill the user's turn.
func TestProviderAuditDumpNeverFailsTheRun(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	t.Setenv(EnvProviderAuditDir, filepath.Join(blocked, "sub"))
	if err := newProviderAuditDump("S5")(context.Background(), completionRecord()); err != nil {
		t.Fatalf("audit hook must never return an error, got %v", err)
	}
}

// TestAuditDumpFileNameIsSafe pins that a session id can never steer the
// dump outside the operator's directory.
func TestAuditDumpFileNameIsSafe(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"SKH3AOD3", "SKH3AOD3.jsonl"},
		{"../../etc/passwd", "etc_passwd.jsonl"},
		{"", "session.jsonl"},
		{"///", "session.jsonl"},
	} {
		if got := auditDumpFileName(tc.in); got != tc.want {
			t.Fatalf("auditDumpFileName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestAuditDumpTextCaps pins the per-field bound so one pathological tool
// result cannot make the file unreadable.
func TestAuditDumpTextCaps(t *testing.T) {
	got := auditDumpText(strings.Repeat("a", auditDumpFieldCap+100))
	if len(got) <= auditDumpFieldCap {
		t.Fatal("capped value should keep the marker suffix")
	}
	if !strings.Contains(got, "truncated") {
		t.Fatalf("missing truncation marker: %q", got[len(got)-40:])
	}
}
