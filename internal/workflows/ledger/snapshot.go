package ledger

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// SnapshotSchemaVersion is the version of the immutable run snapshot wire format.
const SnapshotSchemaVersion = 1

// RefSnapshot pins one schema, template, or verifier by content digest.
// Bytes stores bounded content for templates and schemas.
type RefSnapshot struct {
	Digest  string `json:"digest"`
	Version int    `json:"version,omitempty"`
	Bytes   []byte `json:"bytes,omitempty"`
}

// AgentSnapshot pins an agent definition and its effective provider binding.
type AgentSnapshot struct {
	Digest       string `json:"digest"`
	Version      int    `json:"version,omitempty"`
	ProviderName string `json:"provider_name,omitempty"`
	Model        string `json:"model,omitempty"`
}

// PanelBindingSnapshot pins one static panel member. The key in Snapshot is
// always <step-id>/<member-id> so one agent name may safely use many bindings.
type PanelBindingSnapshot struct {
	StepID         string `json:"step_id"`
	MemberID       string `json:"member_id"`
	AgentName      string `json:"agent_name"`
	AgentDigest    string `json:"agent_digest"`
	ProviderName   string `json:"provider_name"`
	Model          string `json:"model"`
	SkillDigest    string `json:"skill_digest"`
	TemplateDigest string `json:"template_digest"`
	SchemaDigest   string `json:"schema_digest"`
}

type DeliverySnapshot struct {
	Mode     string `json:"mode"`
	Provider string `json:"provider"`
	Base     string `json:"base,omitempty"`
}

// Snapshot is the immutable admission record of one workflow run. It freezes
// the raw workflow definition file bytes (the canonical artifact), the
// compiler digest of the compiled definition, the validated inputs, and the
// resolved agent/schema/template/verifier references. Resume never re-reads a
// changed TOML file: everything needed is in this snapshot.
type Snapshot struct {
	SchemaVersion    int    `json:"schema_version"`
	DefinitionTOML   []byte `json:"definition_toml"`
	DefinitionDigest string `json:"definition_digest"`
	// MCPConfigDigest pins the enabled MCP authority without storing server
	// commands, URLs, headers, environment names, or values.
	MCPConfigDigest string                          `json:"mcp_config_digest,omitempty"`
	Inputs          map[string]string               `json:"inputs,omitempty"`
	Agents          map[string]AgentSnapshot        `json:"agents,omitempty"`
	PanelBindings   map[string]PanelBindingSnapshot `json:"panel_bindings,omitempty"`
	Schemas         map[string]RefSnapshot          `json:"schemas,omitempty"`
	Templates       map[string]RefSnapshot          `json:"templates,omitempty"`
	Skills          map[string]RefSnapshot          `json:"skills,omitempty"`
	Verifiers       map[string]RefSnapshot          `json:"verifiers,omitempty"`
	// VerifierPinsVersion marks snapshots admitted by a binary that pins
	// verifier definitions (verifier-def: keys in Verifiers). 0 means the run
	// predates definition pinning and resumes without definition
	// verification; >= 1 means a referenced definition whose key is absent
	// was stripped, not merely never written, and resume fails closed.
	VerifierPinsVersion int               `json:"verifier_pins_version,omitempty"`
	Delivery            *DeliverySnapshot `json:"delivery,omitempty"`
}

// MarshalSnapshot serializes the snapshot to its canonical JSON form. The
// output bytes are the durable artifact: the snapshot digest is computed over
// them, and resume must reproduce them byte-identically.
func MarshalSnapshot(s Snapshot) ([]byte, error) {
	return json.Marshal(s)
}

// UnmarshalSnapshot decodes a canonical snapshot JSON blob.
func UnmarshalSnapshot(data []byte) (Snapshot, error) {
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return Snapshot{}, err
	}
	var raw struct {
		PanelBindings json.RawMessage `json:"panel_bindings"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Snapshot{}, err
	}
	if len(raw.PanelBindings) != 0 && string(raw.PanelBindings) != "null" {
		bindings, err := decodePanelBindings(raw.PanelBindings)
		if err != nil {
			return Snapshot{}, err
		}
		s.PanelBindings = bindings
	}
	return s, nil
}

func decodePanelBindings(data []byte) (map[string]PanelBindingSnapshot, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	token, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("panel_bindings must be an object")
	}
	bindings := make(map[string]PanelBindingSnapshot)
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("panel binding key is invalid")
		}
		if _, exists := bindings[key]; exists {
			return nil, fmt.Errorf("duplicate panel binding key %q", key)
		}
		var binding PanelBindingSnapshot
		if err := dec.Decode(&binding); err != nil {
			return nil, err
		}
		bindings[key] = binding
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return bindings, nil
}

// SnapshotDigest returns the hex SHA-256 of the canonical snapshot bytes.
func SnapshotDigest(data []byte) string {
	return DigestHex(data)
}

// InputDigest returns the digest of the canonical input JSON object.
func InputDigest(inputs map[string]string) string {
	if inputs == nil {
		inputs = map[string]string{}
	}
	// A map with string keys and values always has a JSON representation.
	data, _ := json.Marshal(inputs)
	return DigestHex(data)
}

// DigestHex returns the lowercase hex SHA-256 of data (shared content-hash helper).
func DigestHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// refDigestHex returns the canonical content digest for a RefSnapshot. The
// wire format matches the CLI and localengine pin helpers: "sha256:" plus the
// lowercase hex SHA-256 of the bytes.
func refDigestHex(data []byte) string {
	return "sha256:" + DigestHex(data)
}

// Validate checks the snapshot for admission invariants: schema version
// supported, non-empty definition bytes and digest, and digest/bytes
// consistency for every populated schema, template, skill, and verifier ref.
func (s Snapshot) Validate() error {
	if s.SchemaVersion != SnapshotSchemaVersion {
		return fmt.Errorf("unsupported snapshot schema version %d (want %d)", s.SchemaVersion, SnapshotSchemaVersion)
	}
	if len(s.DefinitionTOML) == 0 {
		return fmt.Errorf("snapshot definition TOML is empty")
	}
	if s.DefinitionDigest == "" {
		return fmt.Errorf("snapshot definition digest is empty")
	}
	for name, ref := range s.Schemas {
		if err := validateRefSnapshot("schema", name, ref); err != nil {
			return err
		}
	}
	for name, ref := range s.Templates {
		if err := validateRefSnapshot("template", name, ref); err != nil {
			return err
		}
	}
	for name, ref := range s.Skills {
		if err := validateRefSnapshot("skill", name, ref); err != nil {
			return err
		}
	}
	for name, ref := range s.Verifiers {
		if err := validateRefSnapshot("verifier", name, ref); err != nil {
			return err
		}
	}
	return nil
}

func validateRefSnapshot(kind, name string, ref RefSnapshot) error {
	if ref.Digest == "" {
		return fmt.Errorf("snapshot %s %q digest is empty", kind, name)
	}
	// Empty content is a legitimate pin (an empty template file is admissible)
	// when the digest proves the admission pinned empty bytes deliberately.
	// The JSON round-trip drops empty Bytes (omitempty), so the digest is the
	// only durable signal; reject empty bytes whose digest is anything else.
	if len(ref.Bytes) == 0 {
		if ref.Digest != refDigestHex(nil) {
			return fmt.Errorf("snapshot %s %q bytes are empty but digest %q is not the empty-content digest", kind, name, ref.Digest)
		}
		return nil
	}
	want := refDigestHex(ref.Bytes)
	if ref.Digest != want {
		return fmt.Errorf("snapshot %s %q digest %q does not match bytes (want %q)", kind, name, ref.Digest, want)
	}
	return nil
}
