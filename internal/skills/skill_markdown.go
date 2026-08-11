package skills

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Model-facing text caps. These are deliberately chosen starting points,
// not measured limits. If a provider's tool-schema limit is hit in practice,
// re-derive them from that limit rather than tuning by feel.
const (
	nameMaxLen        = 64
	descriptionMaxLen = 200
	triggerMaxLen     = 64
	triggersJoinedMax = 400
)

// knownSkillKeys is the complete recognised frontmatter key set. Anything else
// is rejected, so a field nothing consumes cannot be added silently - the class
// of bug that left `triggers:` inert in nine skills.
var knownSkillKeys = map[string]bool{
	"name": true, "description": true, "triggers": true,
	"user-invocable": true, "argument-hint": true, "short-description": true,
	"tools": true,
	// JSON-string schemas (nested maps are not supported by the frontmatter
	// subset parser). Example: output_schema: '{"type":"object"}'
	"input_schema": true, "output_schema": true,
}

type parsedSkill struct {
	name, description, argsHint, shortDescription, instructions string
	triggers                                                    []string
	tools                                                       []string
	userInvocable                                               bool
	inputSchema, outputSchema                                   map[string]any
}

func parseSkillMarkdown(data []byte) (parsedSkill, error) {
	normalized := normalizeNewlines(string(data))
	m, closing, err := ParseFrontmatterKnownWithClosing([]byte(normalized), knownSkillKeys)
	if err != nil {
		return parsedSkill{}, err
	}
	var instructions string
	if m == nil {
		instructions = strings.TrimSpace(normalized)
	} else {
		lines := strings.Split(normalized, "\n")
		instructions = strings.TrimSpace(strings.Join(lines[closing+1:], "\n"))
	}
	parsed := parsedSkill{userInvocable: true}
	if m != nil {
		if err := fillParsedSkillFrontmatter(&parsed, m); err != nil {
			return parsedSkill{}, err
		}
	}
	parsed.instructions = instructions
	return parsed, nil
}

func fillParsedSkillFrontmatter(parsed *parsedSkill, m map[string]any) error {
	var err error
	if parsed.name, err = frontmatterStringField(m, "name"); err != nil {
		return err
	}
	if parsed.description, err = frontmatterStringField(m, "description"); err != nil {
		return err
	}
	switch tv := m["triggers"].(type) {
	case []string:
		parsed.triggers = tv
	case string:
		if tv != "" {
			parsed.triggers = []string{tv}
		}
	case nil:
		// triggers omitted: keep nil
	default:
		return fmt.Errorf("triggers must be a string or list of strings")
	}
	if parsed.argsHint, err = frontmatterStringField(m, "argument-hint"); err != nil {
		return err
	}
	if parsed.shortDescription, err = frontmatterStringField(m, "short-description"); err != nil {
		return err
	}
	if raw, ok := m["user-invocable"]; ok {
		v, ok := raw.(string)
		if !ok {
			return fmt.Errorf("user-invocable must be true or false")
		}
		if v != "" {
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "true":
				parsed.userInvocable = true
			case "false":
				parsed.userInvocable = false
			default:
				return fmt.Errorf("user-invocable must be true or false")
			}
		}
	}
	tools, err := parseSkillTools(m["tools"])
	if err != nil {
		return err
	}
	parsed.tools = tools
	outSch, err := parseSkillSchemaJSON(m["output_schema"], "output_schema")
	if err != nil {
		return err
	}
	parsed.outputSchema = outSch
	inSch, err := parseSkillSchemaJSON(m["input_schema"], "input_schema")
	if err != nil {
		return err
	}
	parsed.inputSchema = inSch
	return nil
}

// frontmatterStringField returns the string value for key. An omitted key
// yields the zero value with no error; a present non-string value is a hard
// error naming the key. A bare type assertion used to silently drop the
// wrong-typed value - e.g. user-invocable: [false] parses as []string{"false"},
// the assertion failed, and the default user-invocable=true survived - the
// class of silent coercion this parser exists to prevent.
func frontmatterStringField(m map[string]any, key string) (string, error) {
	raw, ok := m[key]
	if !ok {
		return "", nil
	}
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", key)
	}
	return s, nil
}

// parseSkillSchemaJSON accepts a JSON object as a scalar string (frontmatter
// cannot express nested maps). Omitted or empty yields nil.
func parseSkillSchemaJSON(raw any, field string) (map[string]any, error) {
	if raw == nil {
		return nil, nil
	}
	s, ok := raw.(string)
	if !ok {
		return nil, fmt.Errorf("%s must be a JSON object string", field)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, fmt.Errorf("%s: invalid JSON: %w", field, err)
	}
	if m == nil {
		return nil, fmt.Errorf("%s must be a JSON object", field)
	}
	return m, nil
}

// parseSkillTools coerces frontmatter tools into a non-empty-name string list.
// Omitted key yields nil. Empty list is valid (skill declares no required tools).
// Duplicate names within one list are a hard error (plan 43): silent dedup
// would hide an ambiguous declaration.
func parseSkillTools(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	var items []string
	switch v := raw.(type) {
	case []string:
		items = v
	case string:
		if strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("tools: empty tool name")
		}
		items = []string{v}
	default:
		return nil, fmt.Errorf("tools must be a list of tool names")
	}
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, n := range items {
		n = strings.TrimSpace(n)
		if n == "" {
			return nil, fmt.Errorf("tools: empty tool name")
		}
		if _, dup := seen[n]; dup {
			return nil, fmt.Errorf("tools: duplicate tool name %q", n)
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	return out, nil
}
