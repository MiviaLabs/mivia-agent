package subagents_test

// Truncation-aware schema repair: a reply cut off by the provider output
// budget (finish_reason "length") must produce a corrective turn that says so
// and never inlines the failed output; exhaustion must name finish_reason
// "length" as the cause while staying ErrSchemaViolation-typed; ordinary
// invalid replies keep the current corrective message; and the byte-identical
// no-progress fast-fail still fires. The budget is unchanged (retryMax+1
// model calls, DC-8).

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/jschema"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// truncationScriptedCompleter returns scripted replies (content plus finish
// reason) and records the last RoleUser message of every request so tests can
// assert on the corrective turns the sub-agent actually sent.
type truncationScriptedCompleter struct {
	replies   []provider.Response
	i         int
	userTurns []string
}

func (c *truncationScriptedCompleter) Name() string { return "truncation-scripted" }
func (c *truncationScriptedCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", errors.New("Chat unused")
}
func (c *truncationScriptedCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "", errors.New("ChatStream unused")
}
func (c *truncationScriptedCompleter) ChatTurn(_ context.Context, req provider.Request) (*provider.Response, error) {
	if n := len(req.Messages); n > 0 {
		if last := req.Messages[n-1]; last.Role == provider.RoleUser {
			c.userTurns = append(c.userTurns, last.Content)
		}
	}
	if c.i >= len(c.replies) {
		return &provider.Response{Content: `{"ok":true}`, FinishReason: "stop"}, nil
	}
	r := c.replies[c.i]
	c.i++
	return &r, nil
}

func invokeTruncation(t *testing.T, replies []provider.Response, schema map[string]any, retryMax int) (map[string]any, *truncationScriptedCompleter, error) {
	t.Helper()
	c := &truncationScriptedCompleter{replies: replies}
	reg := tools.NewRegistry()
	h := &subagents.MultiStepHandler{
		Completer:      c,
		FullRegistry:   reg,
		Model:          "m",
		MaxSteps:       10,
		SchemaRetryMax: retryMax,
		OutputSchema:   schema,
	}
	input, _ := json.Marshal("do the work")
	out, err := h.Invoke(context.Background(), runtime.Request{
		ID: "task-trunc", Name: "worker", Kind: runtime.Subagent, Input: input,
		OutputSchema: schema,
	})
	var payload map[string]any
	if len(out) > 0 {
		_ = json.Unmarshal(out, &payload)
	}
	return payload, c, err
}

// correctiveTurn returns the LAST recorded user turn, which after a retry is
// the corrective message the sub-agent sent (userTurns[0] is the task prompt).
func correctiveTurn(c *truncationScriptedCompleter) string {
	if len(c.userTurns) == 0 {
		return ""
	}
	return c.userTurns[len(c.userTurns)-1]
}

func TestTruncatedReplyProducesTruncationAwareCorrective(t *testing.T) {
	const marker = "CUTOFF_MARKER"
	// First reply is truncated mid-JSON and carries a distinctive marker; the
	// corrective turn must say it was cut off, restate the schema contract,
	// and NOT inline the failed candidate (prompt-injection amplification).
	first := `{"ok":true,"extra":"` + marker + `"`
	payload, c, err := invokeTruncation(t, []provider.Response{
		{Content: first, FinishReason: "length"},
		{Content: `{"ok":true}`, FinishReason: "stop"},
	}, schemaObject(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if payload["schema"] != "ok" || payload["status"] != "completed" {
		t.Fatalf("payload=%#v", payload)
	}
	if c.i != 2 {
		t.Fatalf("model calls = %d, want 2", c.i)
	}
	corrective := correctiveTurn(c)
	if !strings.Contains(corrective, "cut off") && !strings.Contains(corrective, "output limit") {
		t.Fatalf("corrective must state the reply was cut off by the output limit: %q", corrective)
	}
	if !strings.Contains(corrective, "Output an instance of the schema") {
		t.Fatalf("corrective must restate the schema contract: %q", corrective)
	}
	if !strings.Contains(corrective, `"type":"object"`) {
		t.Fatalf("corrective must restate the schema whole: %q", corrective)
	}
	if strings.Contains(corrective, marker) || strings.Contains(corrective, first) {
		t.Fatalf("corrective must not inline the failed candidate: %q", corrective)
	}
	if !utf8.ValidString(corrective) {
		t.Fatalf("corrective is invalid UTF-8: %q", corrective)
	}
	if len(corrective) > jschema.MaxCorrectiveBytes {
		t.Fatalf("corrective = %d bytes, want <= %d", len(corrective), jschema.MaxCorrectiveBytes)
	}
}

func TestTruncationToExhaustionNamesFinishReason(t *testing.T) {
	// Three DISTINCT truncated candidates at SchemaRetryMax=2: the full
	// retryMax+1 budget is consumed and the error names truncation as the
	// cause while staying ErrSchemaViolation-typed. Output is deleted on
	// exhaustion, exactly like any other schema violation.
	payload, c, err := invokeTruncation(t, []provider.Response{
		{Content: `{"ok":true,"a":`, FinishReason: "length"},
		{Content: `{"ok":true,"b":`, FinishReason: "length"},
		{Content: `{"ok":true,"c":`, FinishReason: "length"},
	}, schemaObject(), 2)
	if !errors.Is(err, subagents.ErrSchemaViolation) {
		t.Fatalf("err=%v, want ErrSchemaViolation", err)
	}
	if !strings.Contains(err.Error(), `finish_reason "length"`) {
		t.Fatalf("err=%v, want it to name finish_reason \"length\"", err)
	}
	if c.i != 3 {
		t.Fatalf("model calls = %d, want retryMax+1 = 3 (no budget growth, DC-8)", c.i)
	}
	if payload["schema"] != "violation" {
		t.Fatalf("schema=%v payload=%#v", payload["schema"], payload)
	}
	if _, has := payload["output"]; has {
		t.Fatalf("output must be deleted on schema violation: %#v", payload)
	}
}

func TestOrdinaryInvalidReplyKeepsCurrentCorrective(t *testing.T) {
	// A plain invalid reply with finish_reason "stop" keeps the existing
	// jschema.FormatCorrectiveWithSchema message: no truncation vocabulary.
	payload, c, err := invokeTruncation(t, []provider.Response{
		{Content: "not json", FinishReason: "stop"},
		{Content: `{"ok":true}`, FinishReason: "stop"},
	}, schemaObject(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if payload["schema"] != "ok" {
		t.Fatalf("payload=%#v", payload)
	}
	corrective := correctiveTurn(c)
	if !strings.Contains(corrective, "did not match the required JSON schema") {
		t.Fatalf("ordinary corrective must keep the current message: %q", corrective)
	}
	if strings.Contains(corrective, "cut off") || strings.Contains(corrective, "output limit") {
		t.Fatalf("ordinary corrective must not claim truncation: %q", corrective)
	}
	if strings.Contains(corrective, "not json") {
		t.Fatalf("ordinary corrective must not inline the failed candidate: %q", corrective)
	}
}

func TestTruncationNoProgressStillFailsFast(t *testing.T) {
	// Byte-identical truncated candidates: the no-progress fast-fail fires
	// after exactly 2 calls, before the budget check, even on the truncation
	// path.
	replies := []provider.Response{
		{Content: `{"ok":true,"x":`, FinishReason: "length"},
		{Content: `{"ok":true,"x":`, FinishReason: "length"},
		{Content: `{"ok":true,"x":`, FinishReason: "length"},
	}
	payload, c, err := invokeTruncation(t, replies, schemaObject(), 2)
	if !errors.Is(err, subagents.ErrSchemaViolation) {
		t.Fatalf("err=%v, want ErrSchemaViolation", err)
	}
	if !strings.Contains(err.Error(), "no progress on schema repair") {
		t.Fatalf("err=%v, want the no-progress cause", err)
	}
	if c.i != 2 {
		t.Fatalf("model calls = %d, want exactly 2 (initial + one confirm)", c.i)
	}
	if payload["schema"] != "violation" {
		t.Fatalf("schema=%v payload=%#v", payload["schema"], payload)
	}
}
