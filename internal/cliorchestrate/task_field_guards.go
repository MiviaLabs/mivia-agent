package cliorchestrate

// Field-name guards for the dispatch_tasks request.
//
// The tool's schema publishes additionalProperties:true and its decoder no
// longer refuses unknown fields, so a decoration the tool does not read
// ("description", "priority", "notes") can never refuse a batch before it
// runs. That permissiveness needs exactly two carve-outs, and both live here:
// a field that would silently RE-ROUTE a task, and a misspelling of a field
// the tool actually reads. Everything else is ignored on purpose.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// declaredTaskFields and declaredRequestFields are the JSON names each object
// decodes. They exist for the near-miss check below, not for decoding: keep
// them in step with dispatchTaskParam and dispatchTaskParams.
var (
	declaredTaskFields    = []string{"id", "agent", "skill", "depends_on", "prompt", "timeout_seconds", "budget", "output_schema", "input_schema"}
	declaredRequestFields = []string{"tasks", "timeout_seconds", "wait", "wait_task_id"}
)

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
	// NOT trimmed: encoding/json does not trim key names, so " depends_on" is
	// a key it decodes into nothing, exactly like "dependsOn". Trimming here
	// made the padded spelling compare EQUAL to the declared one and waved it
	// through as "json handles this" - the fail-open branch of a check whose
	// whole job is to fail closed. Only the squashed comparison below may
	// normalize, because it decides what a miss is OF, never whether one
	// happened.
	lowered := strings.ToLower(supplied)
	squashed := squashFieldName(lowered)
	for _, field := range declared {
		if lowered == field {
			// json decodes it (case-insensitively); not a miss.
			return ""
		}
		if squashed == strings.ReplaceAll(field, "_", "") {
			return field
		}
	}
	return ""
}

// squashFieldName keeps only letters and digits, so every spelling that varies
// a declared name by separators alone collapses onto the same form:
// "dependsOn", "depends-on", " depends_on", "depends.on" and "depends_on\r"
// all squash onto "dependson".
//
// A filter, not a list of separators to strip. The list started as _ and -,
// grew a space when a padded key was found to decode into nothing, and would
// have grown again for \r, a non-breaking space and "." - each addition
// closing one door in a corridor. Keeping only [a-z0-9] has no next door.
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

// reservedTaskSelectors are field names a task must never carry. Every one of
// them selected a route in an older task API or names a knob the task schema
// deliberately does not expose, so accepting and ignoring one would run the
// task somewhere the model did not ask for - the single case where refusing
// beats being permissive. The refusal names the field, so the model can drop
// it and retry instead of guessing.
var reservedTaskSelectors = []string{"handler", "name", "role", "model", "provider", "tools"}

// lookupJSONField finds field in m the way encoding/json resolves a struct
// tag: exact match first, then a unique case-insensitive one. A validator that
// indexes exactly, over an object json decoded case-insensitively, reports on
// a field the decode did not use.
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

// sortedKeys returns m's keys in a stable order, so a validation walk over a
// JSON object reports the same failure every time it runs.
func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
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
func validateTaskObject(index int, fields map[string]json.RawMessage) error {
	// Sorted: a task carrying two bad fields must name the same one on every
	// run, or a retry of the identical call reports a different error and no
	// operator can reproduce what the model saw.
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
