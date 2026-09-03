// remote_input_contract_test.go pins this client's remote-input accept/refuse
// rules to api/contracts/chat-sessions.v1.json, the artefact mivia-app-web
// vendors and hashes.
//
// These rules used to live only in Go, with the API keeping its own copy of
// the same decisions. They drifted: the API accepted "approval" and bodies up
// to 65536, both of which this client refuses, so such an input was accepted,
// stored, delivered, and then silently dropped here. A shared record with a
// test on each side is what stops that recurring - a change on one side now
// has to change the contract, which the other side's gate then sees.
package chatsync

import (
	"testing"
)

// contractInputConstraints mirrors structs.sessionInput.constraints.
type contractInputConstraints struct {
	Kinds            []string `json:"kinds"`
	KindsRetiredInDB []string `json:"kindsRetiredInDB"`
	BodyMaxBytes     int      `json:"bodyMaxBytes"`
	BodyLengthUnit   string   `json:"bodyLengthUnit"`

	BodyRequiredExceptKinds []string `json:"bodyRequiredExceptKinds"`
}

func loadInputConstraints(t *testing.T) contractInputConstraints {
	t.Helper()
	c := loadChatSessionsContract(t)
	si, ok := c.Structs["sessionInput"]
	if !ok {
		t.Fatal("the contract records no sessionInput struct")
	}
	if si.Constraints == nil {
		t.Fatal("the contract records no sessionInput.constraints; the shared accept/refuse rules are missing")
	}
	return *si.Constraints
}

// TestAllowedKindsMatchContract proves this client acts on exactly the kinds
// the contract authorises - no more (a kind accepted here that the API never
// stores is dead code) and no fewer (a kind the API stores and delivers but
// this client refuses is the silent black hole that motivated the record).
func TestAllowedKindsMatchContract(t *testing.T) {
	want := loadInputConstraints(t).Kinds
	if len(want) == 0 {
		t.Fatal("the contract authorises no kinds at all")
	}

	for _, k := range want {
		if !allowedRemoteInputKinds[k] {
			t.Errorf("the contract authorises kind %q but allowedRemoteInputKinds refuses it: "+
				"the API can store and deliver an input this client will silently drop", k)
		}
	}
	inContract := make(map[string]bool, len(want))
	for _, k := range want {
		inContract[k] = true
	}
	for k := range allowedRemoteInputKinds {
		if !inContract[k] {
			t.Errorf("this client acts on kind %q, which the contract does not authorise; "+
				"add it to structs.sessionInput.constraints.kinds or stop accepting it", k)
		}
	}
}

// TestRetiredKindsAreRefused proves a kind the contract marks retired is
// refused here. "approval" is a Postgres enum value that cannot be dropped
// and that historical rows may still carry, so this client must keep saying
// no to it rather than treat a stored row as executable.
func TestRetiredKindsAreRefused(t *testing.T) {
	for _, k := range loadInputConstraints(t).KindsRetiredInDB {
		if allowedRemoteInputKinds[k] {
			t.Errorf("kind %q is retired in the contract but this client still acts on it", k)
		}
	}
}

// TestBodyCapMatchesContract pins the byte cap AND its unit. The unit is the
// load-bearing half: the API expressed the same idea with class-validator's
// MaxLength, which counts UTF-16 code units, so 3000 emoji measured 6000
// there and 12000 here. Two sides can agree on the number and still disagree
// on what is accepted.
func TestBodyCapMatchesContract(t *testing.T) {
	got := loadInputConstraints(t)
	if got.BodyMaxBytes != maxRemoteInputBodyBytes {
		t.Errorf("maxRemoteInputBodyBytes = %d, contract records %d", maxRemoteInputBodyBytes, got.BodyMaxBytes)
	}
	if got.BodyLengthUnit != "utf8-bytes" {
		t.Errorf("contract bodyLengthUnit = %q, want %q: this client measures len(string), which is UTF-8 bytes",
			got.BodyLengthUnit, "utf8-bytes")
	}
}

// TestBodyCapIsMeasuredInBytesNotRunes is the behavioural half of the unit
// pin: a body under the cap in CHARACTERS but over it in BYTES must be
// refused. A reviewer reading only the constant cannot tell these apart.
func TestBodyCapIsMeasuredInBytesNotRunes(t *testing.T) {
	// Each emoji is 4 UTF-8 bytes and 2 UTF-16 code units.
	body := ""
	for len(body) <= maxRemoteInputBodyBytes {
		body += "\U0001F600"
	}
	runes := len([]rune(body))
	if runes >= maxRemoteInputBodyBytes {
		t.Fatalf("test body has %d runes, which is not under the cap; it cannot distinguish the units", runes)
	}
	if len(body) <= maxRemoteInputBodyBytes {
		t.Fatalf("test body is %d bytes, which is not over the cap", len(body))
	}

	p := &InputPoller{sessionID: "sess-1"}
	_, reason := p.validateRemoteInput(t.Context(), &SessionInput{
		SessionID: "sess-1", Kind: "message", Body: body,
	})
	if reason == "" {
		t.Fatal("a body over the BYTE cap was accepted; the cap is being measured in characters")
	}
	if got := "body exceeds"; len(reason) < len(got) || reason[:len(got)] != got {
		t.Errorf("refusal reason = %q, want the body-cap refusal", reason)
	}
}
