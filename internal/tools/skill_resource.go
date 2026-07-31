package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const SkillResourceToolName = "read_skill_resource"

// SkillResourceReader is a host-held activation callback. The model can
// supply only a declared ID, never a path, scope, or skill selector.
type SkillResourceReader func(context.Context, string) (text, marker string, err error)

type skillResourceTool struct {
	read SkillResourceReader
	key  string
	max  int
}

// NewSkillResourceTool creates a fresh scoped reader for one activation.
func NewSkillResourceTool(read SkillResourceReader, activationKey string, maxResultBytes int) Tool {
	return &skillResourceTool{read: read, key: activationKey, max: maxResultBytes}
}

func (t *skillResourceTool) Name() string { return SkillResourceToolName }
func (t *skillResourceTool) Description() string {
	return "Read a named text reference declared by the currently active skill. Use only a resource ID from that skill's catalogue."
}
func (t *skillResourceTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"id": map[string]any{"type": "string", "description": "Declared skill resource ID"},
	}, []string{"id"})
}
func (t *skillResourceTool) Capability(json.RawMessage) Capability {
	return Capability{Class: ExecutionRead, ResourceKey: t.key, MaxResultBytes: t.max}
}
func (t *skillResourceTool) ResultBudgetBytes() int { return t.max }
func (t *skillResourceTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	in.ID = strings.TrimSpace(in.ID)
	if in.ID == "" || t.read == nil {
		return "", fmt.Errorf("skill resource ID is required")
	}
	text, _, err := t.read(ctx, in.ID)
	if err != nil {
		return "", err
	}
	return "<untrusted-skill-resource id=\"" + in.ID + "\">\n" + text + "\n</untrusted-skill-resource>", nil
}
func (t *skillResourceTool) EphemeralResultMarker(args json.RawMessage) string {
	var in struct {
		ID string `json:"id"`
	}
	if decodeArgs(args, &in) != nil {
		return "skill resource access attempted"
	}
	in.ID = strings.TrimSpace(in.ID)
	if !safeSkillResourceID(in.ID) {
		return "skill resource access attempted"
	}
	return "skill resource loaded: " + in.ID
}

func safeSkillResourceID(value string) bool {
	if value == "" || len(value) > 64 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

var _ EphemeralResultTool = (*skillResourceTool)(nil)
