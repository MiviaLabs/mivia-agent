package clichat

// Integration test for the compaction channel contract, driven against REAL
// storage and a REAL OpenAI-compatible httptest provider through the real
// chat --json surface:
//
//   - (a) INV-AG-41: a threshold compaction on the plain (--no-tools) surface
//     emits the typed "compaction" NDJSON event with compaction.summarized
//     == true.
//   - (b) INV-AG-32: that event carries NO summary payload - no summary body
//     field and no "[host-injected context summary" content anywhere in it.
//   - (c) a [context.summary] enabled = false workspace compacts
//     structural-only: summarized == false, the event says "(structural
//     only, no summary)", and the wire names the missing condition
//     ("[context.summary]").
//   - (d/e) INV-AG-39: the rendered summary is durable ONLY in the
//     checkpoint's active context; the projected source events/payloads
//     never carry it.
//
// The model profile's context window is sized so the automatic threshold
// trigger fires after a handful of turns (mirrors the sizing notes in
// scripts/e2e_context_compaction.py scenario_automatic).

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	_ "modernc.org/sqlite"
)

// channelsSummaryStub is an OpenAI-compatible httptest handler that records
// every chat-completions request and answers summary requests (first message
// is the summarize system prompt, see summarySystemMarker) with the
// envelope-echo JSON from summaryEchoReply, and every other request with
// "ok". Main-turn requests are streamed (SSE); summary requests are
// non-streaming JSON - the two wire shapes the OpenAI-compatible client uses.
type channelsSummaryStub struct {
	mu       sync.Mutex
	requests []provider.Request
}

func (s *channelsSummaryStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var wire struct {
		Messages []provider.Message `json:"messages"`
		Stream   bool               `json:"stream"`
	}
	_ = json.Unmarshal(body, &wire)
	req := provider.Request{Messages: wire.Messages, Stream: wire.Stream}
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()

	content := "ok"
	if isSummaryRequest(req) {
		content = summaryEchoReply(req)
	}
	w.Header().Set("Content-Type", "application/json")
	if !wire.Stream {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"index": 0, "finish_reason": "stop",
				"message": map[string]string{"role": "assistant", "content": content},
			}},
		})
		return
	}
	chunk, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{
			"index": 0, "finish_reason": "stop",
			"delta": map[string]string{"role": "assistant", "content": content},
		}},
	})
	fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
}

// summaryRequests returns a copy of every recorded summary request.
func (s *channelsSummaryStub) summaryRequests() []provider.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []provider.Request
	for _, req := range s.requests {
		if isSummaryRequest(req) {
			out = append(out, req)
		}
	}
	return out
}

// channelsWorkspaceConfig writes a HOME-pinned temp workspace whose
// mivia.toml points the ollama provider at the httptest stub and pins the
// durable context store under the workspace (mirrors catalogCompactWorkspace).
// summaryEnabled drives [context.summary] enabled.
func channelsWorkspaceConfig(t *testing.T, serverURL string, summaryEnabled bool) (ws, cfgPath, storePath string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OLLAMA_API_KEY", "")
	ws = t.TempDir()
	cfgPath = filepath.Join(ws, ".mivia", "mivia.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	storePath = filepath.Join(ws, ".mivia", "context.db")
	fixture := fmt.Sprintf(`[provider]
name = "ollama"

[providers.ollama]
base_url = "%s/v1"
api_key_env = "OLLAMA_API_KEY"
models = [{ name = "compact-test", context_window_tokens = 1800, max_output_tokens = 200 }]

[context.summary]
enabled = %v

[subagents]
store_path = %q
`, serverURL, summaryEnabled, storePath)
	if err := os.WriteFile(cfgPath, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(ws)
	return ws, cfgPath, storePath
}

// channelsNewSession loads the workspace config, builds the real completer
// against the httptest stub, and wires the real context store and summary
// policy onto a live session.
func channelsNewSession(t *testing.T, ws, cfgPath string) (*chat.Session, *config.Resolved, *storage.SQLite) {
	t.Helper()
	root, err := chatWorkspaceRoot(ws)
	if err != nil {
		t.Fatalf("chatWorkspaceRoot: %v", err)
	}
	res, err := config.Load(config.LoadOptions{ConfigPath: cfgPath, WorkspaceRoot: root})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	res.SystemPrompt = defaultSystemPrompt
	completer, err := provider.New(res)
	if err != nil {
		t.Fatalf("provider.New: %v", err)
	}
	sess := chat.NewSession(res, completer)
	store, err := setupChatSessionContext(sess, root, chatInvocation{workspacePath: root}, res)
	if err != nil {
		t.Fatalf("setupChatSessionContext: %v", err)
	}
	return sess, res, store
}

// channelsDriveUntilCompaction drives plain chat turns through the real
// --json line-mode surface until the configured context window's automatic
// threshold trigger emits a "compaction" event, and returns the full NDJSON
// transcript. The stub answers every turn with "ok", so history growth (and
// the threshold crossing) is purely a function of the configured window.
func channelsDriveUntilCompaction(t *testing.T, sess *chat.Session, maxTurns int) string {
	t.Helper()
	var transcript strings.Builder
	for i := 1; i <= maxTurns; i++ {
		line := fmt.Sprintf("turn %d: %s", i, strings.Repeat("context compaction channel ", 20))
		done := captureStdout(t)
		err := sendLineMode(sess, line, nil, true)
		wire := done()
		transcript.WriteString(wire)
		if err != nil {
			t.Fatalf("turn %d: sendLineMode: %v (transcript so far: %s)", i, err, transcript.String())
		}
		if strings.Contains(wire, `"type":"compaction"`) {
			return transcript.String()
		}
	}
	return transcript.String()
}

// channelsCompactionLines returns the raw NDJSON lines typed "compaction".
func channelsCompactionLines(t *testing.T, transcript string) []string {
	t.Helper()
	var lines []string
	for _, line := range splitNonEmptyLines(transcript) {
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			t.Fatalf("wire line is not valid JSON: %q: %v", line, err)
		}
		if raw["type"] == "compaction" {
			lines = append(lines, line)
		}
	}
	return lines
}

// channelsCompaction returns the nested compaction record of one wire line.
func channelsCompaction(t *testing.T, line string) map[string]any {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		t.Fatalf("compaction line is not valid JSON: %q: %v", line, err)
	}
	record, ok := raw["compaction"].(map[string]any)
	if !ok {
		t.Fatalf("compaction line carries no nested compaction record: %s", line)
	}
	return record
}

// channelsDurableCheckpoint returns the newest durable checkpoint's active
// context bytes for the session, read straight from context_checkpoints.
func channelsDurableCheckpoint(t *testing.T, storePath, sessionID string) string {
	t.Helper()
	db, err := sql.Open("sqlite", storePath)
	if err != nil {
		t.Fatalf("open store %s: %v", storePath, err)
	}
	defer db.Close()
	var active []byte
	if err := db.QueryRow(`SELECT active_context FROM context_checkpoints WHERE session_id=? ORDER BY durable_revision DESC LIMIT 1`, sessionID).Scan(&active); err != nil {
		t.Fatalf("read active_context for session %s: %v", sessionID, err)
	}
	return string(active)
}

// channelsSourceProjectionCarriesMarker reports whether the host-injected
// context-summary marker leaks into ANY stored source-projection surface:
// context_payloads, context_payload_chunks, the generic content table, or the
// context_source_events rows themselves.
func channelsSourceProjectionCarriesMarker(t *testing.T, storePath, marker string) bool {
	t.Helper()
	db, err := sql.Open("sqlite", storePath)
	if err != nil {
		t.Fatalf("open store %s: %v", storePath, err)
	}
	defer db.Close()
	for _, table := range []string{"context_payloads", "context_payload_chunks", "content"} {
		rows, err := db.Query(`SELECT data FROM ` + table)
		if err != nil {
			continue // table absent in this schema revision
		}
		for rows.Next() {
			var data []byte
			if err := rows.Scan(&data); err != nil {
				rows.Close()
				t.Fatalf("scan %s.data: %v", table, err)
			}
			if bytes.Contains(data, []byte(marker)) {
				rows.Close()
				return true
			}
		}
		rows.Close()
	}
	rows, err := db.Query(`SELECT kind || '|' || role || '|' || COALESCE(tool_call_id, '') || '|' || COALESCE(payload_ref, '') || '|' || provenance || '|' || redaction_status FROM context_source_events`)
	if err == nil {
		for rows.Next() {
			var meta string
			if err := rows.Scan(&meta); err != nil {
				rows.Close()
				t.Fatalf("scan context_source_events: %v", err)
			}
			if strings.Contains(meta, marker) {
				rows.Close()
				return true
			}
		}
		rows.Close()
	}
	return false
}

// TestCompactionChannelsAutomaticEventOmitsSummaryAndPersistsToCheckpoint
// drives the real chat --json surface against an httptest OpenAI-compatible
// stub until the configured window triggers an automatic compaction, then
// pins the INV-AG-32 channel contract: the typed "compaction" event reaches
// the wire with summarized == true and NO summary payload, while the rendered
// summary is durable ONLY in the checkpoint's active context - never in the
// projected source events/payloads (INV-AG-39).
func TestCompactionChannelsAutomaticEventOmitsSummaryAndPersistsToCheckpoint(t *testing.T) {
	stub := &channelsSummaryStub{}
	server := httptest.NewServer(stub)
	t.Cleanup(server.Close)

	ws, cfgPath, storePath := channelsWorkspaceConfig(t, server.URL, true)
	sess, _, store := channelsNewSession(t, ws, cfgPath)
	defer store.Close()

	transcript := channelsDriveUntilCompaction(t, sess, 30)

	lines := channelsCompactionLines(t, transcript)
	if len(lines) == 0 {
		t.Fatal("automatic compaction never reached the --json wire (window 1800 / output 200 should cross the 80% trigger after ~8 turns)")
	}
	record := channelsCompaction(t, lines[0])

	// (a) INV-AG-41: the threshold compaction is a REAL summarized compaction.
	if summarized, _ := record["summarized"].(bool); !summarized {
		t.Fatalf("compaction.summarized = %v, want true: %s", record["summarized"], lines[0])
	}
	if len(stub.summaryRequests()) == 0 {
		t.Fatal("summarized=true but the stub provider received no summary request")
	}

	// (b) INV-AG-32: the event carries NO summary payload.
	if strings.Contains(lines[0], "[host-injected context summary") {
		t.Fatalf("INV-AG-32 broken: compaction event leaks summary content: %s", lines[0])
	}
	wantKeys := []string{"trigger", "before_tokens", "after_tokens", "elided_messages", "elided_bytes", "summary_version", "summarized"}
	if len(record) != len(wantKeys) {
		t.Fatalf("INV-AG-32 broken: compaction record has %d fields, want exactly %v (no summary body field): %s", len(record), wantKeys, lines[0])
	}
	for _, key := range wantKeys {
		if _, ok := record[key]; !ok {
			t.Fatalf("compaction record missing %q: %s", key, lines[0])
		}
	}

	// (d) INV-AG-39: the checkpoint's active context IS the summary's carrier.
	active := channelsDurableCheckpoint(t, storePath, sess.SessionID)
	if !strings.Contains(active, "[host-injected context summary") {
		t.Fatal("durable context_checkpoints.active_context does not carry the host-injected context summary")
	}

	// (e) ...and the source projection stays free of it.
	if channelsSourceProjectionCarriesMarker(t, storePath, "[host-injected context summary") {
		t.Fatal("INV-AG-39 broken: summary content leaked into the projected source events/payloads")
	}
}

// TestCompactionChannelsStructuralOnlyNamesTheMissingCondition drives the
// second workspace with [context.summary] enabled = false: automatic turns
// never call the summarizer, and /compact emits a structural-only compaction
// (summarized == false, "(structural only, no summary)") whose companion
// notice names the missing condition ("[context.summary]").
func TestCompactionChannelsStructuralOnlyNamesTheMissingCondition(t *testing.T) {
	stub := &channelsSummaryStub{}
	server := httptest.NewServer(stub)
	t.Cleanup(server.Close)

	ws, cfgPath, _ := channelsWorkspaceConfig(t, server.URL, false)
	sess, res, store := channelsNewSession(t, ws, cfgPath)
	defer store.Close()

	// Two real committed turns so /compact has history to reclaim.
	done := captureStdout(t)
	for i := 1; i <= 2; i++ {
		if err := sendLineMode(sess, fmt.Sprintf("structural turn %d: %s", i, strings.Repeat("context compaction channel ", 20)), nil, true); err != nil {
			t.Fatalf("turn %d: sendLineMode: %v", i, err)
		}
	}
	_ = done()
	if got := stub.summaryRequests(); len(got) != 0 {
		t.Fatalf("[context.summary] enabled=false still sent %d summary request(s)", len(got))
	}

	var buf strings.Builder
	previous := activeJSONSlashSink
	activeJSONSlashSink = &JSONSlashSink{w: &buf}
	defer func() { activeJSONSlashSink = previous }()
	handled, exit, err := handleSlashCompact("/compact", sess, res, nil)
	if err != nil {
		t.Fatalf("handleSlashCompact: %v", err)
	}
	if !handled || exit {
		t.Fatalf("handled/exit = %v/%v, want true/false", handled, exit)
	}

	lines := channelsCompactionLines(t, buf.String())
	if len(lines) == 0 {
		t.Fatalf("/compact emitted no compaction event: %q", buf.String())
	}
	record := channelsCompaction(t, lines[0])

	// (c) structural-only: summarized == false and the event says so, and it
	// still carries no summary payload.
	if summarized, _ := record["summarized"].(bool); summarized {
		t.Fatalf("compaction.summarized = %v, want false for [context.summary] enabled=false: %s", record["summarized"], lines[0])
	}
	if !strings.Contains(lines[0], "structural only") {
		t.Fatalf("compaction event does not say \"structural only\": %s", lines[0])
	}
	if strings.Contains(lines[0], "[host-injected context summary") {
		t.Fatalf("structural compaction event still leaks summary content: %s", lines[0])
	}
	// The wire names the missing condition.
	if !strings.Contains(buf.String(), "[context.summary]") {
		t.Fatalf("/compact wire never names the missing condition: %q", buf.String())
	}
}
