package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
)

// completionRequest and completionResponse are one completed call carrying
// every shape the dump must preserve: the EFFECTIVE request (reasoning dial,
// max_tokens, tool_choice - the fields the SDK's own request leaves empty),
// history with a tool-call turn, and a response that answered with a tool
// call.
func completionRequest() provider.Request {
	temp := 0.0
	maxTokens := 131072
	call := provider.ToolCall{ID: "call_1", Type: "function"}
	call.Function.Name = "read_file"
	call.Function.Arguments = `{"path":"go.mod"}`
	return provider.Request{
		Model:            "glm-5.3-flash",
		Stream:           true,
		Temperature:      &temp,
		MaxTokens:        &maxTokens,
		ToolChoice:       "auto",
		Timeout:          900 * time.Second,
		ReasoningLevel:   reasoning.Max,
		ReasoningDialect: reasoning.DialectThinkingPreserved,
		Tools: []provider.ToolSpec{
			{"type": "function", "function": map[string]any{"name": "read_file"}},
			{"type": "function", "function": map[string]any{"name": "dispatch_tasks"}},
		},
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "continue"},
			{Role: provider.RoleAssistant, ReasoningContent: "prior thinking", ToolCalls: []provider.ToolCall{call}},
			{Role: provider.RoleTool, ToolCallID: "call_1", Content: "module x"},
		},
	}
}

func completionResponse() *provider.Response {
	call := provider.ToolCall{ID: "call_2", Type: "function"}
	call.Function.Name = "dispatch_tasks"
	call.Function.Arguments = `{"tasks":[]}`
	return &provider.Response{
		Content:          "spawning agents",
		ReasoningContent: "fresh thinking",
		FinishReason:     "tool_calls",
		ToolCalls:        []provider.ToolCall{call},
		TokenUsage:       provider.TokenUsage{Reported: true, InputTokens: 39206, OutputTokens: 0},
		CacheUsage:       provider.CacheUsage{Reported: true, CachedInputTokens: 10176, CacheWriteTokens: 7},
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
	hook(completionRequest(), completionResponse(), 2)
	lines := readDumpLines(t, dir, "S1")
	if len(lines) != 1 {
		t.Fatalf("want 1 dump line, got %d", len(lines))
	}
	got := lines[0]
	if got.Iteration != 2 || got.SessionID != "S1" {
		t.Fatalf("iteration/session not preserved: %+v", got)
	}
	// These four are the fields the SDK's own request leaves empty and the
	// whole reason the dump captures the effective request instead.
	if got.Request.Model != "glm-5.3-flash" || got.Request.ReasoningDialect != "thinking_preserved" {
		t.Fatalf("request shape not preserved: %+v", got.Request)
	}
	if got.Request.ReasoningLevel != "max" {
		t.Fatalf("reasoning level not preserved: %+v", got.Request)
	}
	if got.Request.MaxTokens == nil || *got.Request.MaxTokens != 131072 {
		t.Fatalf("max_tokens not preserved: %+v", got.Request)
	}
	if got.Request.TimeoutSeconds != 900 {
		t.Fatalf("request deadline not preserved: %+v", got.Request)
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
	if !got.Usage.Reported || got.Usage.InputTokens != 39206 || got.Usage.OutputTokens != 0 {
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
		hook(completionRequest(), completionResponse(), i)
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

// TestProviderAuditDumpIgnoresNilResponse pins the guard on the one input
// the seam can legitimately hand it: an observation with no response.
func TestProviderAuditDumpIgnoresNilResponse(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvProviderAuditDir, dir)
	newProviderAuditDump("S3")(completionRequest(), nil, 1)
	if _, err := os.Stat(filepath.Join(dir, auditDumpFileName("S3"))); !os.IsNotExist(err) {
		t.Fatalf("a nil response should write no dump file, stat err = %v", err)
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
	req := completionRequest()
	req.Messages[0].Content = "use sk-abc123DEF please"
	resp := completionResponse()
	resp.Content = "the key is sk-abc123DEF"
	newProviderAuditDump("S4")(req, resp, 2)
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
	auditDumpDisabled.Store(false)
	t.Cleanup(func() { auditDumpDisabled.Store(false) })
	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	t.Setenv(EnvProviderAuditDir, filepath.Join(blocked, "sub"))
	hook := newProviderAuditDump("S5")
	for i := 0; i < 3; i++ {
		hook(completionRequest(), completionResponse(), 2)
	}
	// The failure latches: the log line claims the dump is disabled, so it
	// must really be, not merely quiet while retrying mkdir+open on every
	// iteration of every turn for the rest of the process.
	if !auditDumpDisabled.Load() {
		t.Fatal("a failed dump target must latch off")
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
// result cannot make the file unreadable: the head is kept verbatim, the
// tail past the cap is gone, and the marker says so.
func TestAuditDumpTextCaps(t *testing.T) {
	head := strings.Repeat("a", auditDumpFieldCap)
	got := auditDumpText(head + strings.Repeat("z", 100))
	if !strings.HasPrefix(got, head) {
		t.Fatal("the kept head must be the input's own first auditDumpFieldCap bytes")
	}
	if strings.Contains(got, "zz") {
		t.Fatal("content past the cap must be dropped, not kept")
	}
	if !strings.Contains(got, "truncated") {
		t.Fatalf("missing truncation marker: %q", got[len(got)-40:])
	}
	if short := auditDumpText("small"); short != "small" {
		t.Fatalf("an under-cap value must pass through unchanged, got %q", short)
	}
}

// TestProviderAuditDumpModes pins the file and directory permissions the
// package doc and docs/product/config.md both promise. Nothing else asserts
// them, so a change from 0600 to 0644 would otherwise ship silently.
func TestProviderAuditDumpModes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dump")
	t.Setenv(EnvProviderAuditDir, dir)
	newProviderAuditDump("S6")(completionRequest(), completionResponse(), 2)
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("dump directory mode = %o, want 0700", got)
	}
	fileInfo, err := os.Stat(filepath.Join(dir, auditDumpFileName("S6")))
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("dump file mode = %o, want 0600", got)
	}
}

// TestProviderAuditDumpRefusesSymlink is the negative test for the write
// path: an operator who points the variable at a shared directory must not
// have a turn's prompts appended to a file someone else chose.
func TestProviderAuditDumpRefusesSymlink(t *testing.T) {
	auditDumpDisabled.Store(false)
	t.Cleanup(func() { auditDumpDisabled.Store(false) })
	dir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim.txt")
	if err := os.WriteFile(victim, []byte("original\n"), 0o600); err != nil {
		t.Fatalf("seed victim: %v", err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, auditDumpFileName("S7"))); err != nil {
		t.Fatalf("plant symlink: %v", err)
	}
	t.Setenv(EnvProviderAuditDir, dir)
	newProviderAuditDump("S7")(completionRequest(), completionResponse(), 2)
	raw, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("read victim: %v", err)
	}
	if string(raw) != "original\n" {
		t.Fatalf("wrote through a symlink; victim now holds %q", raw)
	}
}

// TestProviderAuditDumpRedactsBeforeCapping pins the ordering claim: a
// secret that straddles the cap must not leave its head in the file, which
// is exactly what a cap-then-redact implementation would do.
func TestProviderAuditDumpRedactsBeforeCapping(t *testing.T) {
	policy, err := redact.Compile([]string{`sk-[A-Za-z0-9]+`}, nil, "[redacted]")
	if err != nil {
		t.Fatalf("compile policy: %v", err)
	}
	previous := redact.Current()
	redact.SetPolicy(policy)
	t.Cleanup(func() { redact.SetPolicy(previous) })

	got := auditDumpText(strings.Repeat("a", auditDumpFieldCap-5) + "sk-abc123DEF")
	if strings.Contains(got, "sk-ab") {
		t.Fatal("a secret straddling the cap left its head in the output")
	}
	// The redacted form is itself longer than the raw prefix, so it caps -
	// which is fine. What must never appear is any part of the secret.
	if !strings.Contains(got, "[red") {
		t.Fatalf("the straddling secret was not redacted at all: %q", got[len(got)-40:])
	}
}

// TestProviderAuditDumpRedactsToolArgumentKeys pins that the key-name half
// of the redaction policy applies to tool arguments. Only redact.JSONValue
// honours key elision; redact.Text alone would write the value verbatim.
func TestProviderAuditDumpRedactsToolArgumentKeys(t *testing.T) {
	policy, err := redact.Compile(nil, []string{"api_token"}, "[redacted]")
	if err != nil {
		t.Fatalf("compile policy: %v", err)
	}
	previous := redact.Current()
	redact.SetPolicy(policy)
	t.Cleanup(func() { redact.SetPolicy(previous) })

	got := auditDumpArguments([]byte(`{"api_token":"ghp_livevalue","path":"go.mod"}`))
	if strings.Contains(got, "ghp_livevalue") {
		t.Fatalf("key-elided argument reached the dump: %s", got)
	}
	if !strings.Contains(got, "go.mod") {
		t.Fatalf("non-secret arguments must survive: %s", got)
	}
	if raw := auditDumpArguments([]byte("not json")); raw != "not json" {
		t.Fatalf("a non-JSON fragment must still be captured, got %q", raw)
	}
}

// TestProviderAuditDumpConcurrentAppends pins the reason auditDumpMu
// exists: concurrent turns and sessions must not interleave a partial line.
func TestProviderAuditDumpConcurrentAppends(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvProviderAuditDir, dir)
	hook := newProviderAuditDump("S8")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hook(completionRequest(), completionResponse(), 2)
		}()
	}
	wg.Wait()
	if lines := readDumpLines(t, dir, "S8"); len(lines) != 20 {
		t.Fatalf("want 20 well-formed lines, got %d", len(lines))
	}
}
