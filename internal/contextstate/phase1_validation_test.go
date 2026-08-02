package contextstate

import (
	"errors"
	"strings"
	"testing"
)

func validAdvanceRequest(t *testing.T) AdvanceRequest {
	t.Helper()
	principal, binding, _, _ := validContractFixture(t)
	return AdvanceRequest{
		OperationID:       "operation-1",
		Principal:         principal,
		SessionID:         principal.SessionID,
		Expected:          Revision{Session: 4, Durable: 6, Source: 8},
		ExpectedBinding:   binding,
		NewSession:        5,
		NewDurable:        7,
		NewSourceSequence: 8,
		NewBinding:        binding,
		Reason:            "operator requested context advance",
	}
}

func TestAdvanceRequestValidate(t *testing.T) {
	valid := validAdvanceRequest(t)
	cases := []struct {
		name   string
		mutate func(*AdvanceRequest)
		valid  bool
	}{
		{name: "valid", valid: true},
		{name: "unbound principal", mutate: func(r *AdvanceRequest) { r.Principal.capability = [32]byte{} }},
		{name: "session mismatch", mutate: func(r *AdvanceRequest) { r.SessionID = "other-session" }},
		{name: "missing operation", mutate: func(r *AdvanceRequest) { r.OperationID = "" }},
		{name: "invalid expected binding", mutate: func(r *AdvanceRequest) { r.ExpectedBinding.Generation = 0 }},
		{name: "invalid new binding", mutate: func(r *AdvanceRequest) { r.NewBinding.Model = "" }},
		{name: "revision does not advance", mutate: func(r *AdvanceRequest) { r.NewDurable-- }},
		{name: "source sequence changes", mutate: func(r *AdvanceRequest) { r.NewSourceSequence++ }},
		{name: "clear and switch", mutate: func(r *AdvanceRequest) { r.ClearActive, r.ActiveCheckpointID = true, "checkpoint-1" }},
		{name: "empty reason", mutate: func(r *AdvanceRequest) { r.Reason = "" }},
		{name: "overlong reason", mutate: func(r *AdvanceRequest) { r.Reason = strings.Repeat("x", 257) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := valid
			if tc.mutate != nil {
				tc.mutate(&r)
			}
			err := r.Validate()
			if tc.valid && err != nil {
				t.Fatalf("valid AdvanceRequest rejected: %v", err)
			}
			if !tc.valid && !errors.Is(err, ErrInvalidDTO) {
				t.Fatalf("invalid AdvanceRequest error = %v, want ErrInvalidDTO", err)
			}
		})
	}
}
