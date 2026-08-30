// Package agent - operator-invoked provider wire dump.
//
// A turn that came back empty left no evidence at all: no way to tell a
// model that answered with nothing from a request this host built wrong.
// This file records the request and the response of every completed
// provider call to an operator-enabled, append-only JSONL file.
//
// The capture point is the host completer's per-call observation seam
// (newSDKTurnCompleter's onUsage), NOT the SDK loop's Audit hook. That
// distinction is the whole value of the file: the SDK builds its request
// from Model, Messages, and Tools only, and the fields that decide how a
// model behaves - reasoning level and dialect, max_tokens, tool_choice,
// temperature, the per-request deadline - are merged in afterwards by
// mergeTurnDefaults, inside the completer. Auditing the SDK's request
// recorded those as empty and produced a dump that could not answer the
// question it exists for.

package agent

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
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

// auditDumpFailedOnce reports a broken dump target once, and
// auditDumpDisabled latches so the failure is REALLY terminal rather than
// merely quiet: an unwritable target used to be retried (mkdir + open) on
// every iteration of every turn for the rest of the process while the log
// line claimed the dump was disabled.
var (
	auditDumpFailedOnce sync.Once
	auditDumpDisabled   atomic.Bool
)

// providerAuditDump records one completed provider call.
type providerAuditDump func(req provider.Request, resp *provider.Response, iteration int)

// newProviderAuditDump returns the recorder for one run, or nil when
// EnvProviderAuditDir is unset.
//
// It never returns an error and never panics: it observes a turn that is
// already in flight, and a debugging aid must not be able to affect the
// turn it is watching.
func newProviderAuditDump(sessionID string) providerAuditDump {
	dir := strings.TrimSpace(os.Getenv(EnvProviderAuditDir))
	if dir == "" {
		return nil
	}
	path := filepath.Join(dir, auditDumpFileName(sessionID))
	return func(req provider.Request, resp *provider.Response, iteration int) {
		if resp == nil || auditDumpDisabled.Load() {
			return
		}
		if err := appendAuditDump(dir, path, auditDumpRecord(sessionID, iteration, req, resp)); err != nil {
			auditDumpDisabled.Store(true)
			auditDumpFailedOnce.Do(func() {
				log.Printf("agent: provider audit dump disabled for this process: %v", err)
			})
		}
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
	// O_NOFOLLOW: refuse to write through a symlink planted at the dump
	// path. Without it, an operator who points the variable at a shared
	// directory can have a turn's whole prompt and response appended to a
	// file another user chose - and the 0600 argument does not apply,
	// because the target already exists. Repo rule 10 forbids following a
	// symlink on a write path unconditionally.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
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
	StreamTransport  bool     `json:"stream_transport,omitempty"`
	Temperature      *float64 `json:"temperature,omitempty"`
	MaxTokens        *int     `json:"max_tokens,omitempty"`
	ReasoningLevel   string   `json:"reasoning_level,omitempty"`
	ReasoningDialect string   `json:"reasoning_dialect,omitempty"`
	ToolChoice       string   `json:"tool_choice,omitempty"`
	TimeoutSeconds   float64  `json:"timeout_seconds,omitempty"`
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
	Reported     bool `json:"reported"`
	InputTokens  int  `json:"input_tokens"`
	OutputTokens int  `json:"output_tokens"`
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

// auditDumpRecord projects one completed call onto the dump shape,
// redacting every captured string.
func auditDumpRecord(sessionID string, iteration int, req provider.Request, resp *provider.Response) auditDumpEntry {
	entry := auditDumpEntry{
		SessionID: sessionID,
		Iteration: iteration,
		Request: auditDumpRequest{
			Model:            req.Model,
			Stream:           req.Stream,
			StreamTransport:  req.StreamTransport,
			Temperature:      req.Temperature,
			MaxTokens:        req.MaxTokens,
			ReasoningLevel:   string(req.ReasoningLevel),
			ReasoningDialect: string(req.ReasoningDialect),
			ToolChoice:       req.ToolChoice,
			TimeoutSeconds:   req.Timeout.Seconds(),
			ToolNames:        auditDumpToolNames(req.Tools),
			MessageCount:     len(req.Messages),
		},
		Response: auditDumpResponse{
			FinishReason: resp.FinishReason,
			Content:      auditDumpText(resp.Content),
			Reasoning:    auditDumpText(resp.ReasoningContent),
			ToolCalls:    auditDumpToolCalls(resp.ToolCalls),
		},
		Usage: auditDumpUsage{
			Reported:     resp.TokenUsage.Reported,
			InputTokens:  resp.TokenUsage.InputTokens,
			OutputTokens: resp.TokenUsage.OutputTokens,
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

// auditDumpToolNames lists the advertised tool names. A ToolSpec is an
// untyped OpenAI tools[] entry, so a malformed one contributes "?" rather
// than being dropped: the COUNT is what tells an operator whether the turn
// had a tool surface at all.
func auditDumpToolNames(tools []provider.ToolSpec) []string {
	if len(tools) == 0 {
		return nil
	}
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		name := "?"
		if fn, ok := t["function"].(map[string]any); ok {
			if n, ok := fn["name"].(string); ok && n != "" {
				name = n
			}
		}
		out = append(out, name)
	}
	return out
}

func auditDumpMessages(msgs []provider.Message) []auditDumpMessage {
	if len(msgs) == 0 {
		return nil
	}
	out := make([]auditDumpMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, auditDumpMessage{
			Role:       m.Role,
			Content:    auditDumpText(m.Content),
			Reasoning:  auditDumpText(m.ReasoningContent),
			ToolCallID: m.ToolCallID,
			Name:       m.Name,
			ToolCalls:  auditDumpToolCalls(m.ToolCalls),
		})
	}
	return out
}

func auditDumpToolCalls(calls []provider.ToolCall) []auditDumpToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]auditDumpToolCall, 0, len(calls))
	for _, c := range calls {
		out = append(out, auditDumpToolCall{
			ID:        c.ID,
			Name:      c.Function.Name,
			Arguments: auditDumpArguments([]byte(c.Function.Arguments)),
		})
	}
	return out
}

// auditDumpArguments redacts one tool call's argument payload.
//
// It parses the JSON and goes through redact.JSONValue rather than
// redact.Text, because a redaction Policy has TWO halves and Text applies
// only one of them: patterns match values, key elision matches field NAMES
// and is reachable only through JSONValue. A workspace whose [privacy]
// block sets redaction_key_names = ["token"] and no pattern would otherwise
// see {"api_token":"..."} written verbatim here while every other surface
// that handles tool arguments elides it (loop_tool_preview.go,
// runtime/audit_preview.go, workflows/ledger/status.go all use JSONValue).
//
// A payload that is not valid JSON - a fragment from a truncated stream -
// falls back to the string path so it is still captured and still pattern
// redacted.
func auditDumpArguments(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return auditDumpText(string(raw))
	}
	scrubbed, err := json.Marshal(redact.JSONValue(parsed))
	if err != nil {
		return auditDumpText(string(raw))
	}
	// Pattern redaction still runs over the re-serialized form: JSONValue
	// elides by key, Text catches a secret sitting in a value under a key
	// nobody named.
	return auditDumpText(string(scrubbed))
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
