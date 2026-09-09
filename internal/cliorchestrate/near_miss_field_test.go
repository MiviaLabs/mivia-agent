package cliorchestrate

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// A near-miss spelling of a declared field must be refused, not ignored.
//
// Ignoring unknown fields is right for a decoration the tool does not read
// ("description", "priority"). It is wrong for "dependsOn", because that IS a
// field the tool reads - under a spelling encoding/json does not match. The
// decoder's case-insensitive matching does not bridge the underscore, so
// "dependsOn", "DependsOn" and "depends-on" all decode to an EMPTY DependsOn:
// the batch then runs a dependent task concurrently with the task it was
// declared to wait for, and no surface reports anything wrong. Strict decoding
// used to catch this as an unknown field.
//
// The rule: a field that matches a declared name case-insensitively is fine
// (encoding/json accepts it, so "Prompt" works). A field that does NOT match
// any declared name, but whose punctuation-stripped form does, is a
// misspelling of a field the tool reads - refuse it and name the spelling.

// TestNearMissTaskFieldsAreRejected covers every declared task field whose
// name carries an underscore, since those are the ones a model can miss.
func TestNearMissTaskFieldsAreRejected(t *testing.T) {
	for _, tc := range []struct{ field, want string }{
		{`"dependsOn":["a"]`, "depends_on"},
		{`"DependsOn":["a"]`, "depends_on"},
		{`"depends-on":["a"]`, "depends_on"},
		{`"outputSchema":{"type":"object"}`, "output_schema"},
		{`"inputSchema":{"type":"object"}`, "input_schema"},
		{`"timeoutSeconds":30`, "timeout_seconds"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			args := `{"tasks":[{"id":"x","prompt":"work",` + tc.field + `}]}`
			_, err := routingTools(t).Execute(context.Background(), json.RawMessage(args))
			if err == nil {
				t.Fatalf("%s was ignored; the field the tool reads (%s) stayed empty and "+
					"the task ran without it", tc.field, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want the correct spelling %q named", err, tc.want)
			}
		})
	}
}

// TestNearMissTopLevelFieldsAreRejected pins the same rule on the request
// object: a misspelled wait_task_id silently changed which task was waited on.
func TestNearMissTopLevelFieldsAreRejected(t *testing.T) {
	for _, tc := range []struct{ field, want string }{
		{`"waitTaskId":"x"`, "wait_task_id"},
		{`"timeoutSeconds":30`, "timeout_seconds"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			args := `{"tasks":[{"id":"x","prompt":"work"}],` + tc.field + `}`
			_, err := routingTools(t).Execute(context.Background(), json.RawMessage(args))
			if err == nil {
				t.Fatalf("%s was ignored", tc.field)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want the correct spelling %q named", err, tc.want)
			}
		})
	}
}

// TestCaseVariantsOfDeclaredFieldsStillWork is the half the near-miss check
// must not eat: encoding/json matches field names case-insensitively, so a
// capitalised spelling of a single-word field decodes correctly and must NOT
// be refused as a near miss.
func TestCaseVariantsOfDeclaredFieldsStillWork(t *testing.T) {
	args := `{"tasks":[{"ID":"x","Prompt":"work"}],"Wait":"run"}`
	out, err := routingTools(t).Execute(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("Execute error = %v; encoding/json accepts these spellings, so the "+
			"near-miss check must too", err)
	}
	if !strings.Contains(out, "oneshot-ok") {
		t.Fatalf("Execute output = %q, want the task's result", out)
	}
}

// TestDecorationsSurviveTheNearMissCheck guards the permissiveness the
// near-miss rule is carved out of: a field that resembles no declared name is
// still ignored.
func TestDecorationsSurviveTheNearMissCheck(t *testing.T) {
	args := `{"tasks":[{"id":"x","prompt":"work","description":"note","priority":2,"notes":["a"]}],"wait":"run","reason":"fan out"}`
	if _, err := routingTools(t).Execute(context.Background(), json.RawMessage(args)); err != nil {
		t.Fatalf("Execute error = %v, want decorations ignored", err)
	}
}

// TestFieldGuardsAgreeWithTheSchema is the rot gate for the two hand-kept
// lists in task_request_decode.go. Both are name-matched against fields the schema
// declares, so a future property named "model" (reserved) or one added to the
// schema but not to declaredTaskFields (near-miss check goes blind) would be
// advertised and then refused, or read and then silently droppable. Neither
// failure is visible without this check.
func TestFieldGuardsAgreeWithTheSchema(t *testing.T) {
	items := routingTools(t).Parameters()["properties"].(map[string]any)["tasks"].(map[string]any)["items"].(map[string]any)
	declared := items["properties"].(map[string]any)

	for name := range declared {
		if reserved := reservedSelectorFor(name); reserved != "" {
			t.Errorf("the schema advertises %q while reservedTaskSelectors refuses it: "+
				"every call using the advertised field would fail", name)
		}
		if !slices.Contains(declaredTaskFields, name) {
			t.Errorf("the schema advertises %q but declaredTaskFields omits it, so a "+
				"misspelling of it decodes to nothing and is never caught", name)
		}
	}
	for _, name := range declaredTaskFields {
		if _, ok := declared[name]; !ok {
			t.Errorf("declaredTaskFields carries %q, which the schema does not advertise", name)
		}
	}

	// The request object's own fields, same rule.
	root := routingTools(t).Parameters()["properties"].(map[string]any)
	for name := range root {
		if !slices.Contains(declaredRequestFields, name) {
			t.Errorf("the request schema advertises %q but declaredRequestFields omits it", name)
		}
	}
	for _, name := range declaredRequestFields {
		if _, ok := root[name]; !ok {
			t.Errorf("declaredRequestFields carries %q, which the request schema does not advertise", name)
		}
	}
}

// TestFieldRejectionIsDeterministic pins the walk order. A task carrying two
// misspelled fields used to name whichever one Go's map iteration reached
// first, so the same call reported a different error on each retry and no
// operator could reproduce what a model saw.
func TestFieldRejectionIsDeterministic(t *testing.T) {
	args := `{"tasks":[{"id":"x","prompt":"work","dependsOn":["a"],"outputSchema":{}}]}`
	first := ""
	for i := 0; i < 40; i++ {
		_, err := routingTools(t).Execute(context.Background(), json.RawMessage(args))
		if err == nil {
			t.Fatal("two misspelled fields were accepted")
		}
		if first == "" {
			first = err.Error()
			continue
		}
		if err.Error() != first {
			t.Fatalf("run %d reported %q, first run reported %q; the error must not "+
				"depend on map iteration order", i, err, first)
		}
	}
}

// TestWhitespacePaddedFieldsAreRejected closes the fail-open branch of the
// near-miss rule. nearMissOf trimmed the supplied name before comparing it to
// a declared one, so " depends_on" compared EQUAL and was waved through as
// "json decodes this" - but encoding/json does not trim key names, so it
// decoded into nothing. A padded " agent" re-routed the task to the default,
// and a padded " depends_on" dropped the DAG edge: the same silence the
// near-miss rule exists to end, reached through a different door.
func TestWhitespacePaddedFieldsAreRejected(t *testing.T) {
	for _, tc := range []struct{ field, want string }{
		{`" depends_on":["a"]`, "depends_on"},
		{`"agent ":"researcher"`, "agent"},
		{`" budget":5`, "budget"},
		{`" output_schema":{}`, "output_schema"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			args := `{"tasks":[{"id":"x","prompt":"work",` + tc.field + `}]}`
			_, err := routingTools(t).Execute(context.Background(), json.RawMessage(args))
			if err == nil {
				t.Fatalf("%s was ignored; encoding/json drops a padded key, so the field "+
					"the tool reads (%s) stayed empty", tc.field, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want the correct spelling %q named", err, tc.want)
			}
		})
	}
}

// TestMisspelledFieldBeatsTheNullCheck orders the two failure messages: EVERY
// key is name-checked before ANY key is null-checked. A task carrying both a
// misspelling and a null tells the model about the misspelling, because that
// is the fault it cannot see - the null is honest about itself.
//
// Two DIFFERENT keys, deliberately. A single misspelled key set to null cannot
// prove the ordering: the null check reads declared names only, so it never
// fires on "dependsOn" whichever loop runs first. The ordering is observable
// only when one key would fail each check, and "budget" sorts before
// "dependsOn" so a per-key walk would report the null first.
func TestMisspelledFieldBeatsTheNullCheck(t *testing.T) {
	args := `{"tasks":[{"id":"x","prompt":"work","budget":null,"dependsOn":["a"]}]}`
	_, err := routingTools(t).Execute(context.Background(), json.RawMessage(args))
	if err == nil {
		t.Fatal("neither the misspelling nor the null was reported")
	}
	if !strings.Contains(err.Error(), "depends_on") {
		t.Fatalf("error = %v, want the spelling hint; a null complaint sends the model "+
			"to fix a value while the misspelled field stays invisible", err)
	}
}

// TestDeclaredFieldsMatchTheDecodedStructs closes the rot gate's blind spot.
// TestFieldGuardsAgreeWithTheSchema pins the lists against the ADVERTISED
// schema; this pins them against what actually decodes. A struct field added
// without a schema property would otherwise be read by the tool while sitting
// outside the near-miss list, so a misspelling of it would decode to nothing
// and pass silently - the exact failure the near-miss check exists to stop.
func TestDeclaredFieldsMatchTheDecodedStructs(t *testing.T) {
	for _, tc := range []struct {
		name     string
		typ      reflect.Type
		declared []string
	}{
		{"task", reflect.TypeOf(dispatchTaskParam{}), declaredTaskFields},
		{"request", reflect.TypeOf(dispatchTaskParams{}), declaredRequestFields},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for i := 0; i < tc.typ.NumField(); i++ {
				tag := tc.typ.Field(i).Tag.Get("json")
				name, _, _ := strings.Cut(tag, ",")
				if name == "" || name == "-" {
					continue
				}
				if !slices.Contains(tc.declared, name) {
					t.Errorf("%s decodes %q, which the declared list omits: a misspelling "+
						"of it would decode to nothing and never be caught", tc.typ, name)
				}
			}
		})
	}
}

// TestNullDecorationsAreIgnored closes the last door onto the failure this
// whole change exists to remove. The per-task null check walked EVERY key in
// the task object, not the declared ones - harmless while DisallowUnknownFields
// rejected undeclared names first, and reachable the moment the decode went
// permissive. A model writing "description": null (the shape a decoration with
// no value takes) refused a batch of up to 16 tasks and was told to fix the
// value of a field the tool never reads.
func TestNullDecorationsAreIgnored(t *testing.T) {
	args := `{"tasks":[{"id":"x","prompt":"work","description":null,"notes":null}],"wait":"run"}`
	out, err := routingTools(t).Execute(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("Execute error = %v; a null decoration must be ignored like any "+
			"other unread field", err)
	}
	if !strings.Contains(out, "oneshot-ok") {
		t.Fatalf("Execute output = %q, want the task's result", out)
	}
}

// TestNullOnADeclaredFieldIsStillRefused is the half the fix must not eat: a
// null on a field the tool DOES read stays an error, because the model meant
// to set it and the tool would silently use the zero value.
func TestNullOnADeclaredFieldIsStillRefused(t *testing.T) {
	// "Agent" and "DEPENDS_ON" included: encoding/json resolves those onto the
	// declared field and nils it, so the null check has to resolve them too.
	for _, field := range []string{"agent", "depends_on", "timeout_seconds", "Agent", "DEPENDS_ON"} {
		t.Run(field, func(t *testing.T) {
			args := `{"tasks":[{"id":"x","prompt":"work","` + field + `":null}]}`
			_, err := routingTools(t).Execute(context.Background(), json.RawMessage(args))
			if err == nil {
				t.Fatalf("a null %s was accepted", field)
			}
			// "must not be null" as well as the field: the near-miss guard also
			// names the field, so the field alone cannot say which one fired.
			if !strings.Contains(err.Error(), "must not be null") {
				t.Fatalf("error = %v, want the null refusal", err)
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(field)) {
				t.Fatalf("error = %v, want %q named", err, field)
			}
		})
	}
}

// TestCaseVariantTasksKeyDecodes extends the case-variant rule to the one
// field whose presence the validator looked up with an exact-cased index.
// encoding/json decoded {"Tasks":[...]} into a populated Tasks slice, and the
// validator then answered "tasks must be a non-empty array" - a statement that
// was simply false, with no spelling hint to correct toward.
func TestCaseVariantTasksKeyDecodes(t *testing.T) {
	args := `{"Tasks":[{"id":"x","prompt":"work"}],"Wait":"run"}`
	out, err := routingTools(t).Execute(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("Execute error = %v; encoding/json decodes this key, so the "+
			"validator must find it too", err)
	}
	if !strings.Contains(out, "oneshot-ok") {
		t.Fatalf("Execute output = %q, want the task's result", out)
	}
}

// TestExoticSeparatorsAreRejected closes the near-miss rule's separator list
// as a CLASS. Hand-listing separators (_ - space tab newline) left "\r", a
// non-breaking space and "." open, and each one decodes into nothing exactly
// like the spellings the rule already refuses - "depends_on\r" drops the DAG
// edge just as thoroughly as "dependsOn". The rule now keeps only letters and
// digits, so there is no next separator to miss.
func TestExoticSeparatorsAreRejected(t *testing.T) {
	for _, key := range []string{"depends_on\r", "depends_on ", "depends.on", "depends on", "depends‑on"} {
		t.Run(key, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{
				"tasks": []any{map[string]any{"id": "x", "prompt": "work", key: []string{"a"}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, execErr := routingTools(t).Execute(context.Background(), raw)
			if execErr == nil {
				t.Fatalf("%q was ignored; encoding/json drops it, so depends_on stayed empty", key)
			}
			if !strings.Contains(execErr.Error(), "depends_on") {
				t.Fatalf("error = %v, want the correct spelling named", execErr)
			}
		})
	}
}

// TestCaseVariantDuplicateKeysAreRefused closes a bypass of every per-task
// guard in this file.
//
// encoding/json resolves keys case-insensitively and lets the LAST one win, so
// {"tasks":[...],"TASKS":[...]} decodes the second array. The validator
// re-unmarshals the raw arguments into its own map and looked up "tasks"
// exact-first, so it walked the FIRST array: the reserved-selector check, the
// near-miss check and the null check all ran over objects that were never
// dispatched, while the array that did run carried whatever the model liked.
// A byte-comparing scan could not catch it: "tasks" and "TASKS" are two keys
// as bytes and one field to the decoder.
//
// The same shape inside a task object nils a slice: "DEPENDS_ON":["t1"] with a
// later "Depends_On":null decodes to an empty DependsOn while the null check
// reads the non-null sibling.
func TestCaseVariantDuplicateKeysAreRefused(t *testing.T) {
	for name, args := range map[string]string{
		"decoy task array": `{"tasks":[{"id":"a","prompt":"good"}],"TASKS":[{"id":"b","prompt":"bad","dependsOn":["a"],"handler":"x"}]}`,
		"nulled dependency": `{"tasks":[{"id":"a","prompt":"p"},` +
			`{"id":"b","prompt":"q","DEPENDS_ON":["a"],"Depends_On":null}]}`,
		"decoy wait": `{"tasks":[{"id":"a","prompt":"p"}],"wait":"run","WAIT":"none"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := routingTools(t).Execute(context.Background(), json.RawMessage(args))
			if err == nil {
				t.Fatal("two spellings of one field were accepted; the guards ran over " +
					"a value the decoder discarded")
			}
			if !strings.Contains(err.Error(), "resolve to one field") {
				t.Fatalf("error = %v, want the two spellings named", err)
			}
		})
	}
}

// TestNestedSchemaKeepsItsOwnDuplicates is the exact-duplicate half of the
// same scoping rule. A recursive duplicate-key scan refused the WHOLE batch
// over two identical keys inside an output_schema - JSON the tool passes
// through and never reads - which is the decoration-refuses-the-batch failure
// this design exists to remove, at a depth the fold check deliberately
// excludes. Duplicates that matter are the ones at the levels that decode into
// structs, and duplicateFoldedKey owns those.
func TestNestedSchemaKeepsItsOwnDuplicates(t *testing.T) {
	args := `{"tasks":[{"id":"x","prompt":"work","output_schema":` +
		`{"type":"object","type":"object"}}],"wait":"run"}`
	if _, err := routingTools(t).Execute(context.Background(), json.RawMessage(args)); err != nil {
		t.Fatalf("Execute error = %v; a duplicate key inside pass-through JSON is not "+
			"a duplicate struct field", err)
	}
}

// TestStructLevelDuplicatesStillRefused holds the other side: an exact
// duplicate at a level that DOES decode into a struct stays refused, now by
// the fold check rather than by a recursive scan.
func TestStructLevelDuplicatesStillRefused(t *testing.T) {
	for name, args := range map[string]string{
		"request level": `{"tasks":[{"id":"a","prompt":"p"}],"wait":"run","wait":"none"}`,
		"task level":    `{"tasks":[{"id":"a","id":"b","prompt":"p"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := routingTools(t).Execute(context.Background(), json.RawMessage(args))
			if err == nil {
				t.Fatal("a duplicate key on a decoded object was accepted")
			}
			if !strings.Contains(err.Error(), "resolve to one field") {
				t.Fatalf("error = %v, want the duplicate refusal", err)
			}
		})
	}
}

// TestNestedSchemaKeepsItsOwnCaseVariants scopes the fold check. An
// output_schema is arbitrary caller JSON that the tool passes through without
// decoding into a struct, so "Type" and "type" inside it are two honest
// members, not two spellings of one field. Folding keys at every depth would
// refuse schemas the tool never reads - the whole-batch refusal this work
// exists to remove.
func TestNestedSchemaKeepsItsOwnCaseVariants(t *testing.T) {
	args := `{"tasks":[{"id":"x","prompt":"work","output_schema":` +
		`{"type":"object","Type":"not a duplicate here","properties":{"a":{"type":"string"}}}}],"wait":"run"}`
	if _, err := routingTools(t).Execute(context.Background(), json.RawMessage(args)); err != nil {
		t.Fatalf("Execute error = %v; case variants inside a pass-through schema are "+
			"not duplicate struct fields", err)
	}
}

// TestUnicodeFoldDuplicatesAreRefused pins the duplicate check against
// encoding/json's ACTUAL fold, which is not strings.ToLower.
//
// json folds U+017F (ſ) onto "s" and U+212A (K) onto "k", so "taskſ" and
// "tasks" are one field to the decoder and the last one wins. ToLower leaves ſ
// alone, so the check saw two unrelated keys, the decoy array was dispatched,
// and every guard - reserved selectors, near-miss spellings, nulls, duplicate
// ids - ran over the array that never left the building. That is the bypass
// duplicateFoldedKey exists to close, reopened by one rune.
func TestUnicodeFoldDuplicatesAreRefused(t *testing.T) {
	for name, tc := range map[string]struct{ args, first, second string }{
		"decoy tasks array": {`{"tasks":[{"id":"a","prompt":"p"}],` +
			`"taskſ":[{"id":"b","prompt":"p","handler":"x","dependsOn":["a"]}]}`, "tasks", "taskſ"},
		"decoy task field": {`{"tasks":[{"id":"a","prompt":"p","skill":"good","ſkill":"decoy"}]}`, "skill", "ſkill"},
		"kelvin sign":      {`{"tasks":[{"id":"a","prompt":"p","skill":"good","sKill":"decoy"}]}`, "skill", "sKill"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := routingTools(t).Execute(context.Background(), json.RawMessage(tc.args))
			if err == nil {
				t.Fatal("two spellings json folds together were accepted; the guards ran " +
					"over a value the decoder discarded")
			}
			for _, want := range []string{"resolve to one field", tc.first, tc.second} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %v, want it to contain %q", err, want)
				}
			}
		})
	}
}

// TestFoldedSpellingsStillDecode is the other side of the same rule: a single
// folded spelling with no rival is a field encoding/json really does decode,
// so it must be accepted, not reported as a misspelling.
func TestFoldedSpellingsStillDecode(t *testing.T) {
	args := `{"tasks":[{"id":"x","prompt":"work","ſkill":""}],"wait":"run"}`
	if _, err := routingTools(t).Execute(context.Background(), json.RawMessage(args)); err != nil {
		t.Fatalf("Execute error = %v; json decodes this key onto skill, so the guards "+
			"must resolve it the same way", err)
	}
}

// TestHomoglyphMisspellingIsCaught covers the lookalike that does NOT fold. A
// Cyrillic "е" is a different letter to encoding/json - the key decodes into
// nothing - while being indistinguishable on screen. The squash filter dropped
// the rune instead of accounting for it, so "depеnds_on" squashed one letter
// short of "depends_on" and read as a decoration.
//
// depends_on, not prompt: a homoglyph prompt is caught anyway by the
// prompt-required guard, so testing that would pass without the rule under
// test. Nothing else catches a dropped dependency - the task just runs early.
func TestHomoglyphMisspellingIsCaught(t *testing.T) {
	args := "{\"tasks\":[{\"id\":\"a\",\"prompt\":\"p\"},{\"id\":\"b\",\"prompt\":\"q\",\"depеnds_on\":[\"a\"]}]}"
	_, err := routingTools(t).Execute(context.Background(), json.RawMessage(args))
	if err == nil {
		t.Fatal("a homoglyph spelling of depends_on was ignored; the dependency was " +
			"dropped and task b would run alongside task a")
	}
	if !strings.Contains(err.Error(), "depends_on") {
		t.Fatalf("error = %v, want the correct spelling named", err)
	}
}

// TestNonASCIIDecorationsSurvive keeps the homoglyph rule from eating honest
// decorations: a field named in another script resembles no declared name and
// must be ignored like any other unread field.
func TestNonASCIIDecorationsSurvive(t *testing.T) {
	args := "{\"tasks\":[{\"id\":\"x\",\"prompt\":\"work\",\"描述\":\"a note\",\"описан\":1,\"notes描\":2}],\"wait\":\"run\"}"
	if _, err := routingTools(t).Execute(context.Background(), json.RawMessage(args)); err != nil {
		t.Fatalf("Execute error = %v, want non-ASCII decorations ignored", err)
	}
}

// TestHomoglyphRuleLeavesASCIINamesAlone and TestShortForeignNamesAreNotMisses
// pin the two bounds of the lookalike rule, both of which are false-positive
// guards: it applies only to keys that actually carry a non-ASCII rune, and
// only when the ASCII remainder is one character short of a declared name.
//
// Without the first, a plain-ASCII decoration one letter short of a declared
// name ("budge") would be refused as a misspelling. Without the second, a
// two-character foreign decoration would squash to a single letter and match
// any declared name containing it.
func TestHomoglyphRuleLeavesASCIINamesAlone(t *testing.T) {
	args := `{"tasks":[{"id":"x","prompt":"work","budge":1,"promp":"note"}],"wait":"run"}`
	if _, err := routingTools(t).Execute(context.Background(), json.RawMessage(args)); err != nil {
		t.Fatalf("Execute error = %v; these are ASCII decorations, and the lookalike "+
			"rule is for keys carrying a rune json cannot decode", err)
	}
}

func TestShortForeignNamesAreNotMisses(t *testing.T) {
	args := "{\"tasks\":[{\"id\":\"x\",\"prompt\":\"work\",\"aх\":1,\"tу\":2}],\"wait\":\"run\"}"
	if _, err := routingTools(t).Execute(context.Background(), json.RawMessage(args)); err != nil {
		t.Fatalf("Execute error = %v; a two-character foreign name squashes to one "+
			"letter and must not match every declared name containing it", err)
	}
}

// TestDecorationsEndingInTheRangeBoundsAreIgnored covers the top of the two
// character ranges the squash keeps. No declared field contains a "z" or a
// digit, so nothing else in this file exercises those bounds - and dropping
// either one turns a decoration into a false misspelling: without "z",
// "skillz" squashes to "skill" and reads as the declared field; without "9",
// "budget9" squashes to "budget".
func TestDecorationsEndingInTheRangeBoundsAreIgnored(t *testing.T) {
	for _, field := range []string{`"skillz":"a"`, `"output_schemaz":{"a":1}`, `"budget9":1`, `"id0":"x"`} {
		t.Run(field, func(t *testing.T) {
			args := `{"tasks":[{"id":"x","prompt":"work",` + field + `}],"wait":"run"}`
			if _, err := routingTools(t).Execute(context.Background(), json.RawMessage(args)); err != nil {
				t.Fatalf("Execute error = %v; %s names no field the tool reads, so it is a "+
					"decoration and must be ignored", err, field)
			}
		})
	}
}

// TestMultipleLookalikesAreCaught pins where the line sits. What marks a
// lookalike is the ASCII that remains, not how many non-ASCII runes were used:
// "budgeмт" reads as "budget", decodes into nothing, and is no more deliberate
// with two substituted runes than with one.
func TestMultipleLookalikesAreCaught(t *testing.T) {
	args := "{\"tasks\":[{\"id\":\"x\",\"prompt\":\"work\",\"budgeмт\":5}]}"
	_, err := routingTools(t).Execute(context.Background(), json.RawMessage(args))
	if err == nil {
		t.Fatal("a two-rune lookalike of budget was ignored")
	}
	if !strings.Contains(err.Error(), "budget") {
		t.Fatalf("error = %v, want budget named", err)
	}
}

// TestFoldDuplicateDecorationsAreIgnored bounds the duplicate check to the
// fields it exists for.
//
// Two spellings matter because encoding/json keeps the LAST one while a
// validator re-parsing the raw arguments can resolve a different one - the
// decoder and the guards then act on different values. That divergence only
// exists for a field something READS. Two spellings of a decoration are two
// keys nothing reads, so refusing the batch over them is the
// decoration-refuses-the-batch failure this design exists to remove, and the
// message ("resolve to one field") is not even true of them.
func TestFoldDuplicateDecorationsAreIgnored(t *testing.T) {
	for name, args := range map[string]string{
		"task level":    `{"tasks":[{"id":"a","prompt":"p","note":"x","Note":"y"}],"wait":"run"}`,
		"request level": `{"tasks":[{"id":"a","prompt":"p"}],"wait":"run","reason":"x","REASON":"y"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := routingTools(t).Execute(context.Background(), json.RawMessage(args)); err != nil {
				t.Fatalf("Execute error = %v; both spellings name a field the tool never "+
					"reads, so neither can change what runs", err)
			}
		})
	}
}
