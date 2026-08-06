package ledger

import (
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
	SchemaVersion    int                      `json:"schema_version"`
	DefinitionTOML   []byte                   `json:"definition_toml"`
	DefinitionDigest string                   `json:"definition_digest"`
	Inputs           map[string]string        `json:"inputs,omitempty"`
	Agents           map[string]AgentSnapshot `json:"agents,omitempty"`
	Schemas          map[string]RefSnapshot   `json:"schemas,omitempty"`
	Templates        map[string]RefSnapshot   `json:"templates,omitempty"`
	Skills           map[string]RefSnapshot   `json:"skills,omitempty"`
	Verifiers        map[string]RefSnapshot   `json:"verifiers,omitempty"`
	Delivery         *DeliverySnapshot        `json:"delivery,omitempty"`
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
	return s, nil
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
