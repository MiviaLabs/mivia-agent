// Package agent - operator-invoked provider wire dump.
//
// The SDK loop already offers an Audit seam that receives the exact
// provider.Request it sent and the provider.Response it got back, per
// iteration. Nothing wired it, so a turn that came back empty left no
// evidence at all: no way to tell a model that answered with nothing from a
// request this host built wrong. This file wires that seam to an
// operator-enabled, append-only JSONL file.

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkprovider "github.com/MiviaLabs/mivia-ai-sdk/provider"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
)

// EnvProviderAuditDir names a directory that receives one JSONL file per
// session with the provider request and response of every agent-loop
// iteration. Unset (the default) wires no audit hook at all, so the seam
// costs nothing when it is off.
//
// The dump is a debugging aid for the operator's own machine, not a durable
// record: it holds prompts and model output, so it is written 0600 into a
// directory the operator names, never into the workspace by default, and
// every captured string passes through the process-wide redaction policy
// first.
const EnvProviderAuditDir = "MIVIA_PROVIDER_AUDIT_DIR"

// auditDumpFieldCap bounds one captured string. A dump exists to show what
// went over the wire, so the cap is generous; it only stops a single
// pathological tool result from making the file unreadable.
const auditDumpFieldCap = 32 * 1024

// auditDumpMu serializes appends. Several turns (and several sessions) can
// write concurrently, and an interleaved partial line would defeat the
// purpose of the file.
var auditDumpMu sync.Mutex

// auditDumpFailedOnce keeps a broken dump target from printing one line per
// iteration for the rest of the process.
var auditDumpFailedOnce sync.Once

// newProviderAuditDump returns the Audit hook for one run, or nil when
// EnvProviderAuditDir is unset.
//
// The returned function ALWAYS returns nil. The SDK treats an Audit error as
// a hard run failure (agentloop/run.go: audit error -> hardFail), so a
// debugging aid that could not write its file must never take the turn down
// with it.
func newProviderAuditDump(sessionID string) sdkagentloop.AuditFunc {
	dir := strings.TrimSpace(os.Getenv(EnvProviderAuditDir))
	if dir == "" {
		return nil
	}
	path := filepath.Join(dir, auditDumpFileName(sessionID))
	return func(_ context.Context, rec sdkagentloop.AuditRecord) error {
		if rec.Kind != sdkagentloop.AuditKindCompletion {
			return nil
		}
		if err := appendAuditDump(dir, path, auditDumpRecord(sessionID, rec)); err != nil {
			auditDumpFailedOnce.Do(func() {
				log.Printf("agent: provider audit dump disabled for this process: %v", err)
			})
		}
		return nil
	}
}

// auditDumpFileName derives a safe file name from a session id. A session id
// is host-minted, but it reaches this code as a string, so anything that is
// not an ordinary identifier character is replaced rather than trusted to
// stay inside the operator's directory.
func auditDumpFileName(sessionID string) string {
	safe := make([]rune, 0, len(sessionID))
	for _, r := range sessionID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			safe = append(safe, r)
		default:
			safe = append(safe, '_')
		}
	}
	name := strings.Trim(string(safe), "_")
	if name == "" {
		name = "session"
	}
	return name + ".jsonl"
}

// appendAuditDump writes one record as a single line. The directory is
// created on demand so an operator only has to name a path.
func appendAuditDump(dir, path string, rec auditDumpEntry) error {
	raw, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal audit record: %w", err)
	}
	auditDumpMu.Lock()
	defer auditDumpMu.Unlock()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create audit dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open audit file: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(raw, '\n')); err != nil {
		return fmt.Errorf("write audit file: %w", err)
	}
	return nil
}

// auditDumpEntry is one line of the dump.
type auditDumpEntry struct {
	SessionID string             `json:"session_id,omitempty"`
	Iteration int                `json:"iteration"`
	Request   auditDumpRequest   `json:"request"`
	Response  auditDumpResponse  `json:"response"`
	Usage     auditDumpUsage     `json:"usage"`
	Cache     *auditDumpCache    `json:"cache,omitempty"`
	Messages  []auditDumpMessage `json:"messages"`
}

type auditDumpRequest struct {
	Model            string   `json:"model"`
	Stream           bool     `json:"stream"`
	Temperature      *float64 `json:"temperature,omitempty"`
	MaxTokens        *int     `json:"max_tokens,omitempty"`
	ReasoningLevel   string   `json:"reasoning_level,omitempty"`
	ReasoningDialect string   `json:"reasoning_dialect,omitempty"`
	ToolChoice       string   `json:"tool_choice,omitempty"`
	ToolNames        []string `json:"tool_names,omitempty"`
	MessageCount     int      `json:"message_count"`
}

type auditDumpResponse struct {
	FinishReason string              `json:"finish_reason"`
	Content      string              `json:"content"`
	Reasoning    string              `json:"reasoning_content,omitempty"`
	ToolCalls    []auditDumpToolCall `json:"tool_calls,omitempty"`
}

type auditDumpUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	CachedTokens     int `json:"cached_tokens"`
}

type auditDumpCache struct {
	CachedInput int `json:"cached_input_tokens"`
	CacheWrite  int `json:"cache_write_tokens"`
}

type auditDumpMessage struct {
	Role       string              `json:"role"`
	Content    string              `json:"content,omitempty"`
	Reasoning  string              `json:"reasoning_content,omitempty"`
	ToolCallID string              `json:"tool_call_id,omitempty"`
	Name       string              `json:"name,omitempty"`
	ToolCalls  []auditDumpToolCall `json:"tool_calls,omitempty"`
}

type auditDumpToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments,omitempty"`
}

// auditDumpRecord projects one SDK audit record onto the dump shape,
// redacting every captured string.
func auditDumpRecord(sessionID string, rec sdkagentloop.AuditRecord) auditDumpEntry {
	req := rec.Request
	resp := rec.Response
	entry := auditDumpEntry{
		SessionID: sessionID,
		Iteration: rec.Iteration,
		Request: auditDumpRequest{
			Model:            req.Model,
			Stream:           req.Stream,
			Temperature:      req.Temperature,
			MaxTokens:        req.MaxTokens,
			ReasoningLevel:   string(req.ReasoningEffort),
			ReasoningDialect: string(req.ReasoningDialect),
			ToolChoice:       string(req.ToolChoice),
			ToolNames:        auditDumpToolNames(req.Tools),
			MessageCount:     len(req.Messages),
		},
		Response: auditDumpResponse{
			FinishReason: resp.FinishReason,
			Content:      auditDumpText(resp.Message.Content),
			Reasoning:    auditDumpText(resp.Message.ReasoningContent),
			ToolCalls:    auditDumpToolCalls(resp.ToolCalls),
		},
		Usage: auditDumpUsage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
			CachedTokens:     resp.Usage.CachedTokens,
		},
		Messages: auditDumpMessages(req.Messages),
	}
	if resp.CacheUsage.Reported {
		entry.Cache = &auditDumpCache{
			CachedInput: resp.CacheUsage.CachedInputTokens,
			CacheWrite:  resp.CacheUsage.CacheWriteTokens,
		}
	}
	return entry
}

func auditDumpToolNames(tools []sdkprovider.ToolDefinition) []string {
	if len(tools) == 0 {
		return nil
	}
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return out
}

func auditDumpMessages(msgs []sdkprovider.Message) []auditDumpMessage {
	if len(msgs) == 0 {
		return nil
	}
	out := make([]auditDumpMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, auditDumpMessage{
			Role:       string(m.Role),
			Content:    auditDumpText(m.Content),
			Reasoning:  auditDumpText(m.ReasoningContent),
			ToolCallID: m.ToolCallID,
			Name:       m.Name,
			ToolCalls:  auditDumpToolCalls(m.ToolCalls),
		})
	}
	return out
}

func auditDumpToolCalls(calls []sdkprovider.ToolCall) []auditDumpToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]auditDumpToolCall, 0, len(calls))
	for _, c := range calls {
		out = append(out, auditDumpToolCall{
			ID:        c.ID,
			Name:      c.Name,
			Arguments: auditDumpText(string(c.Arguments)),
		})
	}
	return out
}

// auditDumpText redacts and caps one captured string. Redaction runs first:
// a truncation that split a secret would still leave its head in the file.
func auditDumpText(s string) string {
	if s == "" {
		return ""
	}
	s = redact.Text(s)
	if len(s) <= auditDumpFieldCap {
		return s
	}
	return s[:auditDumpFieldCap] + fmt.Sprintf("…[truncated, %d bytes total]", len(s))
}
