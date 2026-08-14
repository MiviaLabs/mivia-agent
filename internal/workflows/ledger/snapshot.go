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
	VerifierPinsVersion int `json:"verifier_pins_version,omitempty"`
	// StackingSemanticsVersion marks snapshots admitted under opt-in
	// stacking activation (StackingSemanticsOptIn): the run's compiled shape
	// is exactly what the strict compile of DefinitionTOML yields, so resume
	// must not apply the legacy inference activation. 0 means the run
	// predates the marker and was admitted under the legacy semantics.
	StackingSemanticsVersion int               `json:"stacking_semantics_version,omitempty"`
	Delivery                 *DeliverySnapshot `json:"delivery,omitempty"`
}

// StackingSemanticsOptIn is the StackingSemanticsVersion for runs admitted
// under opt-in stacking activation (explicit [stacking] table required).
const StackingSemanticsOptIn = 1

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

// Validate checks the snapshot for admission invariants: schema version
// supported, non-empty definition bytes, and non-empty definition digest.
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
	return nil
}
