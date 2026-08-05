package ledger

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"
)

func TestInputDigestMapOrder(t *testing.T) {
	want := DigestHex([]byte(`{"alpha":"2","zeta":"1"}`))
	first := InputDigest(map[string]string{"zeta": "1", "alpha": "2"})
	second := InputDigest(map[string]string{"alpha": "2", "zeta": "1"})
	if first != want || second != want {
		t.Fatalf("InputDigest map order: first %q, second %q, want %q", first, second, want)
	}
}

func TestInputDigestNilMatchesEmptyObject(t *testing.T) {
	want := DigestHex([]byte(`{}`))
	if got := InputDigest(nil); got != want {
		t.Fatalf("InputDigest(nil) = %q, want %q", got, want)
	}
	if got := InputDigest(map[string]string{}); got != want {
		t.Fatalf("InputDigest(empty) = %q, want %q", got, want)
	}
}

func TestSnapshotAgentBindingRoundTrip(t *testing.T) {
	data := []byte(`{"schema_version":1,"definition_toml":"bmFtZSA9IFwiZGVtb1wi","definition_digest":"digest","agents":{"worker":{"digest":"agent-digest","provider_name":"openai","model":"gpt-test"}}}`)
	snapshot, err := UnmarshalSnapshot(data)
	if err != nil {
		t.Fatalf("UnmarshalSnapshot: %v", err)
	}
	agent := snapshot.Agents["worker"]
	if agent.Digest != "agent-digest" || agent.ProviderName != "openai" || agent.Model != "gpt-test" {
		t.Fatalf("agent binding = %+v", agent)
	}
	got, err := MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatalf("MarshalSnapshot: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("agent binding round-trip:\ngot:  %s\nwant: %s", got, data)
	}
}

func TestSnapshotLegacyAgentDecode(t *testing.T) {
	data := []byte(`{"schema_version":1,"definition_toml":"bmFtZSA9IFwiZGVtb1wi","definition_digest":"digest","agents":{"worker":{"digest":"agent-digest","version":2}}}`)
	snapshot, err := UnmarshalSnapshot(data)
	if err != nil {
		t.Fatalf("UnmarshalSnapshot: %v", err)
	}
	agent := snapshot.Agents["worker"]
	if agent.Digest != "agent-digest" || agent.ProviderName != "" || agent.Model != "" {
		t.Fatalf("legacy agent = %+v", agent)
	}
	got, err := MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatalf("MarshalSnapshot: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("legacy agent decode:\ngot:  %s\nwant: %s", got, data)
	}
}

// TestMarshalSnapshotRoundTrip asserts that MarshalSnapshot produces canonical
// JSON that UnmarshalSnapshot decodes back to a byte-identical Snapshot.
func TestMarshalSnapshotRoundTrip(t *testing.T) {
	s := Snapshot{
		SchemaVersion:    SnapshotSchemaVersion,
		DefinitionTOML:   []byte("name = \"demo\"\nrun = \"ci\"\n"),
		DefinitionDigest: "sha256:deadbeef",
		Inputs:           map[string]string{"branch": "main", "tag": "v1.2.3"},
		Agents: map[string]AgentSnapshot{
			"assistant": {Digest: "ad1", ProviderName: "openai", Model: "gpt-test"},
			"worker":    {Digest: "ad2"},
		},
		Schemas: map[string]RefSnapshot{
			"run": {Digest: "sd1", Bytes: []byte(`{"type":"object"}`)},
		},
		Templates: map[string]RefSnapshot{
			"summary": {Digest: "td1", Version: 1, Bytes: []byte("{{.run}}")},
		},
		Verifiers: map[string]RefSnapshot{
			"policy": {Digest: "vd1"},
		},
		Delivery: &DeliverySnapshot{Mode: "push", Provider: "github", Base: "main"},
	}

	data, err := MarshalSnapshot(s)
	if err != nil {
		t.Fatalf("MarshalSnapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("MarshalSnapshot returned empty output")
	}

	got, err := UnmarshalSnapshot(data)
	if err != nil {
		t.Fatalf("UnmarshalSnapshot: %v", err)
	}
	if !reflect.DeepEqual(got, s) {
		t.Fatalf("round-trip mismatch:\ngot:  %+v\nwant: %+v", got, s)
	}
}

// TestMarshalSnapshotCanonicalDeterminism asserts that snapshots with the same
// content but different map insertion order marshal to byte-identical JSON,
// and that the canonical form matches encoding/json's key-sorted output for
// Inputs and all four reference maps.
func TestMarshalSnapshotCanonicalDeterminism(t *testing.T) {
	ref := func(digest string) RefSnapshot {
		return RefSnapshot{Digest: digest, Version: 1, Bytes: []byte("payload")}
	}
	mk := func(inputs map[string]string, agents map[string]AgentSnapshot, schemas, templates, verifiers map[string]RefSnapshot) Snapshot {
		return Snapshot{
			SchemaVersion:    SnapshotSchemaVersion,
			DefinitionTOML:   []byte("name = \"demo\"\n"),
			DefinitionDigest: "abc123",
			Inputs:           inputs,
			Agents:           agents,
			Schemas:          schemas,
			Templates:        templates,
			Verifiers:        verifiers,
		}
	}

	// Same key sets, different insertion order.
	a := mk(
		map[string]string{"zeta": "1", "alpha": "2", "mike": "3"},
		map[string]AgentSnapshot{"z": {Digest: "zd"}, "a": {Digest: "ad"}, "m": {Digest: "md"}},
		map[string]RefSnapshot{"run": ref("rd"), "check": ref("cd"), "build": ref("bd")},
		map[string]RefSnapshot{"summary": ref("td"), "pr": ref("pd"), "issue": ref("id")},
		map[string]RefSnapshot{"policy": ref("vd"), "audit": ref("aud"), "guard": ref("gd")},
	)
	b := mk(
		map[string]string{"mike": "3", "alpha": "2", "zeta": "1"},
		map[string]AgentSnapshot{"m": {Digest: "md"}, "z": {Digest: "zd"}, "a": {Digest: "ad"}},
		map[string]RefSnapshot{"build": ref("bd"), "run": ref("rd"), "check": ref("cd")},
		map[string]RefSnapshot{"issue": ref("id"), "summary": ref("td"), "pr": ref("pd")},
		map[string]RefSnapshot{"guard": ref("gd"), "policy": ref("vd"), "audit": ref("aud")},
	)

	dataA, err := MarshalSnapshot(a)
	if err != nil {
		t.Fatalf("MarshalSnapshot(a): %v", err)
	}
	dataB, err := MarshalSnapshot(b)
	if err != nil {
		t.Fatalf("MarshalSnapshot(b): %v", err)
	}
	if !bytes.Equal(dataA, dataB) {
		t.Fatalf("insertion-order variants marshaled differently:\nA: %s\nB: %s", dataA, dataB)
	}

	// encoding/json sorts map keys, so the canonical output must equal it.
	want, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !bytes.Equal(dataA, want) {
		t.Fatalf("canonical JSON diverges from encoding/json:\ngot:  %s\nwant: %s", dataA, want)
	}
}

// TestSnapshotDigest asserts SnapshotDigest is the lowercase hex SHA-256 of the
// snapshot bytes and is stable across calls.
func TestSnapshotDigest(t *testing.T) {
	data := []byte(`{"schema_version":1,"definition_toml":"bmFtZQ=="}`)
	sum := sha256.Sum256(data)
	want := hex.EncodeToString(sum[:])

	first := SnapshotDigest(data)
	if first != want {
		t.Fatalf("SnapshotDigest = %q, want %q", first, want)
	}
	if again := SnapshotDigest(data); again != want {
		t.Fatalf("SnapshotDigest unstable: first %q, second %q", first, again)
	}
}

// TestDigestHex asserts DigestHex is the lowercase hex SHA-256 for arbitrary
// bytes, including empty and nil inputs.
func TestDigestHex(t *testing.T) {
	payloads := [][]byte{
		[]byte("hello world"),
		[]byte{0x00, 0x01, 0xfe, 0xff},
		[]byte(`{"a":1}`),
		[]byte{},
		nil,
	}
	for _, p := range payloads {
		sum := sha256.Sum256(p)
		want := hex.EncodeToString(sum[:])
		if got := DigestHex(p); got != want {
			t.Fatalf("DigestHex(%q) = %q, want %q", p, got, want)
		}
	}
}

// TestSnapshotValidate asserts admission invariants: valid snapshots pass and
// unsupported versions, empty TOML, and empty digests are rejected.
func TestSnapshotValidate(t *testing.T) {
	valid := Snapshot{
		SchemaVersion:    SnapshotSchemaVersion,
		DefinitionTOML:   []byte("name = \"demo\""),
		DefinitionDigest: "digest",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid snapshot rejected: %v", err)
	}

	cases := []struct {
		name string
		s    Snapshot
	}{
		{"schema version 0", Snapshot{SchemaVersion: 0, DefinitionTOML: []byte("x"), DefinitionDigest: "d"}},
		{"schema version 2", Snapshot{SchemaVersion: 2, DefinitionTOML: []byte("x"), DefinitionDigest: "d"}},
		{"empty definition TOML", Snapshot{SchemaVersion: SnapshotSchemaVersion, DefinitionTOML: nil, DefinitionDigest: "d"}},
		{"empty definition digest", Snapshot{SchemaVersion: SnapshotSchemaVersion, DefinitionTOML: []byte("x"), DefinitionDigest: ""}},
	}
	for _, tc := range cases {
		if err := tc.s.Validate(); err == nil {
			t.Errorf("%s: expected error, got nil", tc.name)
		}
	}
}

// TestSnapshotUnmarshalRejectsMalformedJSON asserts that malformed JSON is
// rejected with an error rather than silently decoded.
func TestSnapshotUnmarshalRejectsMalformedJSON(t *testing.T) {
	malformed := [][]byte{
		[]byte(`{"schema_version":`),
		[]byte(`{"schema_version": 1,`),
		[]byte(`not json`),
		[]byte(``),
	}
	for _, data := range malformed {
		if _, err := UnmarshalSnapshot(data); err == nil {
			t.Errorf("UnmarshalSnapshot(%q): expected error, got nil", data)
		}
	}
}

// TestSnapshotRefRoundTrip asserts RefSnapshot marshal/unmarshal round-trips
// and that empty Bytes are omitted from the JSON (omitempty).
func TestSnapshotRefRoundTrip(t *testing.T) {
	omitted, err := json.Marshal(RefSnapshot{Digest: "d", Version: 2, Bytes: []byte{}})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if bytes.Contains(omitted, []byte(`"bytes"`)) {
		t.Errorf("empty Bytes should be omitted, got: %s", omitted)
	}

	cases := []RefSnapshot{
		{Digest: "d1"},
		{Digest: "d2", Version: 3},
		{Digest: "d3", Version: 1, Bytes: []byte(`{"type":"object"}`)},
	}
	for _, ref := range cases {
		data, err := json.Marshal(ref)
		if err != nil {
			t.Fatalf("json.Marshal(%+v): %v", ref, err)
		}
		var got RefSnapshot
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("json.Unmarshal(%s): %v", data, err)
		}
		if !reflect.DeepEqual(got, ref) {
			t.Errorf("RefSnapshot round-trip mismatch:\ngot:  %+v\nwant: %+v", got, ref)
		}
	}
}

// TestSnapshotDeliveryRoundTrip asserts DeliverySnapshot marshal/unmarshal
// round-trips and that an empty Base is omitted from the JSON (omitempty).
func TestSnapshotDeliveryRoundTrip(t *testing.T) {
	omitted, err := json.Marshal(DeliverySnapshot{Mode: "pr", Provider: "gitlab"})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if bytes.Contains(omitted, []byte(`"base"`)) {
		t.Errorf("empty Base should be omitted, got: %s", omitted)
	}

	cases := []DeliverySnapshot{
		{Mode: "push", Provider: "github", Base: "main"},
		{Mode: "pr", Provider: "gitlab"},
	}
	for _, d := range cases {
		data, err := json.Marshal(d)
		if err != nil {
			t.Fatalf("json.Marshal(%+v): %v", d, err)
		}
		var got DeliverySnapshot
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("json.Unmarshal(%s): %v", data, err)
		}
		if !reflect.DeepEqual(got, d) {
			t.Errorf("DeliverySnapshot round-trip mismatch:\ngot:  %+v\nwant: %+v", got, d)
		}
	}
}
