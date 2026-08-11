package subagents

// Deterministic in-process fuzz coverage for truncationCorrectiveMessage plus
// the two white-box properties the black-box tests cannot reach: the injected
// redactor is applied on the truncation path, and an oversized schema contract
// is kept WHOLE (never-truncate-schema) even when it exceeds the soft cap.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/jschema"
)

// truncationTestSchema is the package-internal analogue of schemaObject() in
// the external test package: a small object schema with one required boolean.
func truncationTestSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ok": map[string]any{"type": "boolean"},
		},
		"required":             []any{"ok"},
		"additionalProperties": false,
	}
}

func TestTruncationCorrectiveAppliesRedaction(t *testing.T) {
	injected := func(s string) string { return "[r]:" + s }
	msg := truncationCorrectiveMessage(truncationTestSchema(), injected)
	if !strings.HasPrefix(msg, "[r]:") {
		t.Fatalf("redaction not applied on the truncation path: %q", msg)
	}
}

func TestTruncationCorrectiveKeepsOversizedContractWhole(t *testing.T) {
	// A schema whose model contract exceeds MaxCorrectiveBytes must keep the
	// FULL contract substring in the corrective message (never-truncate-schema,
	// mirroring jschema_paths_test.go soft-cap semantics): the prefix yields
	// before the contract does.
	props := map[string]any{}
	for i := 0; i < 40; i++ {
		props[fmt.Sprintf("field_%02d", i)] = map[string]any{
			"type": "string", "minLength": 5, "pattern": "^[a-z]+$",
		}
	}
	schema := map[string]any{"type": "object", "properties": props}
	contract := jschema.ModelSchemaContract(schema)
	if contract == "" {
		t.Fatal("contract unexpectedly empty")
	}
	if len(contract) <= jschema.MaxCorrectiveBytes {
		t.Fatalf("test schema contract %d bytes must exceed %d", len(contract), jschema.MaxCorrectiveBytes)
	}
	msg := truncationCorrectiveMessage(schema, nil)
	if !strings.Contains(msg, contract) {
		t.Fatalf("contract truncated by corrective (len=%d, contract %d): %.120s…", len(msg), len(contract), msg)
	}
	if !utf8.ValidString(msg) {
		t.Fatalf("corrective is invalid UTF-8: %q", msg)
	}
}

// FuzzTruncationCorrectiveMessage pins the message-builder invariants across
// arbitrary schema input: valid UTF-8, the full schema contract present when
// non-empty, and the byte cap holding whenever prefix+contract fit. The seed
// corpus runs under the fixed go-test gate (repo convention:
// coordinator/retry_test.go FuzzEffectiveBackoff); a standalone
// `go test -fuzz -fuzztime` engine gate is not practical because the workflow
// evidence-gate verifiers are fixed and the constraints forbid workflow
// changes.
func FuzzTruncationCorrectiveMessage(f *testing.F) {
	smallRaw, _ := json.Marshal(truncationTestSchema())
	f.Add(smallRaw)
	strRaw, _ := json.Marshal(map[string]any{"type": "string"})
	f.Add(strRaw)
	f.Add([]byte{})
	f.Add([]byte(`{"type":`)) // malformed JSON → nil schema after unmarshal
	props := map[string]any{}
	for i := 0; i < 40; i++ {
		props[fmt.Sprintf("field_%02d", i)] = map[string]any{
			"type": "string", "minLength": 5, "pattern": "^[a-z]+$",
		}
	}
	oversized, _ := json.Marshal(map[string]any{"type": "object", "properties": props})
	f.Add(oversized)

	f.Fuzz(func(t *testing.T, raw []byte) {
		var schema map[string]any
		_ = json.Unmarshal(raw, &schema) // malformed bytes → nil schema
		msg := truncationCorrectiveMessage(schema, nil)
		if !utf8.ValidString(msg) {
			t.Fatalf("truncationCorrectiveMessage emitted invalid UTF-8: %q", msg)
		}
		contract := jschema.ModelSchemaContract(schema)
		if contract != "" && !strings.Contains(msg, contract) {
			t.Fatalf("contract not kept whole: %q", msg)
		}
		if len(msg) > jschema.MaxCorrectiveBytes {
			// Over the soft cap is legal ONLY when the contract alone (plus
			// the minimum prefix) does not fit; the contract must still be
			// whole, which the check above already guarantees.
			prefix := "Your previous reply was cut off by the output limit. " +
				"Reply again with ONLY the required JSON, as concise as possible."
			if len(prefix)+len(contract) <= jschema.MaxCorrectiveBytes {
				t.Fatalf("corrective = %d bytes, want <= %d when prefix+contract fit", len(msg), jschema.MaxCorrectiveBytes)
			}
		}
	})
}
