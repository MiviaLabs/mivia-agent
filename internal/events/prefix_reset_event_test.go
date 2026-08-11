package events

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestPrefixResetEventIsSealedAndContentFree pins INV-68-7: the typed reset
// event carries only allowlisted category names and generation counters. The
// serialized payload must not contain prompt content, digest preimages,
// tool-schema bodies, tool-argument values, or an injected secret sentinel.
func TestPrefixResetEventIsSealedAndContentFree(t *testing.T) {
	event, err := NewPrefixResetEvent(PrefixResetEventParams{
		Categories:                []string{"model", "reasoning"},
		OutgoingModelGeneration:   1,
		IncomingModelGeneration:   2,
		OutgoingSurfaceGeneration: 1,
		IncomingSurfaceGeneration: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := event.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(raw)
	for _, forbidden := range []string{
		"secret-sentinel",
		`"content"`,
		`"input"`,
		`"output"`,
		"preimage",
		"schema_body",
		"arguments",
		"prompt_text",
	} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("typed event contains forbidden field/value %q: %s", forbidden, encoded)
		}
	}
	if len(event.Categories) != 2 || event.IncomingModelGeneration != 2 {
		t.Fatalf("event fields = %+v", event)
	}
}

func TestPrefixResetEventRejectsUnsealedConstruction(t *testing.T) {
	var event PrefixResetEvent
	if err := event.Validate(); err == nil {
		t.Fatal("zero-value event must fail validation")
	}
}

func TestPrefixResetEventRejectsEmptyCategories(t *testing.T) {
	if _, err := NewPrefixResetEvent(PrefixResetEventParams{}); err == nil {
		t.Fatal("no categories accepted")
	}
	if _, err := NewPrefixResetEvent(PrefixResetEventParams{Categories: []string{""}}); err == nil {
		t.Fatal("empty category name accepted")
	}
}

func TestPrefixResetEventRejectsUnknownCategory(t *testing.T) {
	if _, err := NewPrefixResetEvent(PrefixResetEventParams{Categories: []string{"model", "made_up_category"}}); err == nil {
		t.Fatal("category outside the allowlist accepted")
	}
}

func TestPrefixResetEventRejectsDuplicateCategories(t *testing.T) {
	if _, err := NewPrefixResetEvent(PrefixResetEventParams{Categories: []string{"model", "model"}}); err == nil {
		t.Fatal("duplicate category accepted")
	}
}

func TestPrefixResetEventRejectsOversizedCategories(t *testing.T) {
	tooLong := strings.Repeat("a", maxPrefixResetCategoryLen+1)
	if _, err := NewPrefixResetEvent(PrefixResetEventParams{Categories: []string{tooLong}}); err == nil {
		t.Fatal("oversized category name accepted")
	}
}

func TestPrefixResetEventRejectsControlCharacters(t *testing.T) {
	if _, err := NewPrefixResetEvent(PrefixResetEventParams{Categories: []string{"model\nforged"}}); err == nil {
		t.Fatal("control character in category accepted")
	}
}

// TestPrefixResetEventSerialization pins the Marshal/Unmarshal contract: the
// marshal side validates first, a valid payload round-trips and is re-sealed,
// malformed JSON is rejected, and wire payloads with empty, unknown, duplicate,
// oversized, or control-character categories are rejected through
// re-validation. The serialized event carries no content (INV-68-7).
func TestPrefixResetEventSerialization(t *testing.T) {
	sealed, err := NewPrefixResetEvent(PrefixResetEventParams{
		Categories:                []string{"tools", "tool_admission"},
		OutgoingModelGeneration:   3,
		IncomingModelGeneration:   3,
		OutgoingSurfaceGeneration: 2,
		IncomingSurfaceGeneration: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := MarshalPrefixResetEvent(sealed)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(raw)
	for _, forbidden := range []string{"secret-sentinel", `"content"`, "preimage", "schema_body", "arguments", "prompt_text"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("serialized event contains forbidden value %q: %s", forbidden, encoded)
		}
	}
	restored, err := UnmarshalPrefixResetEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Categories) != 2 || restored.Categories[0] != "tools" || restored.Categories[1] != "tool_admission" {
		t.Fatalf("round trip categories = %+v", restored.Categories)
	}
	if restored.IncomingSurfaceGeneration != 3 {
		t.Fatalf("round trip generations = %+v", restored)
	}
	if err := restored.Validate(); err != nil {
		t.Fatalf("round-tripped event is not re-sealed: %v", err)
	}
	if _, err := MarshalPrefixResetEvent(PrefixResetEvent{Categories: []string{"model"}}); err == nil {
		t.Fatal("unsealed event was accepted by Marshal")
	}
	if _, err := UnmarshalPrefixResetEvent([]byte("not json")); err == nil {
		t.Fatal("malformed JSON accepted by Unmarshal")
	}

	valid := `{"categories":["model"],"outgoing_model_generation":1,"incoming_model_generation":2}`
	for _, tc := range []struct {
		name  string
		wire  string
		valid bool
	}{
		{name: "valid single category", wire: valid, valid: true},
		{name: "empty categories", wire: `{"categories":[],"outgoing_model_generation":1,"incoming_model_generation":2}`},
		{name: "empty category name", wire: `{"categories":[""],"outgoing_model_generation":1,"incoming_model_generation":2}`},
		{name: "unknown category", wire: `{"categories":["bogus"],"outgoing_model_generation":1,"incoming_model_generation":2}`},
		{name: "duplicate category", wire: `{"categories":["model","model"],"outgoing_model_generation":1,"incoming_model_generation":2}`},
		{name: "oversized category", wire: `{"categories":["aaaaaaaaaaaaaaaaa"],"outgoing_model_generation":1,"incoming_model_generation":2}`},
		{name: "control character category", wire: "{\"categories\":[\"model\\nx\"],\"outgoing_model_generation\":1,\"incoming_model_generation\":2}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := UnmarshalPrefixResetEvent([]byte(tc.wire))
			if tc.valid {
				if err != nil {
					t.Fatalf("valid wire rejected: %v", err)
				}
				if len(got.Categories) != 1 || got.Categories[0] != "model" {
					t.Fatalf("categories = %+v", got.Categories)
				}
				return
			}
			if err == nil {
				t.Fatal("invalid wire accepted through re-validation")
			}
		})
	}
}
