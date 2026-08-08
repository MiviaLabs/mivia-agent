package coordinator

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// pinnedRequestFingerprint is the golden identity of the fixed pin fixture
// below. Any accidental widening of fingerprintTask (or change to the
// projection of work-defining Task fields) fails this pin and must be a
// conscious update - mailbox/message runtime state must never enter it.
//
// Computed from requestFingerprint of pinFixtureTasks().
const pinnedRequestFingerprint = "sha256:fbc7d7cdcb814cedf0088d134c031f52b53f2cedd0be435374f52c28f33cf436"

func pinFixtureTasks() []subagents.Task {
	return []subagents.Task{{
		ID: "pin-task-1", Name: "pin-worker",
		Input:        json.RawMessage(`"fingerprint-pin-fixture"`),
		Timeout:      time.Second,
		Budget:       1,
		Scope:        "scope",
		Permission:   "permission",
		AgentName:    "pin-worker",
		AgentDigest:  "sha256:pin-agent-v1",
		Skill:        "pin-skill",
		ProviderName: "deepseek",
		Model:        "deepseek-v4-flash",
	}}
}

// TestRequestFingerprintPin guards fingerprint stability for plan 53:
// message mailboxes and runtime message state travel via context or
// non-fingerprinted handles only. Widening fingerprintTask fails this pin.
func TestRequestFingerprintPin(t *testing.T) {
	got, err := requestFingerprint(pinFixtureTasks())
	if err != nil {
		t.Fatal(err)
	}
	if got != pinnedRequestFingerprint {
		t.Fatalf("request fingerprint pin broken:\n  got  %s\n  want %s\n"+
			"If you intentionally changed work-defining Task fields or fingerprintTask, update pinnedRequestFingerprint.\n"+
			"Do not put mailbox/message runtime state on fingerprinted fields.",
			got, pinnedRequestFingerprint)
	}
}

func TestRequestFingerprintPinIgnoresCallerOnlyFields(t *testing.T) {
	base := pinFixtureTasks()[0]
	want, err := requestFingerprint([]subagents.Task{base})
	if err != nil {
		t.Fatal(err)
	}
	// Caller identity and coordination keys must not affect the pin.
	mutated := base
	mutated.Owner = "other"
	mutated.SessionID = "sess-x"
	mutated.TurnID = "turn-x"
	mutated.Role = "role-x"
	mutated.Depth = 9
	mutated.InvocationKey = "inv"
	mutated.IdempotencyKey = "idem"
	got, err := requestFingerprint([]subagents.Task{mutated})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("caller fields changed fingerprint: got %s want %s", got, want)
	}
	if got != pinnedRequestFingerprint {
		t.Fatalf("pin drift: %s", got)
	}
}
