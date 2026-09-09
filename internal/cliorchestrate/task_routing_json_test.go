package cliorchestrate

// Tests for the dispatch_tasks request decode and its name guards, which live
// in task_request_decode.go.
//
// The file keeps its old name on purpose. Renaming it to match reads to git as
// a delete plus an add - the content similarity lands just under git's 50%
// rename threshold - and check_test_quality then sees every test in it as a
// removed test function. Splitting the rename into its own commit does not
// help either: the pre-push sweep gates the aggregate push range, where the
// rename and the edits are one change again. Allowlisting those would put four entries in
// .mivia/policy/test-skips.json claiming a removal was approved for tests that
// are still here, which is a worse thing to leave behind than a filename that
// no longer matches its subject.

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

// TestRejectTrailingJSON_SurfacesTheSecondDecodeError pins the branch the
// trailing-garbage case does NOT reach: bytes after a complete value that are
// not a second value and not EOF, so the SECOND Decode - the guard's own -
// returns a syntax error rather than io.EOF.
//
// "not json at all {" was the old fixture and proved nothing: it fails the
// first Decode inside validateDispatchTaskSelectors, so neutering
// rejectTrailingJSON's error return left the test green. "tru" is the shape
// that reaches the second Decode: valid JSON, then a token that starts a
// value and cannot finish one.
func TestRejectTrailingJSON_SurfacesTheSecondDecodeError(t *testing.T) {
	if err := rejectTrailingJSON([]byte(`{"a":1} tru`)); err == nil {
		t.Fatal("rejectTrailingJSON accepted a truncated second value")
	}
	// Through the public entry point too, so the wiring is pinned, not just
	// the helper.
	if err := decodeErr(t, `{"tasks":[{"id":"a","prompt":"p"}]} tru`); err == nil {
		t.Fatal("decodeDispatchTaskJSON accepted a truncated second value")
	}
}

// TestDecodeDispatchTaskJSON_WrongTypedFieldIsRefused pins the decode's own
// error return. A declared field carrying the wrong JSON type is the one bad
// shape none of the name guards can see: it is not null, not a near miss, and
// only agent/skill have a shape check of their own. Without this, neutering
// the decode's error return accepted {"wait":5} as Wait:"".
func TestDecodeDispatchTaskJSON_WrongTypedFieldIsRefused(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`{"tasks":[{"id":"a","prompt":"p"}],"wait":5}`, "wait"},
		{`{"tasks":[{"id":"a","prompt":"p"}],"timeout_seconds":"soon"}`, "timeout_seconds"},
		{`{"tasks":[{"id":"a","prompt":5}]}`, "prompt"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			err := decodeErr(t, tc.raw)
			if err == nil {
				t.Fatalf("decodeDispatchTaskJSON accepted %s", tc.raw)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want the offending field %q named", err, tc.want)
			}
		})
	}
}

// TestRejectTrailingJSON covers the one-value guard. Duplicate-key detection
// used to live here and recursed to every depth; it now belongs to
// duplicateFoldedKey at the two levels that decode into structs, so a
// duplicate inside a pass-through output_schema no longer refuses the batch
// (near_miss_field_test.go pins both halves).
func TestRejectTrailingJSON(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{"multiple top-level values", `{"a":1} {"b":2}`, "multiple JSON values"},
		{"malformed json", `{"a":`, ""},
		{"truncated mid-key", `{"a":1,`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := rejectTrailingJSON([]byte(tc.raw))
			if err == nil {
				t.Fatalf("rejectTrailingJSON(%s) = nil, want an error", tc.raw)
			}
			if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
	if err := rejectTrailingJSON([]byte(`{"tasks":[{"id":"a"},{"id":"b"}],"wait":"all"}`)); err != nil {
		t.Fatalf("rejectTrailingJSON on well-formed input: %v", err)
	}
	// A duplicate key is no longer this function's business at any depth.
	if err := rejectTrailingJSON([]byte(`{"a":1,"a":2}`)); err != nil {
		t.Fatalf("rejectTrailingJSON on a duplicate key: %v; the fold check owns that "+
			"decision now, at the levels that decode into structs", err)
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

// TestValidateDispatchTaskSelectors_MalformedTopLevelJSONSurfaces pins the
// json.Unmarshal(args, &root) guard directly: production only reaches this
// function with already-validated JSON (via decodeDispatchTaskJSON), so
// this exercises the guard at the function's own boundary.
func TestValidateDispatchTaskSelectors_MalformedTopLevelJSONSurfaces(t *testing.T) {
	if err := validateDispatchTaskSelectors(json.RawMessage(`not json`), nil); err == nil {
		t.Fatal("validateDispatchTaskSelectors accepted malformed top-level JSON")
	}
}

// TestValidateDispatchTaskSelectors_NonObjectTaskElementSurfaces pins the
// per-task json.Unmarshal(raw, &fields) guard: a tasks array element that
// is syntactically valid JSON but not an object (a bare string here) fails
// the map decode, distinct from a top-level JSON syntax error.
func TestValidateDispatchTaskSelectors_NonObjectTaskElementSurfaces(t *testing.T) {
	err := validateDispatchTaskSelectors(json.RawMessage(`{"tasks":["not an object"]}`), nil)
	if err == nil {
		t.Fatal("validateDispatchTaskSelectors accepted a non-object task element")
	}
}

// TestDuplicateFoldedKeyOnBytesItsCallersNeverSend covers the guard's bail-out
// paths directly.
//
// Both production callers hand it bytes that json.Unmarshal already parsed, so
// none of these shapes can reach it today - which is exactly why the function
// keeps the guards rather than assuming an object. The contract is to answer
// found/not-found for arbitrary bytes: a future caller passing unparsed input
// must get false, not a panic. That contract is testable, so it is tested
// rather than written off as unreachable in the coverage residue.
func TestDuplicateFoldedKeyOnBytesItsCallersNeverSend(t *testing.T) {
	for _, raw := range []string{
		`[1,2]`,      // not an object: the first token is not '{'
		`"a string"`, // not an object either
		`{"a"`,       // truncated mid-key: Token() errors
		`{"a":tru`,   // truncated value: the skip Decode errors
		`{"a":1,`,    // truncated after a pair
		``,           // no tokens at all
		// An ARRAY whose elements happen to be two spellings of one declared
		// field. This is the shape that distinguishes returning early on a
		// non-object from walking one as though it were an object: walked, its
		// elements read as alternating keys and values and the two spellings
		// "collide", reporting a duplicate in a document that has no fields at
		// all.
		`["wait","x","WAIT","y"]`,
	} {
		t.Run(raw, func(t *testing.T) {
			first, second, found := duplicateFoldedKey(json.RawMessage(raw), declaredRequestFields)
			if found {
				t.Fatalf("duplicateFoldedKey(%q) reported a duplicate (%q, %q); malformed "+
					"input has no two spellings of one field", raw, first, second)
			}
		})
	}

	// The positive control, so the table above cannot pass by the function
	// being broken outright.
	if _, _, found := duplicateFoldedKey(json.RawMessage(`{"wait":"run","WAIT":"none"}`), declaredRequestFields); !found {
		t.Fatal("duplicateFoldedKey missed two spellings of a declared field")
	}
}
