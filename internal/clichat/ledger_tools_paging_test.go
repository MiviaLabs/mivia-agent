package clichat

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

type ledgerReadPage struct {
	Status        string `json:"status"`
	Bytes         int    `json:"bytes"`
	Offset        int    `json:"offset"`
	Limit         int    `json:"limit"`
	ReturnedBytes int    `json:"returned_bytes"`
	NextOffset    *int   `json:"next_offset"`
	HasMore       bool   `json:"has_more"`
	Truncated     bool   `json:"truncated"`
	ContentIsData bool   `json:"content_is_data"`
	Note          string `json:"note"`
	Content       string `json:"content"`
}

func readLedgerPage(t *testing.T, tool *ledgerReadTool, ref string, offset, limit int) (ledgerReadPage, string) {
	t.Helper()
	args, err := json.Marshal(map[string]any{"ref": ref, "offset": offset, "limit": limit})
	if err != nil {
		t.Fatal(err)
	}
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	var page ledgerReadPage
	if err := json.Unmarshal([]byte(out), &page); err != nil {
		t.Fatalf("unmarshal %s: %v", out, err)
	}
	return page, out
}

func TestLedgerReadPagesRedactedUTF8Content(t *testing.T) {
	policy, err := redact.Compile([]string{`secret-[A-Z]{6}`}, nil, "[redacted]")
	if err != nil {
		t.Fatal(err)
	}
	previous := redact.Current()
	redact.SetPolicy(policy)
	t.Cleanup(func() { redact.SetPolicy(previous) })

	const source = "α secret-ABCDEF ω"
	const redacted = "α [redacted] ω"
	repo := ledger.NewMemoryLedgerRepository()
	ref := storeLedgerContent(t, repo, []byte(source))
	tool := &ledgerReadTool{repo: repo}

	var rebuilt string
	offset := 0
	for pageNo := 0; ; pageNo++ {
		page, _ := readLedgerPage(t, tool, ref, offset, 5)
		if page.Status != "ok" {
			t.Fatalf("page %d status = %q", pageNo, page.Status)
		}
		if page.Offset != offset || page.Limit != 5 || page.ReturnedBytes != len(page.Content) {
			t.Fatalf("page %d metadata = %+v", pageNo, page)
		}
		if !page.ContentIsData || page.Note != contentIsDataNote {
			t.Fatalf("page %d lost untrusted-data framing: %+v", pageNo, page)
		}
		if strings.Contains(page.Content, "secret-") {
			t.Fatalf("page %d leaked a secret fragment: %q", pageNo, page.Content)
		}
		rebuilt += page.Content
		if !page.HasMore {
			if page.NextOffset != nil || page.Truncated {
				t.Fatalf("final page has continuation metadata: %+v", page)
			}
			break
		}
		if !page.Truncated || page.NextOffset == nil || *page.NextOffset <= offset {
			t.Fatalf("page %d has invalid continuation metadata: %+v", pageNo, page)
		}
		offset = *page.NextOffset
		if pageNo > len(redacted) {
			t.Fatal("pagination did not terminate")
		}
	}
	if rebuilt != redacted {
		t.Fatalf("rebuilt redacted stream = %q, want %q", rebuilt, redacted)
	}
}

func TestLedgerReadRejectsInvalidPagingArguments(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	ref := storeLedgerContent(t, repo, []byte("recorded output"))
	tool := &ledgerReadTool{repo: repo}

	tests := []string{
		`{"ref":"` + ref + `","offset":-1}`,
		`{"ref":"` + ref + `","limit":0}`,
		`{"ref":"` + ref + `","limit":1}`,
		`{"ref":"` + ref + `","offset":1.5}`,
		`{"ref":"` + ref + `","limit":1e999}`,
		`{"ref":"` + ref + `","offset":100}`,
		`{"ref":"` + ref + `","unknown":true}`,
		`{"ref":"` + ref + `","offset":0,"offset":1}`,
		`{"ref":"` + ref + `","limit":1,"limit":2}`,
	}
	for _, args := range tests {
		t.Run(args, func(t *testing.T) {
			if _, err := tool.Execute(context.Background(), json.RawMessage(args)); err == nil {
				t.Fatalf("invalid paging arguments were accepted: %s", args)
			}
		})
	}
}

func TestLedgerReadRejectsOffsetInsideUTF8Rune(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	ref := storeLedgerContent(t, repo, []byte("α output"))
	tool := &ledgerReadTool{repo: repo}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"ref":"`+ref+`","offset":1}`)); err == nil {
		t.Fatal("offset inside a UTF-8 rune was accepted")
	}
}

func TestLedgerReadPagingKeepsWholeEnvelopeUnderCapabilityCap(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	ref := storeLedgerContent(t, repo, []byte(strings.Repeat("\x00", 8192)))
	tool := &ledgerReadTool{repo: repo, maxBytes: 4096}

	page, out := readLedgerPage(t, tool, ref, 0, 4096)
	if page.Status != "ok" || !page.HasMore || page.NextOffset == nil {
		t.Fatalf("expected a cap-fitted continuation page, got %+v", page)
	}
	if cap := tool.Capability(nil).MaxResultBytes; cap <= 0 || len(out) > cap {
		t.Fatalf("response size %d exceeds declared capability cap %d", len(out), cap)
	}
}

func TestLedgerReadPageFieldsPrecedeContent(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	ref := storeLedgerContent(t, repo, []byte(strings.Repeat("x", 64)))
	_, out := readLedgerPage(t, &ledgerReadTool{repo: repo}, ref, 0, 4)

	contentAt := strings.Index(out, `"content"`)
	if contentAt < 0 {
		t.Fatalf("content missing from %s", out)
	}
	for _, key := range []string{"status", "ref", "kind", "bytes", "offset", "limit", "returned_bytes", "next_offset", "has_more", "truncated", "content_is_data", "note"} {
		if at := strings.Index(out, `"`+key+`"`); at < 0 || at > contentAt {
			t.Fatalf("%q must precede content in %s", key, out)
		}
	}
}

type ledgerReadCapCompleter struct {
	ref        string
	args       string
	calls      int
	toolResult string
}

func (c *ledgerReadCapCompleter) Name() string { return "ledger-read-cap" }
func (c *ledgerReadCapCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	r, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return r.Content, nil
}
func (c *ledgerReadCapCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return c.Chat(ctx, req)
}
func (c *ledgerReadCapCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.calls++
	if c.calls == 1 {
		var call provider.ToolCall
		call.ID = "ledger-read-cap"
		call.Type = "function"
		call.Function.Name = "ledger_read"
		call.Function.Arguments = c.args
		if call.Function.Arguments == "" {
			call.Function.Arguments = `{"ref":"` + c.ref + `","limit":4096}`
		}
		return &provider.Response{ToolCalls: []provider.ToolCall{call}, FinishReason: "tool_calls"}, nil
	}
	for _, message := range req.Messages {
		if message.Role == provider.RoleTool {
			c.toolResult = message.Content
		}
	}
	return &provider.Response{Content: "done", FinishReason: "stop"}, nil
}

func TestLedgerReadInvalidArgumentsDoNotEchoUntrustedFieldNames(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	ref := storeLedgerContent(t, repo, []byte("recorded output"))
	field := strings.Repeat("injected-", 4<<10)
	comp := &ledgerReadCapCompleter{ref: ref, args: `{"ref":"` + ref + `","` + field + `":true}`}
	reg := tools.NewRegistry()
	dispatcher, err := NewSessionDispatcher(SessionDispatcherOpts{
		Registry:           reg,
		Completer:          comp,
		Model:              "test-model",
		Config:             config.SubagentConfig{DefaultTimeout: 60},
		Repo:               repo,
		ToolResultCapBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dispatcher.Close)

	result := dispatcher.Invoke(context.Background(), runtime.Request{
		ID: "ledger-read-invalid-arguments", Kind: runtime.Subagent, Name: "multi_step",
		Input: json.RawMessage(`"read the recorded output"`),
	})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if !json.Valid([]byte(comp.toolResult)) || len(comp.toolResult) > 1024 {
		t.Fatalf("invalid argument result escaped the loop cap: %d bytes", len(comp.toolResult))
	}
	if strings.Contains(comp.toolResult, field) {
		t.Fatal("invalid argument result reflected the untrusted field name")
	}
}

func TestLedgerReadPageIsNotTailCutByTheAgentLoop(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	ref := storeLedgerContent(t, repo, []byte(strings.Repeat("\x00", 4096)))
	comp := &ledgerReadCapCompleter{ref: ref}
	reg := tools.NewRegistry()
	dispatcher, err := NewSessionDispatcher(SessionDispatcherOpts{
		Registry:           reg,
		Completer:          comp,
		Model:              "test-model",
		Config:             config.SubagentConfig{DefaultTimeout: 60},
		Repo:               repo,
		ToolResultCapBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dispatcher.Close)

	result := dispatcher.Invoke(context.Background(), runtime.Request{
		ID: "ledger-read-cap", Kind: runtime.Subagent, Name: "multi_step",
		Input: json.RawMessage(`"read the recorded output"`),
	})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if !json.Valid([]byte(comp.toolResult)) {
		t.Fatalf("agent loop tail-cut ledger_read into invalid JSON: %q", comp.toolResult)
	}
	if len(comp.toolResult) > 1024 {
		t.Fatalf("agent loop received %d bytes, exceeds its 1024-byte cap", len(comp.toolResult))
	}
	var page ledgerReadPage
	if err := json.Unmarshal([]byte(comp.toolResult), &page); err != nil {
		t.Fatal(err)
	}
	if !page.ContentIsData || page.Note != contentIsDataNote || !page.HasMore || page.NextOffset == nil {
		t.Fatalf("cap-fitted page lost its framing or continuation: %+v", page)
	}
}
