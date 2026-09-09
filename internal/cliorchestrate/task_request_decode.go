package cliorchestrate

// Decoding and validation of the dispatch_tasks request.
//
// The tool's schema publishes additionalProperties:true and the decoder does
// not refuse unknown fields, so a decoration the tool never reads cannot
// refuse a batch before it runs. The carve-outs from that rule are all about
// field NAMES, and all live here with the decode they qualify:
//
//   - two spellings that resolve to one field, which would let a validator
//     read a value the decoder discarded (duplicateFoldedKey);
//   - a name that would silently RE-ROUTE a task (reservedTaskSelectors);
//   - a misspelling of a name the tool actually reads, whether it varies by
//     separator, by padding, or by a lookalike rune (nearMissOf, and
//     homoglyphMissOf for the runes json cannot decode at all).
//
// One test of admission for every one of them: would ignoring this name make
// the tool act on something other than what the caller wrote? A decoration
// fails that test and is ignored; a name that decodes into nothing while
// reading as correct passes it and is refused. Anything that does not answer
// that question does not belong in this list.
//
// Everything else is ignored on purpose. task_routing.go keeps route
// resolution and the model-facing schema prose.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// ---- wire shape ------------------------------------------------------------

// dispatchTaskParams is the decoded dispatch_tasks request.
type dispatchTaskParams struct {
	Tasks          []dispatchTaskParam `json:"tasks"`
	TimeoutSeconds int                 `json:"timeout_seconds,omitempty"`
	Wait           string              `json:"wait,omitempty"`
	WaitTaskID     string              `json:"wait_task_id,omitempty"`
}

// decodeDispatchTaskJSON adds the presence checks that encoding/json cannot
// express for optional string selectors. It refuses arguments carrying more
// than one JSON value, then decodes; validateDispatchTaskSelectors then
// refuses two spellings of one declared field, reserved selectors, and
// misspellings of declared fields. Every other unknown field is ignored.
func decodeDispatchTaskJSON(args json.RawMessage, target *dispatchTaskParams) error {
	if err := rejectTrailingJSON(args); err != nil {
		return err
	}
	// No DisallowUnknownFields: an unread field is ignored, not fatal. See
	// taskItemSchema for why, and validateDispatchTaskSelectors for the fields
	// that stay refused because ignoring them would silently re-route a task
	// or would decode into nothing while reading as correct.
	decoder := json.NewDecoder(bytes.NewReader(args))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return validateDispatchTaskSelectors(args, target.Tasks)
}

// rejectTrailingJSON refuses arguments that are not exactly one JSON value.
// Two values would let the decoder read one while the validator's own parse
// reads the other.
//
// It does NOT walk the document looking for duplicate keys any more. That scan
// recursed to every depth, so two identical keys inside an output_schema -
// JSON this tool passes through and never reads - refused the whole batch,
// which is the decoration-refuses-the-batch failure the permissive decode
// exists to remove. Duplicates that can change what runs are the ones at the
// levels that decode into structs, and duplicateFoldedKey refuses those by
// json's own fold, which also covers a byte-identical repeat.
func rejectTrailingJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	var first json.RawMessage
	if err := dec.Decode(&first); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

// ---- validation ------------------------------------------------------------

func validateDispatchTaskSelectors(args json.RawMessage, tasks []dispatchTaskParam) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(args, &root); err != nil {
		return err
	}
	// Before anything is looked up: two spellings of one field mean the value
	// this function reads and the value the decoder kept can differ, and every
	// check below would then run over a value nothing dispatches.
	if first, second, found := duplicateFoldedKey(args, declaredRequestFields); found {
		return fmt.Errorf("%q and %q resolve to one field; keep one", first, second)
	}
	// Case-insensitively, the way encoding/json already resolved it into
	// target.Tasks: an exact-cased index answered "tasks must be a non-empty
	// array" for {"Tasks":[...]} while the array sat decoded and populated -
	// a false statement with no spelling to correct toward, on the one field
	// the request requires.
	rawTasks, ok := lookupJSONField(root, "tasks")
	if !ok {
		return fmt.Errorf("tasks must be a non-empty array")
	}
	var taskObjects []json.RawMessage
	if err := json.Unmarshal(rawTasks, &taskObjects); err != nil || len(taskObjects) == 0 {
		return fmt.Errorf("tasks must be a non-empty array")
	}
	for _, field := range []string{"timeout_seconds", "wait", "wait_task_id"} {
		if value, present := lookupJSONField(root, field); present && string(value) == "null" {
			return fmt.Errorf("%s must not be null", field)
		}
	}
	// Near-miss only, no reserved-selector check: the reserved names are
	// ROUTE selectors and the request object routes nothing, so a top-level
	// "name" or "role" is an ordinary decoration. Refusing it would cost a
	// whole batch for no safety - the asymmetry with the per-task check is
	// deliberate.
	for _, present := range sortedKeys(root) {
		if declared := nearMissOf(present, declaredRequestFields); declared != "" {
			return fmt.Errorf("%q is not a request field; the field you mean is spelled %q", present, declared)
		}
	}
	seenIDs := make(map[string]struct{}, len(tasks))
	for i, raw := range taskObjects {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}

		if i < len(tasks) {
			id := strings.TrimSpace(tasks[i].ID)
			if id != "" {
				if _, exists := seenIDs[id]; exists {
					return fmt.Errorf("duplicate task id %q", id)
				}
				seenIDs[id] = struct{}{}
			}
		}
		if err := validateTaskObject(i+1, raw, fields); err != nil {
			return err
		}
	}
	return nil
}

// validateTaskObject refuses the field names and shapes one task object must
// not carry. Name-shape first for EVERY key, then nulls, then the two string
// selectors - the order is the contract, not an accident: a misspelled field
// set to null is first of all misspelled, and "dependsOn must not be null"
// sends the model to fix the value of a field the tool never read. Fusing the
// walks into one pass would make that ordering per-key instead of global, so a
// task with a null "budget" and a misspelled "dependsOn" would report the null
// (b sorts before d) and invert the rule.
//
// index is the task's 1-based position, for messages the model can act on.
func validateTaskObject(index int, raw json.RawMessage, fields map[string]json.RawMessage) error {
	if first, second, found := duplicateFoldedKey(raw, declaredTaskFields); found {
		return fmt.Errorf("task %d: %q and %q resolve to one field; keep one", index, first, second)
	}
	keys := sortedKeys(fields)
	// Case-insensitively, because encoding/json matches field names that way
	// too: a "Handler" the decode would have ignored must not slip past a
	// check that only knows the lowercase spelling.
	for _, present := range keys {
		if reserved := reservedSelectorFor(present); reserved != "" {
			return fmt.Errorf("task %d: %q is not a task field; "+
				"route with \"agent\" (and optionally \"skill\") instead", index, present)
		}
		if declared := nearMissOf(present, declaredTaskFields); declared != "" {
			return fmt.Errorf("task %d: %q is not a task field; the field you mean is spelled %q",
				index, present, declared)
		}
	}
	// Declared fields only. This walked EVERY key, which was harmless while
	// the decode rejected undeclared names outright and became a trap the
	// moment it went permissive: "description": null - the shape a decoration
	// with no value takes - refused a batch of up to 16 tasks and named a
	// field the tool never reads. A null on a field the tool DOES read stays
	// an error: the model meant to set it, and the tool would silently use the
	// zero value.
	for _, field := range declaredTaskFields {
		if value, present := lookupJSONField(fields, field); present && string(value) == "null" {
			return fmt.Errorf("task %d: %s must not be null", index, field)
		}
	}
	// No null branch here: the loop above already returned for every
	// null-valued declared field, agent and skill included.
	for _, field := range []string{"agent", "skill"} {
		value, present := lookupJSONField(fields, field)
		if !present {
			continue
		}
		var text string
		if err := json.Unmarshal(value, &text); err != nil {
			return fmt.Errorf("task %d: %s must be a string when present: %w", index, field, err)
		}
	}
	return nil
}

// ---- the name rules --------------------------------------------------------

// declaredTaskFields and declaredRequestFields are the JSON names each object
// decodes. They exist for the near-miss check below, not for decoding: keep
// them in step with dispatchTaskParam and dispatchTaskParams.
var (
	declaredTaskFields    = []string{"id", "agent", "skill", "depends_on", "prompt", "timeout_seconds", "budget", "output_schema", "input_schema"}
	declaredRequestFields = []string{"tasks", "timeout_seconds", "wait", "wait_task_id"}
)

// reservedTaskSelectors are field names a task must never carry. Every one of
// them selected a route in an older task API or names a knob the task schema
// deliberately does not expose, so accepting and ignoring one would run the
// task somewhere the model did not ask for - the single case where refusing
// beats being permissive. The refusal names the field, so the model can drop
// it and retry instead of guessing.
var reservedTaskSelectors = []string{"handler", "name", "role", "model", "provider", "tools"}

// foldedDeclared reports whether name resolves onto a declared field under
// encoding/json's fold - the same test the decoder applies when it decides
// which struct field a key fills.
func foldedDeclared(name string, declared []string) bool {
	for _, field := range declared {
		if strings.EqualFold(name, field) {
			return true
		}
	}
	return false
}

// duplicateFoldedKey reports two keys of ONE object that resolve to the same
// struct field, naming both. It reads only this object's own keys - nested
// values are skipped whole - because the fold is a decoder rule, and only the
// levels decoded into structs (the request and each task) obey it. An
// output_schema is arbitrary caller JSON where "Type" and "type" are two
// honest members, and refusing those would break schemas the tool never reads.
//
// A byte-identical repeat folds equal too, so this one check covers both it
// and the case variants. They matter because encoding/json lets the
// LAST spelling win while a validator that re-parses the raw arguments can
// resolve a different one - so every guard runs over a value that was never
// used. That turns one extra key into a bypass of the whole guard layer.
func duplicateFoldedKey(raw json.RawMessage, declared []string) (first, second string, found bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return "", "", false
	}
	// EqualFold, not a map keyed on ToLower: json's field fold is wider,
	// folding U+017F onto "s" and U+212A onto "k", so "taskſ" and "tasks" are
	// ONE field to the decoder. The quadratic scan is over one object's own
	// keys, which is a handful.
	var seen []string
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return "", "", false
		}
		// No type-assertion branch: Token() at a key position returns a string
		// or an error (JSON grammar admits nothing else there), so a guard for
		// the impossible case would be a line no test could ever execute. A
		// zero name would fold-match no declared field, so even that case
		// cannot produce a false refusal.
		name, _ := key.(string)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return "", "", false
		}
		for _, prev := range seen {
			// Declared fields only. The divergence this closes is between the
			// decoder, which keeps the LAST spelling, and a validator that
			// re-parses the raw arguments and may resolve a different one -
			// and that can only matter for a field something reads. Two
			// spellings of a decoration are two keys nothing reads, so
			// refusing them would be the decoration-refuses-the-batch failure
			// this file exists to prevent, over a message ("resolve to one
			// field") that is not even true of them.
			if strings.EqualFold(prev, name) && foldedDeclared(name, declared) {
				return prev, name, true
			}
		}
		seen = append(seen, name)
	}
	return "", "", false
}

// reservedSelectorFor returns the reserved selector field matches, or "" when
// it is an ordinary unread field.
func reservedSelectorFor(field string) string {
	lowered := strings.ToLower(strings.TrimSpace(field))
	for _, reserved := range reservedTaskSelectors {
		if lowered == reserved {
			return reserved
		}
	}
	return ""
}

// nearMissOf reports the declared field that supplied is a misspelling of, or
// "" when supplied is an ordinary decoration the caller may ignore.
//
// The distinction is exactly encoding/json's own matching rule. json matches
// field names case-insensitively, so "Prompt" decodes into prompt and is not a
// miss. It does NOT bridge punctuation, so "dependsOn", "DependsOn" and
// "depends-on" all decode into NOTHING while looking correct to a reader - the
// task then runs with no dependency, concurrently with the task it was
// declared to wait for, and no surface reports it. That silence is the reason
// this check exists: ignoring a field the tool never reads is harmless,
// ignoring a misspelling of a field the tool DOES read is a wrong answer
// delivered confidently.
func nearMissOf(supplied string, declared []string) string {
	lowered := strings.ToLower(supplied)
	squashed := squashFieldName(lowered)
	for _, field := range declared {
		// supplied, untrimmed: json does not trim key names, so " depends_on"
		// decodes into nothing exactly like "dependsOn". Only the squashed
		// comparison below may normalize - it decides what a miss is OF, never
		// whether one happened.
		if strings.EqualFold(supplied, field) {
			// json decodes it under its own fold; not a miss.
			return ""
		}
		if squashed == strings.ReplaceAll(field, "_", "") {
			return field
		}
	}
	// A lookalike that does NOT fold: a Cyrillic "е" is a different letter to
	// json, so the key decodes into nothing while reading correctly on screen.
	// squashFieldName drops the rune rather than accounting for it, leaving
	// "depеnds_on" one letter short of "dependson" and looking like an
	// ordinary decoration.
	return homoglyphMissOf(lowered, squashed, declared)
}

// homoglyphMissOf reports the declared field a key carrying non-ASCII runes is
// a lookalike spelling of: dropping those runes leaves a form that is a
// subsequence of the declared name, exactly one character shorter.
//
// The ASCII remainder is what separates a lookalike from a foreign-language
// decoration, not the NUMBER of non-ASCII runes. A key written wholly in
// another script ("описание", "描述") squashes to nothing and is ignored, as
// an honest decoration in a language this tool does not read should be -
// refusing it would be the whole-batch refusal this design exists to remove.
// A name that is ASCII except for a lookalike or two ("depеnds_on",
// "budgeмт") decodes into nothing while reading as correct on screen, and
// that is never what the caller meant.
func homoglyphMissOf(lowered, squashed string, declared []string) string {
	// No squashed == "" case: it would need a declared name of one character,
	// and the shortest is "id".
	if squashed == lowered {
		return ""
	}
	for _, field := range declared {
		bare := strings.ReplaceAll(field, "_", "")
		if len(bare) == len(squashed)+1 && isSubsequence(squashed, bare) {
			return field
		}
	}
	return ""
}

// isSubsequence reports whether every rune of s appears in order within outer.
func isSubsequence(s, outer string) bool {
	rest := s
	for _, r := range outer {
		if rest != "" && rune(rest[0]) == r {
			rest = rest[1:]
		}
	}
	return rest == ""
}

// squashFieldName keeps only letters and digits, so every spelling that varies
// a declared name by separators alone collapses onto the same form:
// "dependsOn", "depends-on", " depends_on", "depends.on" and "depends_on\r"
// all squash onto "dependson".
//
// A filter, not a list of separators to strip: a list closes one door at a
// time (_ and -, then space, then \r, a non-breaking space, "."), while
// keeping only [a-z0-9] has no next door.
func squashFieldName(lowered string) string {
	var b strings.Builder
	b.Grow(len(lowered))
	for _, r := range lowered {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ---- json key resolution ---------------------------------------------------

// lookupJSONField finds field in m the way encoding/json resolves a struct
// tag: exact match first, else a case-insensitive one. A validator that
// indexes exactly, over an object json decoded case-insensitively, reports on
// a field the decode did not use.
//
// It does not arbitrate between two keys that fold together, because it never
// sees them: duplicateFoldedKey refuses that object before any lookup runs.
// Without that, "first fold match in sorted order" and "the last spelling in
// the document" would be different values.
func lookupJSONField(m map[string]json.RawMessage, field string) (json.RawMessage, bool) {
	if value, ok := m[field]; ok {
		return value, true
	}
	for _, key := range sortedKeys(m) {
		if strings.EqualFold(key, field) {
			return m[key], true
		}
	}
	return nil, false
}

// sortedKeys returns m's keys in a stable order. Every validation walk uses
// it, so an object carrying two faults names the same one on every run: with
// map order, a retry of the identical call reports a different field and no
// operator can reproduce what the model saw.
func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
