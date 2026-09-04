package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSanitizeToolMetadataBoundsUntrustedValues(t *testing.T) {
	if _, _, err := sanitizeToolMetadata("too long", map[string]any{"type": "object"}, 3, 100, nil); err == nil {
		t.Fatal("sanitizeToolMetadata() accepted an oversized description")
	}
	schema := map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}}
	description, got, err := sanitizeToolMetadata("line\nvalue", schema, 100, 1000, nil)
	if err != nil {
		t.Fatal(err)
	}
	if description != "line value" || got["type"] != "object" {
		t.Fatalf("sanitized metadata = %q %#v", description, got)
	}
}

func TestSanitizeToolMetadataBridgesUnsupportedSchema(t *testing.T) {
	schema := map[string]any{"$ref": "https://untrusted.invalid/schema"}
	_, got, err := sanitizeToolMetadata("tool", schema, 100, 1000, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got["type"] != "object" || got["additionalProperties"] != true || len(got) != 2 {
		t.Fatalf("bridged schema = %#v", got)
	}
}

func TestSanitizeToolMetadataCopiesSafeSchema(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}}
	_, got, err := sanitizeToolMetadata("tool", schema, 100, 1000, nil)
	if err != nil {
		t.Fatal(err)
	}
	properties := got["properties"].(map[string]any)
	if properties["name"].(map[string]any)["type"] != "string" {
		t.Fatalf("bridged schema = %#v", got)
	}
	properties["name"].(map[string]any)["type"] = "changed"
	if schema["properties"].(map[string]any)["name"].(map[string]any)["type"] != "string" {
		t.Fatal("bridge returned a mutable source schema")
	}
}

// TestSanitizeToolMetadataKeepsDescriptionBearingProperties is the
// empty-advertised-schema regression: the codegraph_explore schema - like
// nearly every real server's - puts "description" on the schema and on each
// property. bridgeSchemaValue treated any unlisted key as fatal, so the whole
// schema collapsed to a bare open object: the model was never shown the
// parameter names or the required list, sent empty arguments, and the server
// rejected them. Descriptions must survive the bridge.
func TestSanitizeToolMetadataKeepsDescriptionBearingProperties(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Symbol names or a natural-language question",
			},
			"maxFiles": map[string]any{"type": "number", "default": float64(12)},
		},
		"required": []any{"query"},
	}
	_, got, err := sanitizeToolMetadata("tool", schema, 100, 4096, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got["type"] != "object" {
		t.Fatalf("bridged schema = %#v, want type object", got)
	}
	properties, ok := got["properties"].(map[string]any)
	if !ok {
		t.Fatalf("bridged schema = %#v, want the properties preserved", got)
	}
	query, ok := properties["query"].(map[string]any)
	if !ok || query["type"] != "string" || query["description"] != "Symbol names or a natural-language question" {
		t.Fatalf("bridged query property = %#v, want type and description preserved", query)
	}
	maxFiles := properties["maxFiles"].(map[string]any)
	if maxFiles["default"] != float64(12) {
		t.Fatalf("bridged maxFiles property = %#v, want the default preserved", maxFiles)
	}
	required, ok := got["required"].([]string)
	if !ok || !slices.Equal(required, []string{"query"}) {
		t.Fatalf("bridged required = %#v, want [query]", got["required"])
	}
}

// TestSanitizeToolMetadataOmitsUnknownAnnotations pins the fail-open rule:
// one vendor annotation key ("x-vendor") must not nuke the parameter
// contract - the key is dropped, everything else survives.
func TestSanitizeToolMetadataOmitsUnknownAnnotations(t *testing.T) {
	schema := map[string]any{
		"type":     "object",
		"x-vendor": map[string]any{"internal": "metadata"},
		"properties": map[string]any{
			"query": map[string]any{"type": "string"},
		},
	}
	_, got, err := sanitizeToolMetadata("tool", schema, 100, 4096, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := got["x-vendor"]; exists {
		t.Fatalf("bridged schema = %#v, want the unknown annotation omitted", got)
	}
	if got["properties"] == nil {
		t.Fatalf("bridged schema = %#v, want the properties kept despite the unknown key", got)
	}
}

// TestCallToolErrorTextKeepsServerDetail pins the second half of the
// transcript-visibility fix: an isError result carries the actionable reason
// in its CONTENT, and the old CallTool discarded it for a generic constant.
func TestCallToolErrorTextKeepsServerDetail(t *testing.T) {
	got := callToolErrorText([]sdk.Content{
		&sdk.TextContent{Text: "missing required argument: query"},
	})
	if got != "missing required argument: query" {
		t.Fatalf("callToolErrorText() = %q", got)
	}
	if empty := callToolErrorText(nil); empty != "MCP tool returned an error" {
		t.Fatalf("callToolErrorText(nil) = %q, want the honest generic", empty)
	}
}

func TestSanitizeToolMetadataRejectsDeepSchema(t *testing.T) {
	value := map[string]any{}
	root := value
	for range maxSchemaDepth + 1 {
		next := map[string]any{}
		value["nested"] = next
		value = next
	}
	if _, _, err := sanitizeToolMetadata("ok", root, 100, 10000, nil); err == nil {
		t.Fatal("sanitizeToolMetadata() accepted a deep schema")
	}
}

func TestSanitizeToolMetadataAppliesRedaction(t *testing.T) {
	policy, err := redact.Compile([]string{"secret"}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	description, _, err := sanitizeToolMetadata("secret value", nil, 100, 100, policy)
	if err != nil {
		t.Fatal(err)
	}
	if description == "secret value" {
		t.Fatal("sanitizeToolMetadata() did not redact the description")
	}
}

func TestSanitizeToolMetadataRedactsEnumValues(t *testing.T) {
	policy, err := redact.Compile([]string{"secret-value"}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	schema := map[string]any{"type": "string", "enum": []any{"public", "secret-value"}}
	_, got, err := sanitizeToolMetadata("tool", schema, 100, 1000, policy)
	if err != nil {
		t.Fatal(err)
	}
	values := got["enum"].([]any)
	if values[1] == "secret-value" {
		t.Fatalf("sanitizeToolMetadata() leaked enum value: %#v", values)
	}
}

func TestDiscoveredToolRedactsResult(t *testing.T) {
	policy, err := redact.Compile([]string{"secret"}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	tool := discoveredTool{remoteName: "result", client: resultClient{}, redaction: policy}
	got, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if got == "secret value" {
		t.Fatal("discoveredTool.Execute() did not redact result text")
	}
}

func TestDiscoveredToolUsesExternalServerCapability(t *testing.T) {
	tool := discoveredTool{serverID: "repository", timeout: 3 * time.Second, maxResultBytes: 100}
	capability := tool.Capability(nil)
	if capability.Class != tools.ExecutionExternal || capability.ResourceKey != "mcp:repository" || capability.Timeout != 3*time.Second {
		t.Fatalf("Capability() = %#v", capability)
	}
	if tool.ResultBudgetBytes() != 100 {
		t.Fatalf("ResultBudgetBytes() = %d, want 100", tool.ResultBudgetBytes())
	}
}

// TestDiscoveredToolSurfacesServerErrorRedacted pins the transcript contract
// reversed from TestDiscoveredToolHidesRemoteError: the operator and the model
// must see WHY an MCP call failed - a bare "MCP tool call failed" hid whether
// the arguments were wrong, the index stale, or the server crashed. The
// server-owned text passes through the session redaction policy (the same one
// results use) before it reaches the transcript.
func TestDiscoveredToolSurfacesServerErrorRedacted(t *testing.T) {
	policy, err := redact.Compile([]string{"hush-\\d+"}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	tool := discoveredTool{
		remoteName: "result",
		client:     errorMessageClient{"index stale: token hush-12345 in request"},
		redaction:  policy,
	}
	_, err = tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("Execute() = nil error, want the server-owned failure surfaced")
	}
	if !strings.Contains(err.Error(), "MCP tool call failed: index stale: token") {
		t.Fatalf("Execute() error = %v, want the server error detail surfaced for the transcript", err)
	}
	if strings.Contains(err.Error(), "hush-12345") {
		t.Fatalf("Execute() error = %v, want the secret redacted before surfacing", err)
	}
}

func TestDiscoveredToolBoundsServerErrorLength(t *testing.T) {
	tool := discoveredTool{remoteName: "result", client: errorMessageClient{strings.Repeat("x", 4096)}}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("Execute() = nil error, want the server-owned failure surfaced")
	}
	if len(err.Error()) > 600 {
		t.Fatalf("Execute() error is %d bytes, want the server detail bounded", len(err.Error()))
	}
	if !strings.HasSuffix(err.Error(), "…[truncated]") {
		t.Fatalf("Execute() error = %.80s..., want a truncation marker", err.Error())
	}
}

func TestDiscoveredToolEmptyServerErrorFallsBackToNoDetail(t *testing.T) {
	tool := discoveredTool{remoteName: "result", client: errorMessageClient{"   "}}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "server returned no error detail") {
		t.Fatalf("Execute() error = %v, want an honest no-detail message", err)
	}
}

func TestDiscoveredToolPreservesCancellationIdentity(t *testing.T) {
	tool := discoveredTool{remoteName: "result", client: canceledResultClient{}}
	got, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute() error = %v, want a context.Canceled identity", err)
	}
	if got != "" {
		t.Fatalf("Execute() result = %q, want empty on error", got)
	}
}

func TestDiscoveredToolPreservesTimeoutIdentity(t *testing.T) {
	tool := discoveredTool{remoteName: "result", client: timedOutResultClient{}}
	got, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Execute() error = %v, want a context.DeadlineExceeded identity", err)
	}
	if got != "" {
		t.Fatalf("Execute() result = %q, want empty on error", got)
	}
}

func TestDiscoveredToolReportsExpiredContextOverServerText(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tool := discoveredTool{remoteName: "result", client: ctxIgnoringDiagnosticClient{}}
	_, err := tool.Execute(ctx, json.RawMessage(`{}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute() error = %v, want a context.Canceled identity", err)
	}
	if err != nil && strings.Contains(err.Error(), "untrusted server diagnostic") {
		t.Fatalf("Execute() leaked server diagnostic text: %v", err)
	}
}

type resultClient struct{}

func (resultClient) ListTools(context.Context) ([]remoteTool, error) { return nil, nil }
func (resultClient) CallTool(context.Context, string, map[string]any) (string, error) {
	return "secret value", nil
}
func (resultClient) Close() error { return nil }

type errorMessageClient struct{ msg string }

func (c errorMessageClient) ListTools(context.Context) ([]remoteTool, error) { return nil, nil }
func (c errorMessageClient) CallTool(context.Context, string, map[string]any) (string, error) {
	return "", errors.New(c.msg)
}
func (c errorMessageClient) Close() error { return nil }

type canceledResultClient struct{}

func (canceledResultClient) ListTools(context.Context) ([]remoteTool, error) { return nil, nil }
func (canceledResultClient) CallTool(context.Context, string, map[string]any) (string, error) {
	return "", context.Canceled
}
func (canceledResultClient) Close() error { return nil }

type timedOutResultClient struct{}

func (timedOutResultClient) ListTools(context.Context) ([]remoteTool, error) { return nil, nil }
func (timedOutResultClient) CallTool(context.Context, string, map[string]any) (string, error) {
	return "", fmt.Errorf("server timed out: %w", context.DeadlineExceeded)
}
func (timedOutResultClient) Close() error { return nil }

type ctxIgnoringDiagnosticClient struct{}

func (ctxIgnoringDiagnosticClient) ListTools(context.Context) ([]remoteTool, error) { return nil, nil }
func (ctxIgnoringDiagnosticClient) CallTool(context.Context, string, map[string]any) (string, error) {
	return "", errors.New("untrusted server diagnostic")
}
func (ctxIgnoringDiagnosticClient) Close() error { return nil }

func TestSanitizeToolSchemaBoundsPostRedactionGrowth(t *testing.T) {
	policy, err := redact.Compile([]string{"sk-"}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	// The default placeholder is "[redacted]" (10 bytes); redaction replaces
	// the matched span, so a short match like "sk-abc" grows to "[redacted]abc".
	if redacted := policy.Text("sk-abc"); !strings.HasPrefix(redacted, "[redacted]") {
		t.Fatalf("redact.Compile default placeholder differs; redacted value = %q", redacted)
	}
	// 64 properties x 1 nested "v" property = 128 total properties, the
	// sanitizer's whole-schema complexity cap. The bridge sees at most 64
	// properties per level and 20 enum values, so it stays within the
	// per-level bridge caps (properties <= 128, enum <= 32).
	schema := map[string]any{"type": "object", "properties": map[string]any{}}
	properties := schema["properties"].(map[string]any)
	for i := 0; i < 64; i++ {
		enum := make([]any, 0, 20)
		for j := 0; j < 20; j++ {
			enum = append(enum, "sk-abc")
		}
		properties[fmt.Sprintf("p%d", i)] = map[string]any{
			"type": "object",
			"properties": map[string]any{
				"v": map[string]any{"type": "string", "enum": enum},
			},
		}
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) >= 20000 {
		t.Fatalf("precondition: raw schema marshal = %d bytes, want < 20000", len(raw))
	}
	redactedSchema, ok := policy.JSONValue(schema).(map[string]any)
	if !ok {
		t.Fatal("redaction did not yield a schema map")
	}
	encoded, err := json.Marshal(bridgeToolSchema(redactedSchema))
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) <= 20000 {
		t.Fatalf("precondition: post-redaction bridged marshal = %d bytes, want > 20000", len(encoded))
	}
	if _, err := sanitizeToolSchema(schema, 20000, policy); err == nil || !strings.Contains(err.Error(), "MCP tool schema exceeds configured limit") {
		t.Fatalf("sanitizeToolSchema(schema, 20000, policy) = %v, want error containing 'MCP tool schema exceeds configured limit'", err)
	}
	got, err := sanitizeToolSchema(schema, 40000, policy)
	if err != nil {
		t.Fatal(err)
	}
	if got["type"] != "object" {
		t.Fatalf("sanitizeToolSchema(schema, 40000, policy) type = %#v, want 'object'", got["type"])
	}
}

func TestScrubDescriptionTextStripsControlAndFormat(t *testing.T) {
	if got := scrubDescriptionText("a\x01b"); got != "a b" {
		t.Fatalf("scrubDescriptionText(\"a\\x01b\") = %q, want %q", got, "a b")
	}
	if got := scrubDescriptionText("a\u202eb"); got != "a b" {
		t.Fatalf("scrubDescriptionText(\"a\\u202eb\") = %q, want %q", got, "a b")
	}
	policy, err := redact.Compile([]string{"secret"}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	got := sanitizeToolDescription(" \x00 secret ", policy)
	if strings.Contains(got, "secret") || !strings.Contains(got, "[redacted]") {
		t.Fatalf("sanitizeToolDescription() = %q, want a trimmed redacted form without %q", got, "secret")
	}
}

func TestComposeToolDescriptionRedactsRemoteNameInWholeString(t *testing.T) {
	policy, err := redact.Compile([]string{"secret"}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := composeToolDescription("repo", "secretTool", "safe body", 4096, policy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "secret") {
		t.Fatalf("composeToolDescription() leaked the pattern in the composed text: %q", got)
	}
}

func TestComposeToolDescriptionBoundsWholeString(t *testing.T) {
	if _, err := composeToolDescription("repo", "t", "x", 30, nil); err == nil || !strings.Contains(err.Error(), "MCP tool description exceeds configured limit") {
		t.Fatalf("composeToolDescription(repo, t, x, 30, nil) = %v, want error containing 'MCP tool description exceeds configured limit'", err)
	}
	got, err := composeToolDescription("repo", "t", strings.Repeat("x", 60), 4096, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got == "" {
		t.Fatal("composeToolDescription() returned an empty description")
	}
}
