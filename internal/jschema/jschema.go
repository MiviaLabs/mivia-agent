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

	"github.com/MiviaLabs/mivia-agent/internal/textutil"
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
// wraps the entire body. Nested or partial fences are left untouched. The
// opening and closing fences must carry the same number of backticks (>= 3),
// so 4-backtick fences - the correct way to fence JSON containing backticks -
// are handled as well as the conventional triple-backtick fence.
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
	open := lines[0]
	backticks := 0
	for backticks < len(open) && open[backticks] == '`' {
		backticks++
	}
	if backticks < 3 {
		return s
	}
	if strings.TrimSpace(lines[len(lines)-1]) != strings.Repeat("`", backticks) {
		return s
	}
	body := lines[1 : len(lines)-1]
	for _, line := range body {
		if strings.HasPrefix(strings.TrimSpace(line), strings.Repeat("`", backticks)) {
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
		msg = textutil.TruncateRuneSafe(msg, MaxCorrectiveBytes)
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
	// The restated contract is the point of this message: the model repairs
	// against it, so it is built first and NEVER truncated. The validation
	// detail is bounded to whatever budget remains; if the contract alone
	// fills the budget, the detail gives way (a cut contract would make the
	// retry repair blind, which is exactly the failure this fixes).
	schemaSection := ""
	if contract := ModelSchemaContract(schema); contract != "" {
		schemaSection = "\nThe required schema is:\n" + contract
	}
	prefix := "Your previous reply did not match the required JSON schema. " +
		"Reply again with ONLY valid JSON matching the schema. Errors:\n"
	detail := "output does not match the required schema"
	if validateErr != nil {
		detail = strings.TrimPrefix(validateErr.Error(), ErrValidation.Error()+": ")
	}
	lines := strings.Split(detail, "\n")
	if len(lines) > MaxValidationErrors {
		lines = lines[:MaxValidationErrors]
		lines = append(lines, "…")
	}
	joined := strings.Join(lines, "\n")
	budget := MaxCorrectiveBytes - len(schemaSection)
	room := budget - len(prefix)
	if room < 0 {
		room = 0
	}
	if len(joined) > room {
		joined = textutil.TruncateRuneSafe(joined, room)
	}
	msg := prefix + joined + schemaSection
	if redact != nil {
		msg = redact(msg)
	}
	return msg
}

// PromptAppendix is the deterministic host instruction appended when a schema
// is in force.
func PromptAppendix(schema map[string]any) string {
	contract := ModelSchemaContract(schema)
	if contract == "" {
		return "\n\nReturn ONLY valid JSON matching the required output schema."
	}
	return "\n\nReturn ONLY valid JSON matching this schema (no prose, no markdown fences):\n" + contract
}

// schemaMetaKeys are JSON Schema meta/annotation keywords that describe the
// document, not the instance shape. Every model-facing renderer strips them.
// A verbatim schema document invites the model to echo the document back as
// its answer, so the model must never see one.
var schemaMetaKeys = map[string]struct{}{
	"$schema":     {},
	"title":       {},
	"description": {},
	"$id":         {},
	"$comment":    {},
	"default":     {},
}

// maxExampleRequiredKeys bounds the compact example. An example is practical
// only for small object schemas.
const maxExampleRequiredKeys = 4

// ModelSchemaContract renders the model-facing contract for a schema map.
// It strips the schema meta-keywords, adds the never-echo instruction, and
// appends a compact filled example for small object schemas. The compiled
// validator still uses the raw document (Compiled.Raw()); this is only the
// text the model sees. It returns "" when the schema cannot be rendered.
func ModelSchemaContract(schema map[string]any) string {
	if schema == nil {
		return ""
	}
	raw, err := json.Marshal(stripMetaKeys(schema))
	if err != nil || len(raw) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Output an instance of the schema, never the schema document itself.")
	if ex := exampleForSchema(schema); ex != "" {
		b.WriteString("\nExample: " + ex)
	}
	b.WriteString("\n" + string(raw))
	return b.String()
}

// stripMetaKeys removes the schema meta-keywords recursively, so the rendered
// contract keeps only the instance-shape keywords.
func stripMetaKeys(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, child := range t {
			if _, meta := schemaMetaKeys[k]; meta {
				continue
			}
			out[k] = stripMetaKeys(child)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, child := range t {
			out[i] = stripMetaKeys(child)
		}
		return out
	default:
		return v
	}
}

// exampleForSchema builds a compact one-line example instance for a small
// object schema (up to maxExampleRequiredKeys required properties). It
// returns "" when no compact example is practical.
func exampleForSchema(schema map[string]any) string {
	keys := requiredKeys(schema)
	if len(keys) == 0 || len(keys) > maxExampleRequiredKeys {
		return ""
	}
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		return ""
	}
	inst := make(map[string]any, len(keys))
	for _, name := range keys {
		prop, _ := props[name].(map[string]any)
		inst[name] = exampleValue(prop)
	}
	raw, err := json.Marshal(inst)
	if err != nil {
		return ""
	}
	return string(raw)
}

// exampleValue picks a plausible instance value for one property subschema.
func exampleValue(prop map[string]any) any {
	if prop == nil {
		return ""
	}
	if e := firstEnumValue(prop); e != nil {
		return e
	}
	if c, ok := prop["const"]; ok {
		return c
	}
	switch prop["type"] {
	case "boolean":
		return true
	case "number", "integer":
		return 0
	case "array":
		items, _ := prop["items"].(map[string]any)
		return []any{exampleValue(items)}
	case "object":
		out := map[string]any{}
		childProps, _ := prop["properties"].(map[string]any)
		for _, name := range requiredKeys(prop) {
			child, _ := childProps[name].(map[string]any)
			out[name] = exampleValue(child)
		}
		return out
	default:
		return "..."
	}
}

// requiredKeys returns the schema's required property names. It accepts both
// []any (JSON decode) and []string (Go literals).
func requiredKeys(schema map[string]any) []string {
	switch req := schema["required"].(type) {
	case []any:
		out := make([]string, 0, len(req))
		for _, r := range req {
			if s, ok := r.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return req
	}
	return nil
}

// firstEnumValue returns the first enum member when the property declares one.
func firstEnumValue(prop map[string]any) any {
	switch e := prop["enum"].(type) {
	case []any:
		if len(e) > 0 {
			return e[0]
		}
	case []string:
		if len(e) > 0 {
			return e[0]
		}
	}
	return nil
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
		return textutil.TruncateRuneSafe(s, MaxCorrectiveBytes)
	}
	return s
}
