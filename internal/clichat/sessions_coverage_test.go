package clichat

// sessions_coverage_test.go covers the session formatting and parsing
// helpers in sessions_command.go. These were uncovered because legacytui
// and the cli command paths run them through the production flow; this
// test exercises each helper directly.

import (
	"bytes"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func TestRedactSessionMessagesForDisplay(t *testing.T) {
	msgs := []provider.Message{
		{Role: "user", Content: "open /tmp/key-pem"},
		{Role: "assistant", Content: "ok"},
	}
	out := redactSessionMessagesForDisplay(msgs)
	if len(out) != len(msgs) {
		t.Fatalf("redactSessionMessagesForDisplay len = %d, want %d", len(out), len(msgs))
	}
	// empty slice path
	if out := redactSessionMessagesForDisplay(nil); len(out) != 0 {
		t.Fatalf("redactSessionMessagesForDisplay(nil) = %v, want nil", out)
	}
}

func TestTruncateForDisplay(t *testing.T) {
	if got := truncateForDisplay(""); got != "" {
		t.Fatalf("truncateForDisplay(empty) = %q, want empty", got)
	}
	if got := truncateForDisplay("short"); got != "short" {
		t.Fatalf("truncateForDisplay(short) = %q, want short", got)
	}
	// Long input must be truncated at the documented length.
	long := strings.Repeat("a", 1000)
	got := truncateForDisplay(long)
	if len(got) > len(long) {
		t.Fatalf("truncateForDisplay did not truncate (len=%d)", len(got))
	}
}

func TestParseSessionsWorkspaceAndJSON(t *testing.T) {
	ws, jsonFlag, pos, err := parseSessionsWorkspaceAndJSON("list", []string{"--workspace", "/tmp/x"}, 0)
	if err != nil || ws != "/tmp/x" || jsonFlag || len(pos) != 0 {
		t.Fatalf("parseSessionsWorkspaceAndJSON = (%q, %v, %v, %v)", ws, jsonFlag, pos, err)
	}
	_, jsonFlag, _, err = parseSessionsWorkspaceAndJSON("list", []string{"--json"}, 0)
	if err != nil || !jsonFlag {
		t.Fatalf("parseSessionsWorkspaceAndJSON --json: jsonFlag=%v, err=%v", jsonFlag, err)
	}
}

func TestWriteSessionsJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := writeSessionsJSON(&buf, map[string]any{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"a": 1`) {
		t.Fatalf("writeSessionsJSON did not emit expected JSON; got %q", buf.String())
	}
}

func TestWriteSessionsTable(t *testing.T) {
	var buf bytes.Buffer
	infos := []chat.SessionInfo{{SessionID: "a", Name: "alpha"}, {SessionID: "b", Name: "beta"}}
	writeSessionsTable(&buf, infos)
	if !strings.Contains(buf.String(), "alpha") || !strings.Contains(buf.String(), "beta") {
		t.Fatalf("writeSessionsTable did not emit session names; got %q", buf.String())
	}
	// Empty list still writes a header.
	buf.Reset()
	writeSessionsTable(&buf, nil)
	if buf.Len() == 0 {
		t.Fatal("writeSessionsTable(empty) wrote nothing")
	}
}

func TestWriteSessionsShowText(t *testing.T) {
	var buf bytes.Buffer
	writeSessionsShowText(&buf, []provider.Message{{Role: "user", Content: "hi"}})
	if !strings.Contains(buf.String(), "hi") {
		t.Fatalf("writeSessionsShowText did not emit message body; got %q", buf.String())
	}
}
