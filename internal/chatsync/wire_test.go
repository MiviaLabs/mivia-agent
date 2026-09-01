package chatsync

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

type contractField struct {
	Kind     string `json:"kind"`
	Nullable bool   `json:"nullable"`
}

type contractStruct struct {
	DTO    string                   `json:"dto"`
	Fields map[string]contractField `json:"fields"`
}

type chatSessionsContract struct {
	KnownTypes []string                  `json:"knownTypes"`
	Structs    map[string]contractStruct `json:"structs"`
	Events     contractEvents            `json:"events"`
}

func loadChatSessionsContract(t *testing.T) chatSessionsContract {
	t.Helper()
	path := filepath.Join("..", "..", "api", "contracts", "chat-sessions.v1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var c chatSessionsContract
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	if len(c.KnownTypes) == 0 || len(c.Structs) == 0 {
		t.Fatalf("%s parsed to an empty contract", path)
	}
	return c
}

func goWireDTOStructs() map[string]any {
	return map[string]any{
		"session":       Session{},
		"eventItem":     EventItem{},
		"storedEvent":   StoredEvent{},
		"appendResult":  AppendResult{},
		"sessionInput":  SessionInput{},
		"nextInput":     NextInput{},
		"errorEnvelope": ErrorEnvelope{},
	}
}

// TestWireStructsMatchContractSnapshot asserts that every recorded struct has
// a Go model whose JSON field names, value kinds, and nullability match the contract.
func TestWireStructsMatchContractSnapshot(t *testing.T) {
	contract := loadChatSessionsContract(t)
	models := goWireDTOStructs()

	for name, want := range contract.Structs {
		model, ok := models[name]
		if !ok {
			t.Errorf("contract records struct %q but no Go type models it", name)
			continue
		}
		got := describeGoStruct(t, model)

		if diff := sortedKeys(got); !reflect.DeepEqual(diff, sortedKeys(want.Fields)) {
			t.Errorf("%s: JSON field names differ\n  go:       %v\n  contract: %v",
				name, diff, sortedKeys(want.Fields))
			continue
		}
		for field, wantField := range want.Fields {
			gotField := got[field]
			if gotField.Nullable != wantField.Nullable {
				t.Errorf("%s.%s: nullable = %v, contract records %v",
					name, field, gotField.Nullable, wantField.Nullable)
			}
			if !kindMatches(gotField.Kind, wantField.Kind) {
				t.Errorf("%s.%s: Go value kind %q does not satisfy contract kind %q",
					name, field, gotField.Kind, wantField.Kind)
			}
		}
	}

	for name := range models {
		if _, ok := contract.Structs[name]; !ok {
			t.Errorf("Go models wire struct %q that the contract does not record", name)
		}
	}
}

// TestKnownWireTypesMatchContractSnapshot checks that the Go type constants match
// the recorded known types list.
func TestKnownWireTypesMatchContractSnapshot(t *testing.T) {
	contract := loadChatSessionsContract(t)
	got := append([]string(nil), KnownWireTypes...)
	sort.Strings(got)
	want := append([]string(nil), contract.KnownTypes...)
	sort.Strings(want)

	if !reflect.DeepEqual(got, want) {
		t.Errorf("KnownWireTypes differ from contract\n  got:  %v\n  want: %v", got, want)
	}
}

// TestTypeConstantsAreSSESafe asserts that all event type constants can be safe SSE event names:
// no carriage return, newline, max 100 chars, and start with mivia.chat.v1.
func TestTypeConstantsAreSSESafe(t *testing.T) {
	for _, typeStr := range KnownWireTypes {
		if strings.ContainsAny(typeStr, "\r\n") {
			t.Errorf("type constant %q contains newline/CR characters", typeStr)
		}
		if len(typeStr) > 100 {
			t.Errorf("type constant %q exceeds 100 chars (len=%d)", typeStr, len(typeStr))
		}
		if !strings.HasPrefix(typeStr, "mivia.chat.v1.") {
			t.Errorf("type constant %q lacks 'mivia.chat.v1.' prefix", typeStr)
		}
	}
}

// TestPayloadsEmbedEnvelope verifies that each wire payload struct embeds Envelope.
// It walks the event table rather than its own list: a hand-kept list here
// would be one more copy of the vocabulary that a new type could miss.
func TestPayloadsEmbedEnvelope(t *testing.T) {
	for _, spec := range WireEventSpecs() {
		typ := reflect.TypeOf(spec.Payload)
		field, ok := typ.FieldByName("Envelope")
		if !ok || !field.Anonymous {
			t.Errorf("%s does not embed Envelope anonymously", typ.Name())
		}
	}
}

// TestPayloadJSONSerialization verifies that payloads encode envelope fields properly.
func TestPayloadJSONSerialization(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	p := TurnStartedPayload{
		Envelope: Envelope{
			V:    1,
			At:   now,
			Turn: "turn:1",
		},
		Text: "hello world",
	}

	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if m["v"] != float64(1) {
		t.Errorf("v = %v, want 1", m["v"])
	}
	if m["turn"] != "turn:1" {
		t.Errorf("turn = %v, want turn:1", m["turn"])
	}
	if m["text"] != "hello world" {
		t.Errorf("text = %v, want hello world", m["text"])
	}
}

func describeGoStruct(t *testing.T, model any) map[string]contractField {
	t.Helper()
	typ := reflect.TypeOf(model)
	out := map[string]contractField{}
	for i := range typ.NumField() {
		field := typ.Field(i)
		tag := field.Tag.Get("json")
		if tag == "" || tag == "-" {
			t.Fatalf("%s.%s has no json tag", typ.Name(), field.Name)
		}
		name, _, _ := strings.Cut(tag, ",")
		fieldType := field.Type
		nullable := fieldType.Kind() == reflect.Pointer
		if nullable {
			fieldType = fieldType.Elem()
		}
		out[name] = contractField{Kind: goKindName(fieldType), Nullable: nullable}
	}
	return out
}

func goKindName(t reflect.Type) string {
	if t == reflect.TypeOf(json.RawMessage{}) {
		return "string|array|object"
	}
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "bool"
	case reflect.Int, reflect.Int64, reflect.Float64:
		return "number"
	case reflect.Struct, reflect.Map:
		return "object"
	case reflect.Slice:
		return "array"
	default:
		return t.Kind().String()
	}
}

func kindMatches(got, want string) bool {
	if got == want {
		return true
	}
	for _, alt := range strings.Split(want, "|") {
		for _, gotAlt := range strings.Split(got, "|") {
			if strings.TrimSpace(gotAlt) == strings.TrimSpace(alt) {
				return true
			}
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
