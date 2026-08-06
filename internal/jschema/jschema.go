// Package jschema is a fail-closed JSON Schema compile/validate wrapper for
// structured subagent outputs (plan tools/02).
//
// Policy:
//   - no remote $ref resolution (only the document under compile)
//   - marshaled schema size and nesting depth caps
//   - validation errors are redacted and size-bounded before model-facing use
package jschema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Admission caps applied at compile time.
const (
	// MaxSchemaBytes is the maximum marshaled size of a schema document.
	MaxSchemaBytes = 16 << 10
	// MaxSchemaDepth is the maximum nesting depth of objects/arrays in a schema.
	MaxSchemaDepth = 32
	// MaxValidationErrors is how many validation error lines a corrective
	// message may carry.
	MaxValidationErrors = 5
	// MaxCorrectiveBytes caps the corrective user message body.
	MaxCorrectiveBytes = 1024
)

// Test seams for defensive error paths Compile's own inputs cannot reach: its
// document is the output of json.Marshal, which is always parseable, always
// re-unmarshalable, and always added to a freshly built compiler under a fixed
// URL. The checks stay because none of that is guaranteed by a type.
var (
	unmarshalSchemaJSON = jsonschema.UnmarshalJSON
	addSchemaResource   = func(c *jsonschema.Compiler, url string, doc any) error { return c.AddResource(url, doc) }
	cloneSchemaJSON     = json.Unmarshal
)

// ErrAdmission is returned when a schema is refused before any run starts.
var ErrAdmission = errors.New("schema admission rejected")

// ErrValidation is returned when an instance fails validation.
var ErrValidation = errors.New("schema validation failed")

// Compiled is a schema ready for Validate.
type Compiled struct {
	sch *jsonschema.Schema
	raw map[string]any
}

// Compile admits and compiles a JSON Schema object. Remote $ref is disabled;
// size and depth caps fail closed.
func Compile(schema map[string]any) (*Compiled, error) {
	if schema == nil {
		return nil, fmt.Errorf("%w: empty schema", ErrAdmission)
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal: %v", ErrAdmission, err)
	}
	if len(raw) > MaxSchemaBytes {
		return nil, fmt.Errorf("%w: schema is %d bytes (max %d)", ErrAdmission, len(raw), MaxSchemaBytes)
	}
	if depth := mapDepth(schema); depth > MaxSchemaDepth {
		return nil, fmt.Errorf("%w: schema depth %d exceeds max %d", ErrAdmission, depth, MaxSchemaDepth)
	}
	if err := rejectRemoteRefs(schema, ""); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAdmission, err)
	}

	doc, err := unmarshalSchemaJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: parse: %v", ErrAdmission, err)
	}
	c := jsonschema.NewCompiler()
	// Refuse all URL loads so $ref cannot escape the admitted document.
	c.UseLoader(rejectAllLoader{})
	const resourceURL = "mem://output-schema.json"
	if err := addSchemaResource(c, resourceURL, doc); err != nil {
		return nil, fmt.Errorf("%w: add resource: %v", ErrAdmission, err)
	}
	sch, err := c.Compile(resourceURL)
	if err != nil {
		return nil, fmt.Errorf("%w: compile: %v", ErrAdmission, err)
	}
	// Keep a deep copy of the admitted map for prompt appendix / fingerprint.
	var copy map[string]any
	if err := cloneSchemaJSON(raw, &copy); err != nil {
		return nil, fmt.Errorf("%w: clone: %v", ErrAdmission, err)
	}
	return &Compiled{sch: sch, raw: copy}, nil
}

// Raw returns a deep copy of the admitted schema map.
func (c *Compiled) Raw() map[string]any {
	if c == nil || c.raw == nil {
		return nil
	}
	raw, _ := json.Marshal(c.raw)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

// Validate checks instance against the compiled schema.
func (c *Compiled) Validate(instance any) error {
	if c == nil || c.sch == nil {
		return fmt.Errorf("%w: nil schema", ErrValidation)
	}
	if err := c.sch.Validate(instance); err != nil {
		return fmt.Errorf("%w: %s", ErrValidation, formatValidationErr(err))
	}
	return nil
}

// ValidateJSONBytes unmarshals raw JSON and validates it.
func (c *Compiled) ValidateJSONBytes(raw []byte) (any, error) {
	var instance any
	if err := json.Unmarshal(raw, &instance); err != nil {
		return nil, fmt.Errorf("%w: not valid JSON: %v", ErrValidation, err)
	}
	if err := c.Validate(instance); err != nil {
		return nil, err
	}
	return instance, nil
}

// StripOneCodeFence removes at most one well-formed markdown code fence that
// wraps the entire body. Nested or partial fences are left untouched.
func StripOneCodeFence(s string) string {
	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, "```") {
		return s
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) < 2 {
		// Single-line fenced body is not a well-formed multi-line fence wrap.
		return s
	}
	if strings.TrimSpace(lines[len(lines)-1]) != "```" {
		return s
	}
	// The opening line is ``` or ```lang by construction: trimmed starts with a
	// fence, so its first line does too.
	body := lines[1 : len(lines)-1]
	for _, line := range body {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			// Nested fence — refuse to strip.
			return s
		}
	}
	return strings.Join(body, "\n")
}

// FormatCorrective builds a bounded, plain-text corrective user message.
// callerRedact, when non-nil, is applied to the full message before return.
func FormatCorrective(validateErr error, redact func(string) string) string {
	detail := "output does not match the required schema"
	if validateErr != nil {
		detail = validateErr.Error()
		// Drop sentinel prefix noise for the model.
		detail = strings.TrimPrefix(detail, ErrValidation.Error()+": ")
	}
	lines := strings.Split(detail, "\n")
	if len(lines) > MaxValidationErrors {
		lines = lines[:MaxValidationErrors]
		lines = append(lines, "…")
	}
	msg := "Your previous reply did not match the required JSON schema. " +
		"Reply again with ONLY valid JSON matching the schema. Errors:\n" +
		strings.Join(lines, "\n")
	if len(msg) > MaxCorrectiveBytes {
		msg = msg[:MaxCorrectiveBytes]
	}
	if redact != nil {
		msg = redact(msg)
	}
	return msg
}

// FormatCorrectiveWithSchema builds the corrective user message with the
// required schema restated inline. The retry turn replaces the task prompt,
// which carried the schema appendix, so without a restated schema the model
// repairs its output shape blind and the retry budget is spent on the same
// invalid shape.
func FormatCorrectiveWithSchema(validateErr error, schema map[string]any, redact func(string) string) string {
	msg := FormatCorrective(validateErr, nil)
	if raw, err := json.Marshal(schema); err == nil {
		msg += "\nThe required schema is:\n" + string(raw)
		if len(msg) > MaxCorrectiveBytes {
			msg = msg[:MaxCorrectiveBytes]
		}
	}
	if redact != nil {
		msg = redact(msg)
	}
	return msg
}

// PromptAppendix is the deterministic host instruction appended when a schema
// is in force.
func PromptAppendix(schema map[string]any) string {
	raw, err := json.Marshal(schema)
	if err != nil {
		return "\n\nReturn ONLY valid JSON matching the required output schema."
	}
	return "\n\nReturn ONLY valid JSON matching this schema (no prose, no markdown fences):\n" + string(raw)
}

type rejectAllLoader struct{}

func (rejectAllLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("remote schema load refused: %s", url)
}

func mapDepth(v any) int {
	switch t := v.(type) {
	case map[string]any:
		max := 0
		for _, child := range t {
			if d := mapDepth(child); d > max {
				max = d
			}
		}
		return max + 1
	case []any:
		max := 0
		for _, child := range t {
			if d := mapDepth(child); d > max {
				max = d
			}
		}
		return max + 1
	default:
		return 1
	}
}

// rejectRemoteRefs walks the schema for $ref values that are absolute URLs
// or otherwise not in-document fragments.
func rejectRemoteRefs(v any, path string) error {
	switch t := v.(type) {
	case map[string]any:
		if ref, ok := t["$ref"].(string); ok {
			if isRemoteRef(ref) {
				return fmt.Errorf("$ref %q at %s is remote or external; only in-document refs are allowed", ref, pathOrRoot(path))
			}
		}
		for k, child := range t {
			p := path + "/" + k
			if err := rejectRemoteRefs(child, p); err != nil {
				return err
			}
		}
	case []any:
		for i, child := range t {
			if err := rejectRemoteRefs(child, fmt.Sprintf("%s/%d", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func isRemoteRef(ref string) bool {
	if ref == "" {
		return false
	}
	// In-document fragment only.
	if strings.HasPrefix(ref, "#") {
		return false
	}
	// Anything with a scheme or looking like a network path is remote.
	if strings.Contains(ref, "://") {
		return true
	}
	if strings.HasPrefix(ref, "//") {
		return true
	}
	// Relative file refs are also external to the admitted document.
	return true
}

func pathOrRoot(path string) string {
	if path == "" {
		return "/"
	}
	return path
}

func formatValidationErr(err error) string {
	if err == nil {
		return ""
	}
	// Prefer a short multi-line basic form when available.
	var ve *jsonschema.ValidationError
	if errors.As(err, &ve) {
		out := ve.BasicOutput()
		raw, mErr := json.Marshal(out)
		if mErr == nil && len(raw) < MaxCorrectiveBytes {
			return string(raw)
		}
	}
	s := err.Error()
	if len(s) > MaxCorrectiveBytes {
		return s[:MaxCorrectiveBytes]
	}
	return s
}
