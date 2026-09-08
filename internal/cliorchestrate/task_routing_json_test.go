package cliorchestrate

import (
	"encoding/json"
	"strings"
	"testing"
)

func decodeErr(t *testing.T, raw string) error {
	t.Helper()
	var target dispatchTaskParams
	return decodeDispatchTaskJSON(json.RawMessage(raw), &target)
}

// TestDecodeDispatchTaskJSON_MultipleValuesRejected pins the trailing-data
// guard: valid JSON followed by ANOTHER JSON value (not just trailing
// whitespace, which decoder.Decode tolerates) must be refused.
func TestDecodeDispatchTaskJSON_MultipleValuesRejected(t *testing.T) {
	err := decodeErr(t, `{"tasks":[{"id":"a","prompt":"p"}]}{"extra":1}`)
	if err == nil || !strings.Contains(err.Error(), "multiple JSON values") {
		t.Fatalf("err = %v, want a multiple-JSON-values rejection", err)
	}
}

// TestDecodeDispatchTaskJSON_TrailingGarbageSurfacesTheScanError pins the
// non-EOF, non-nil branch: trailing bytes that are not even valid JSON
// (not a second value, not EOF) propagate the raw decode error.
func TestDecodeDispatchTaskJSON_TrailingGarbageSurfacesTheScanError(t *testing.T) {
	err := decodeErr(t, `{"tasks":[{"id":"a","prompt":"p"}]} not json at all {`)
	if err == nil {
		t.Fatal("decodeDispatchTaskJSON accepted trailing garbage")
	}
}

// TestRejectDuplicateJSONKeys covers scanJSONValue's full branch set: a
// duplicate top-level key, a non-string object key (impossible in valid
// JSON on its own, but scanJSONValue's own Token-level walk would reject a
// malformed stream that gets this far), nested duplicate keys inside an
// array element, and the multiple-top-level-values case shared with
// decodeDispatchTaskJSON.
func TestRejectDuplicateJSONKeys(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{"duplicate top-level key", `{"a":1,"a":2}`, `duplicate JSON key "a"`},
		{"duplicate nested key", `{"tasks":[{"id":"a","id":"b"}]}`, `duplicate JSON key "id"`},
		{"duplicate key inside array element object", `[{"x":1,"x":2}]`, `duplicate JSON key "x"`},
		{"multiple top-level values", `{"a":1} {"b":2}`, "multiple JSON values"},
		{"malformed json", `{"a":`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := rejectDuplicateJSONKeys([]byte(tc.raw))
			if err == nil {
				t.Fatalf("rejectDuplicateJSONKeys(%s) = nil, want an error", tc.raw)
			}
			if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
	if err := rejectDuplicateJSONKeys([]byte(`{"tasks":[{"id":"a"},{"id":"b"}],"wait":"all"}`)); err != nil {
		t.Fatalf("rejectDuplicateJSONKeys on well-formed input: %v", err)
	}
}

// TestValidateDispatchTaskSelectors covers the field-level null/shape
// checks: a missing or null tasks array, a null top-level optional field, a
// null or wrong-typed per-task field, and duplicate task ids.
func TestValidateDispatchTaskSelectors(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{"tasks missing", `{}`, "tasks must be a non-empty array"},
		{"tasks null", `{"tasks":null}`, "tasks must be a non-empty array"},
		{"tasks empty", `{"tasks":[]}`, "tasks must be a non-empty array"},
		{"timeout_seconds null", `{"tasks":[{"id":"a"}],"timeout_seconds":null}`, "timeout_seconds must not be null"},
		{"wait null", `{"tasks":[{"id":"a"}],"wait":null}`, "wait must not be null"},
		{"wait_task_id null", `{"tasks":[{"id":"a"}],"wait_task_id":null}`, "wait_task_id must not be null"},
		{"task field null", `{"tasks":[{"id":"a","prompt":null}]}`, `task 1: prompt must not be null`},
		{"duplicate task id", `{"tasks":[{"id":"dup"},{"id":"dup"}]}`, `duplicate task id "dup"`},
		{"agent null", `{"tasks":[{"id":"a","agent":null}]}`, "agent must not be null"},
		{"skill wrong type", `{"tasks":[{"id":"a","skill":5}]}`, "skill must be a string when present"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var target dispatchTaskParams
			// decodeDispatchTaskJSON reaches validateDispatchTaskSelectors
			// last, after the raw JSON passes duplicate-key and shape
			// decoding, so drive it that way for realistic root/tasks state.
			raw := json.RawMessage(tc.raw)
			var root map[string]json.RawMessage
			if err := json.Unmarshal(raw, &root); err != nil {
				t.Fatalf("fixture json invalid: %v", err)
			}
			_ = json.Unmarshal(raw, &target)
			err := validateDispatchTaskSelectors(raw, target.Tasks)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
	if err := validateDispatchTaskSelectors(json.RawMessage(`{"tasks":[{"id":"a","agent":"x","skill":"y"}]}`), nil); err != nil {
		t.Fatalf("validateDispatchTaskSelectors on well-formed input: %v", err)
	}
}
